package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type DiagnosticFilter struct {
	Code       string
	Source     string
	PathPrefix string
	Confidence string
	Page       int
}

func (db *DB) ExplainDiagnostic(ctx context.Context, code string) ([]Diagnostic, error) {
	return db.ExplainDiagnosticFiltered(ctx, DiagnosticFilter{Code: code})
}

func (db *DB) ExplainDiagnosticFiltered(ctx context.Context, f DiagnosticFilter) ([]Diagnostic, error) {
	projectSource, err := db.projectSourceName(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT d.source,d.severity,d.code,d.message,COALESCE(d.path,''),COALESCE(d.line,0),COALESCE(d.col,0),COALESCE(fi.source_name,d.source_layer,''),d.confidence,d.fingerprint,d.occurrences
		FROM diagnostics d LEFT JOIN files fi ON fi.id=d.file_id
		LEFT JOIN source_layers sl ON lower(sl.name)=lower(COALESCE(fi.source_name,d.source_layer,''))
		WHERE (?='' OR d.code=?) AND (?='' OR fi.source_name=?) AND (?='' OR d.path LIKE ?) AND (?='' OR d.confidence=?)
		ORDER BY CASE d.severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
			CASE WHEN sl.role=? THEN 0 ELSE 1 END,
			d.code,d.path,d.line`, f.Code, f.Code, f.Source, f.Source, f.PathPrefix, f.PathPrefix+"%", f.Confidence, f.Confidence, string(SourceRoleProject))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column, &d.SourceLayer, &d.Confidence, &d.Fingerprint, &d.Occurrences); err != nil {
			return nil, err
		}
		d.Suggestion, d.RuleSource = diagnosticHint(d.Code, d.Message)
		if d.Confidence == "medium" {
			d.Confidence = diagnosticConfidence(d.Code, d.Severity)
		}
		if d.Fingerprint == "" {
			d.Fingerprint = diagnosticFingerprint(d)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return prioritizeProjectDiagnostics(aggregateDiagnostics(out), projectSource), nil
}

// prioritizeProjectDiagnostics preserves severity ordering while keeping the
// configured project layer first within a severity class. The source name is
// resolved from persisted SourceRole metadata, so renamed project sources do
// not fall behind dependencies merely because they are not named "project".
func prioritizeProjectDiagnostics(diagnostics []Diagnostic, projectSource string) []Diagnostic {
	if strings.TrimSpace(projectSource) == "" {
		return diagnostics
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		leftSeverity := diagnosticSeverityOrder(diagnostics[i].Severity)
		rightSeverity := diagnosticSeverityOrder(diagnostics[j].Severity)
		if leftSeverity != rightSeverity {
			return leftSeverity < rightSeverity
		}
		leftProject := strings.EqualFold(diagnostics[i].SourceLayer, projectSource)
		rightProject := strings.EqualFold(diagnostics[j].SourceLayer, projectSource)
		if leftProject != rightProject {
			return leftProject
		}
		return false
	})
	return diagnostics
}

func diagnosticSeverityOrder(severity string) int {
	switch severity {
	case "error":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func diagnosticConfidence(code, severity string) string {
	if severity == "error" || code == "scope_mismatch" || code == "missing_object_reference" {
		return "high"
	}
	if code == "scope_uncertain" || code == "resource_resolution_uncertain" {
		return "low"
	}
	return "medium"
}

func resourceDiagnostic(name string) (string, string) {
	n := filepathSlash(strings.TrimSpace(name))
	// Only a source-root-qualified path is a deterministic missing resource.
	// Bare filenames and layer-relative fragments require owning-context resolution.
	if strings.Contains(n, "/") && filepath.Ext(n) != "" && (strings.HasPrefix(n, "gfx/") || strings.HasPrefix(n, "sound/") || strings.HasPrefix(n, "map_data/")) {
		return "missing_resource", "warning"
	}
	return "resource_resolution_uncertain", "info"
}

func diagnosticFingerprint(d Diagnostic) string {
	target := d.Message
	if a := strings.Index(target, "'"); a >= 0 {
		if b := strings.Index(target[a+1:], "'"); b >= 0 {
			target = target[a+1 : a+1+b]
		}
	}
	if d.Code == "missing_localization" || d.Code == "missing_resource" || d.Code == "resource_resolution_uncertain" {
		return d.Code + ":" + strings.ToLower(target)
	}
	return fmt.Sprintf("%s:%s:%d:%s", d.Code, filepathSlash(d.Path), d.Line, strings.ToLower(target))
}

func diagnosticRank(d Diagnostic) int {
	if d.Severity == "error" {
		return 0
	}
	if d.Confidence == "high" && (strings.HasPrefix(d.Code, "scope_") || strings.Contains(d.Code, "reference")) {
		return 1
	}
	if d.Code == "missing_resource" || d.Code == "resource_resolution_uncertain" {
		return 2
	}
	if strings.Contains(d.Code, "localization") || strings.Contains(d.Code, "_loc") {
		return 3
	}
	if d.Confidence == "low" || strings.Contains(d.Code, "uncertain") {
		return 5
	}
	return 4
}

func aggregateDiagnostics(in []Diagnostic) []Diagnostic {
	by := map[string]int{}
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		key := d.Fingerprint
		if key == "" {
			key = diagnosticFingerprint(d)
			d.Fingerprint = key
		}
		if i, ok := by[key]; ok {
			out[i].Occurrences += maxInt(1, d.Occurrences)
			continue
		}
		if d.Occurrences < 1 {
			d.Occurrences = 1
		}
		by[key] = len(out)
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := diagnosticRank(out[i]), diagnosticRank(out[j])
		if ri != rj {
			return ri < rj
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func diagnosticHint(code, message string) (string, string) {
	switch code {
	case "scope_mismatch":
		if strings.Contains(message, "can_recruit") && strings.Contains(message, "has_cultural") {
			return "In men_at_arms_type.can_recruit, wrap culture-scope triggers in culture = { ... }, or use valid_for_maa_trigger = { PARAMETER = unlock_maa_xxx }.", "ck3-index:maa_can_recruit_scope"
		}
		return "Move this trigger/effect into a block with the required scope, or use a scope transition/iterator that provides that scope before calling it.", "ck3-index:scope_rules"
	case "missing_localization":
		return "Add the referenced localization key under localization/<language>/, or change the script to reference an existing key. Do not use localization text as mechanism evidence.", "ck3-index:localization_refs"
	case "localization_invalid_character":
		return "Remove the replacement character or control byte from the localization source and save it as valid UTF-8. Comments are checked too because CK3 still reads the whole localization file.", "ck3-index:localization_syntax"
	case "localization_entry_syntax":
		return "Close the localization value with the matching quote. Keep the key's value quoted; CK3 macro arguments such as \\\"\\\" are accepted when the outer value is closed.", "ck3-index:localization_syntax"
	case "localization_macro_syntax":
		return "Balance the localization square brackets and Concept/SelectLocalization/Select_CString-style call. Preserve valid escaped quotes inside macro arguments.", "ck3-index:localization_syntax"
	case "missing_resource":
		return "Add the referenced resource at the resolved CK3 path, or update the script to point at an existing indexed resource. For culture tradition layers, prefer layer-relative filenames such as 4 = icon.dds.", "ck3-index:resource_refs"
	case "resource_resolution_uncertain":
		return "This is a bare or context-relative filename. Resolve it against the owning gfx/script context before treating it as missing.", "ck3-index:resource_resolution"
	case "missing_object_reference":
		return "Define the referenced object in the current project or verify the ref kind/name against active upstream objects. If it is dynamic state, model it as flag/global_var rather than a normal object ref.", "ck3-index:object_refs"
	case "unsupported_event_field":
		return "Remove is_triggered_only from the CK3 event definition. Control when the event is called through its caller, trigger, or on_action.", "ck3-index:event_runtime_contract"
	case "event_option_selection_conflict":
		return "Choose either ai_chance or ai_will_select for this event option. CK3 1.19 treats both as competing AI-selection grammars and prefers ai_will_select.", "ck3-index:event_runtime_contract"
	case "invalid_script_value_field":
		return "Use value = ... to initialize a CK3 script value, then use add/multiply/subtract/etc. Do not use the scripted-modifier field base here.", "ck3-index:script_value_runtime_contract"
	case "unknown_modifier_field":
		return "Replace the field with a current CK3 modifier tag from modifiers.log/vanilla modifier formats; typos and removed 1.19 fields are not valid modifier entries.", "ck3-index:modifier_runtime_contract"
	case "invalid_modifier_context":
		return "Move the modifier to a container whose use area matches the engine contract, such as character_modifier, province_modifier, county_modifier, culture_modifier, travel_plan_modifier, or scheme_modifier.", "ck3-index:modifier_runtime_contract"
	case "illegal_modifier_container":
		return "Replace the obsolete modifier container with a CK3 1.19 container documented by vanilla examples, such as character_modifier, province_modifier, county_modifier, or a culture modifier container.", "ck3-index:modifier_runtime_contract"
	case "unknown_modifier_definition":
		return "Do not invent a modifier type in common/modifier_definition_formats; define formatting only for a code-defined modifier tag confirmed by the current engine evidence.", "ck3-index:modifier_runtime_contract"
	case "duplicate_on_action_field":
		return "Keep one trigger and one effect block per named on_action. Append behavior through events/on_actions or call a separate custom on_action.", "ck3-index:on_action_runtime_contract"
	case "illegal_field_context":
		return "Move or rename the field according to the module's CK3 runtime contract. The parser can accept arbitrary keys, but CK3 only loads fields valid for that container.", "ck3-index:runtime_field_contract"
	case "unregistered_government_type":
		return "Register the custom government id in NGovernment.GOVERNMENT_TYPES in an active common/defines file, then run a full scan before testing the government-dependent modifiers.", "ck3-index:government_runtime_contract"
	case "opinion_modifier_time_conflict":
		return "Choose one opinion-modifier time grammar: monthly_change for a changing modifier, or days/months/years for a fixed duration.", "ck3-index:opinion_modifier_runtime_contract"
	case "opinion_modifier_mode_conflict":
		return "Use only one of decaying = yes and growing = yes on an opinion modifier; they describe opposite change modes.", "ck3-index:opinion_modifier_runtime_contract"
	case "opinion_modifier_invalid_delay":
		return "Only use delay_days/months/years together with decaying = yes on an opinion modifier.", "ck3-index:opinion_modifier_runtime_contract"
	case "opinion_modifier_missing_duration":
		return "A decaying or growing opinion modifier must define monthly_change or a days/months/years duration.", "ck3-index:opinion_modifier_runtime_contract"
	case "opinion_modifier_invalid_value":
		return "Set opinion modifier monthly_change to zero or a positive value; use decaying/growing to control its direction. Negative values are not valid in this contract.", "ck3-index:opinion_modifier_runtime_contract"
	case "unknown_scripted_relation_modifier":
		return "Use a current static character modifier in scripted_relations.modifier. Scheme- or lifestyle-generated modifier tags are not valid in this relation contract.", "ck3-index:scripted_relation_runtime_contract"
	case "scripted_relation_flag_limit":
		return "Reduce the scripted relation flags to 32 or fewer; CK3 stores at most 32 flags per relation.", "ck3-index:scripted_relation_runtime_contract"
	case "religion_doctrine_order":
		return "Move religion-level doctrine entries before the faiths block. Doctrine entries nested inside faiths remain inside their faith definition.", "ck3-index:religion_runtime_contract"
	case "name_list_probability_sum":
		return "Lower the paternal/maternal grandfather or grandmother and parent-name chances so each sex-specific total is at most 100.", "ck3-index:name_list_runtime_contract"
	case "activity_duplicate_category":
		return "Give each activity option category a unique name within the activity type.", "ck3-index:activity_runtime_contract"
	case "activity_duplicate_option":
		return "Give each option a unique name within its activity option category.", "ck3-index:activity_runtime_contract"
	case "activity_duplicate_phase":
		return "Give each activity phase a unique name within the activity type.", "ck3-index:activity_runtime_contract"
	case "activity_missing_phase":
		return "Add at least one named phase to the activity type; CK3 requires an executable active phase.", "ck3-index:activity_runtime_contract"
	case "situation_takeover_conflict":
		return "Choose either takeover_points or takeover_duration for the future phase; the CK3 situation contract forbids both on the same phase transition.", "ck3-index:situation_runtime_contract"
	case "situation_missing_phase":
		return "Add at least one named phase under phases = { ... }; a situation without an active phase cannot run its phase logic.", "ck3-index:situation_runtime_contract"
	case "trait_genetic_inheritance_conflict":
		return "Choose genetic = yes or a manual inherit_chance/both_parent_has_trait_inherit_chance grammar for this trait; CK3 does not allow both inheritance systems together.", "ck3-index:trait_runtime_contract"
	case "trait_opinion_gender_conflict":
		return "Choose male_only = yes or female_only = yes for a triggered_opinion entry; enabling both makes the opinion entry unreachable.", "ck3-index:trait_runtime_contract"
	case "trait_track_duplicate_name":
		return "Give each direct trait track a unique name within the trait's tracks block.", "ck3-index:trait_runtime_contract"
	case "trait_track_xp_range":
		return "Keep literal trait track thresholds between 0 and 100 inclusive; use a separate named level for nonnumeric track entries.", "ck3-index:trait_runtime_contract"
	case "trait_track_xp_order":
		return "Order literal trait track thresholds from low to high so the engine can resolve the first matching level deterministically.", "ck3-index:trait_runtime_contract"
	case "innovation_asset_display_missing":
		return "Add at least one display field to the innovation asset block: name = <key> or icon = <path>.", "ck3-index:innovation_runtime_contract"
	case "event_transition_invalid_duration":
		return "Set transition duration to a literal value greater than zero; zero and negative durations are invalid.", "ck3-index:event_transition_runtime_contract"
	case "event_2d_invalid_duration":
		return "Set effect_2d duration to zero or a positive literal value; negative durations are invalid.", "ck3-index:event_2d_runtime_contract"
	case "event_theme_missing_required_field":
		return "Add direct background, icon, and sound fields to the event theme definition.", "ck3-index:event_theme_runtime_contract"
	case "house_aspiration_missing_level":
		return "Add at least one named level block to the house aspiration definition.", "ck3-index:house_aspiration_runtime_contract"
	case "dynasty_perk_trait_chance":
		return "Give the dynasty perk traits block at least one nonzero literal AI chance; dynamic values need a separate runtime check.", "ck3-index:dynasty_perk_runtime_contract"
	case "struggle_missing_phase_list":
		return "Add at least one phase under phase_list = { ... }; the struggle needs a phase list to execute.", "ck3-index:struggle_runtime_contract"
	case "struggle_missing_start_phase":
		return "Set start_phase to the name of an initial struggle phase.", "ck3-index:struggle_runtime_contract"
	case "struggle_missing_ending_decision":
		return "Add ending_decisions to at least one struggle phase so the struggle has a defined ending path.", "ck3-index:struggle_runtime_contract"
	case "government_missing_fallback":
		return "Give at least one active government a positive fallback priority; fallback = 0 means that government is not a fallback candidate.", "ck3-index:government_runtime_contract"
	case "government_missing_mechanic_default":
		return "Mark exactly one government in this mechanic_type family with is_mechanic_type_default = yes.", "ck3-index:government_runtime_contract"
	case "government_duplicate_mechanic_default":
		return "Keep only one is_mechanic_type_default = yes government in this mechanic_type family.", "ck3-index:government_runtime_contract"
	case "law_succession_field_context":
		return "Align succession fields with order_of_succession: title_division needs inheritance/noble_family, election_type needs election, appointment_type needs appointment, and appointment cannot define traversal/division/rank.", "ck3-index:law_runtime_contract"
	case "council_task_clone_context":
		return "A council task clone may redefine only position, and it must redefine position; move all other fields to the source task.", "ck3-index:council_task_runtime_contract"
	case "council_task_field_context":
		return "Match council-task fields to their task_type/task_progress: county targets require task_type_county, and current/max values require task_progress_value.", "ck3-index:council_task_runtime_contract"
	case "house_relation_missing_level":
		return "Add at least one named level under levels = { ... } to the house relation type.", "ck3-index:house_relation_runtime_contract"
	case "flavorization_missing_domicile_type":
		return "Add domicile_type = <database key> to a flavourization whose type is domicile.", "ck3-index:flavorization_runtime_contract"
	case "lease_contract_value_range":
		return "Keep lease_liege and rest.max literal shares between 0 and 100 inclusive; dynamic weight formulas are checked at runtime.", "ck3-index:lease_contract_runtime_contract"
	case "lease_contract_hierarchy_context":
		return "Define hierarchy = { ... } before using lease_liege; that share belongs only to hierarchical leases.", "ck3-index:lease_contract_runtime_contract"
	case "lease_contract_enum":
		return "Use one of the lease contract enum values documented by CK3: ruler/lessee for beneficiaries or none/any/strong for hook strength.", "ck3-index:lease_contract_runtime_contract"
	case "subject_contract_contribution_range":
		return "Keep literal subject-contract tax/levy/herd/barter contribution and minimum values between 0 and 1; scripted math needs runtime evaluation.", "ck3-index:subject_contract_runtime_contract"
	case "subject_contract_enum":
		return "Use tree, radiobutton, checkbox, or hidden for a subject contract display_mode.", "ck3-index:subject_contract_runtime_contract"
	case "accolade_name_option_count":
		return "Set num_options to exactly the number of option blocks in the accolade name definition.", "ck3-index:accolade_name_runtime_contract"
	case "culture_era_year":
		return "Give each culture era a literal or runtime-resolvable year, and keep literal years at 0 or greater.", "ck3-index:culture_era_runtime_contract"
	case "ai_war_stance_side":
		return "Set war-stances side to attacker or defender.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_behaviour_attribute":
		return "Set at least one of stronger, weaker, or desperate to yes in behaviour_attributes.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_behaviour_field":
		return "Use only stronger, weaker, and desperate inside behaviour_attributes.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_objective_field":
		return "Use a current war-coordinator objective name and only priority/area inside enemy_unit_province.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_objective_context":
		return "Object-style objectives are valid only for enemy_unit_province; use a numeric priority for other objective names.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_objective_priority":
		return "Keep war-stance priorities as integer values in the inclusive range 0..1000.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_area_enum":
		return "Use wargoal, primary_attacker, primary_attacker_ally, primary_defender, or primary_defender_ally for enemy_unit_province area.", "ck3-index:ai_war_stance_runtime_contract"
	case "ai_war_stance_area_overlap":
		return "Use each enemy_unit_province area at most once across a war stance's objectives blocks.", "ck3-index:ai_war_stance_runtime_contract"
	case "house_unity_stage_points":
		return "Give every house-unity stage a positive integer points value; stages without positive points are ignored by CK3.", "ck3-index:house_unity_runtime_contract"
	case "story_cycle_duration_missing":
		return "Give each story-cycle effect_group one duration field: days, weeks, months, or years.", "ck3-index:story_cycle_runtime_contract"
	case "story_cycle_duration_conflict":
		return "Choose one duration unit per story-cycle effect_group instead of mixing days, weeks, months, and years.", "ck3-index:story_cycle_runtime_contract"
	case "story_cycle_chance_range":
		return "Keep literal story-cycle effect_group chance values between 0 and 100 inclusive.", "ck3-index:story_cycle_runtime_contract"
	case "story_cycle_triggered_effect_shape":
		return "Give every story-cycle triggered_effect an effect block; trigger may be omitted when it inherits the effect_group trigger.", "ck3-index:story_cycle_runtime_contract"
	case "activity_ai_tier_missing":
		return "Complete ai_check_interval_by_tier with barony, county, duchy, kingdom, empire, and hegemony.", "ck3-index:activity_runtime_contract"
	case "activity_intent_default_invalid":
		return "Keep host/guest default and player_defaults entries inside the corresponding intents list.", "ck3-index:activity_runtime_contract"
	case "decision_ai_interval_missing":
		return "Set ai_check_interval or a complete ai_check_interval_by_tier on the decision, unless ai_goal = yes.", "ck3-index:decision_runtime_contract"
	case "decision_picture_missing":
		return "Add at least one direct picture = { reference = \"gfx/...\" } block to the decision. Use separate trigger-gated picture blocks for variants instead of a bare string path.", "ck3-index:decision_runtime_contract"
	case "decision_ai_tier_missing":
		return "Complete decision ai_check_interval_by_tier with barony, county, duchy, kingdom, empire, and hegemony.", "ck3-index:decision_runtime_contract"
	case "interaction_ai_tier_missing":
		return "Complete ai_frequency_by_tier with barony, county, duchy, kingdom, empire, and hegemony.", "ck3-index:character_interaction_runtime_contract"
	case "great_project_ai_tier_missing":
		return "Complete each great-project ai_check_interval_by_tier block with barony, county, duchy, kingdom, empire, and hegemony.", "ck3-index:great_project_runtime_contract"
	case "struggle_missing_future_phase":
		return "Give every non-ending struggle phase at least one future_phases entry; ending phases are the exception.", "ck3-index:struggle_runtime_contract"
	case "struggle_invalid_duration":
		return "Use a positive time duration or an integer point-based duration of at least 1 in struggle phases.", "ck3-index:struggle_runtime_contract"
	case "struggle_phase_reference":
		return "Make start_phase and future_phases names match phases declared in the same phase_list.", "ck3-index:struggle_runtime_contract"
	case "struggle_ending_phase_fields":
		return "Remove ending_decisions, future_phases, and struggle modifier blocks from an ending phase; they are ignored there.", "ck3-index:struggle_runtime_contract"
	case "court_type_duplicate_default":
		return "Keep default = yes on at most one active court type across common/court_types.", "ck3-index:court_type_runtime_contract"
	case "history_character_name_localization_missing":
		return "Add a localization value for the direct character-history name key, or quote the name when it is intended as a literal. Nested set_variable { name = ... } fields are not treated as character names.", "ck3-index:history_character_runtime_contract"
	case "variable_write_only":
		return "Read the project variable from an indexed script, remove the setter, or replace it with the intended flag/state mechanism. Localization references do not count as a runtime read.", "ck3-index:variable_runtime_contract"
	}
	return "", ""
}

func refHint(kind string) (string, string) {
	switch kind {
	case "localization":
		return "Add the localization key in the same patch or an indexed localization file, then run preflight_patch again before writing.", "ck3-index:localization_refs"
	case "resource":
		return "Add the resource file in the same patch or change the script to an existing indexed resource path.", "ck3-index:resource_refs"
	case "sound":
		return "Verify the event:/ sound name against known CK3 sound events or an existing project sound definition.", "ck3-index:sound_refs"
	case "flag", "global_var":
		return "Dynamic flags and variables normally do not require object definitions; if this appears unresolved, check extractor context before changing script.", "ck3-index:dynamic_state_refs"
	default:
		if isObjectRefKind(kind) {
			return "Define the referenced object or change the ref kind/name to an active indexed object; check query_object and find_refs before editing.", "ck3-index:object_refs"
		}
	}
	return "", ""
}
