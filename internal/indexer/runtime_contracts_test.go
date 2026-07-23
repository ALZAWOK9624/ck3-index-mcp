package indexer

import (
	"strings"
	"testing"
)

func runtimeContractHasCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func runtimeContractDetails(diagnostics []Diagnostic) string {
	var out strings.Builder
	for _, diagnostic := range diagnostics {
		out.WriteString(diagnostic.Code)
		out.WriteString(": ")
		out.WriteString(diagnostic.Message)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestRuntimeContractsDetectUnsupportedFieldsAndContexts(t *testing.T) {
	cases := []struct {
		name string
		path string
		text string
		want string
	}{
		{
			name: "event old trigger flag",
			path: "events/runtime_contract_events.txt",
			text: `namespace = runtime_contract
runtime_contract.1 = {
	type = character_event
	is_triggered_only = yes
}
`,
			want: "unsupported_event_field",
		},
		{
			name: "casus belli base",
			path: "common/casus_belli_types/runtime_contract.txt",
			text: `runtime_contract_cb = {
	ai_score_mult = {
		base = 0
		add = 1
	}
}
`,
			want: "invalid_script_value_field",
		},
		{
			name: "government field placement",
			path: "common/governments/runtime_contract.txt",
			text: `runtime_contract_government = {
	government_rules = {
		court_generate_commanders = no
	}
}
`,
			want: "invalid_government_rule_context",
		},
		{
			name: "on action unknown and duplicate field",
			path: "common/on_action/runtime_contract.txt",
			text: `runtime_contract_action = {
	trigger = { always = yes }
	trigger = { always = no }
	effect = { add_gold = 1 }
	effect = { add_gold = 2 }
	unknown_field = yes
}
`,
			want: "illegal_field_context",
		},
		{
			name: "modifier field and context",
			path: "common/buildings/runtime_contract.txt",
			text: `runtime_contract_building = {
	province_modifier = {
		garrison_size_mult = 0.5
		travel_speed = 0.2
	}
	country_modifier = { monthly_income = 1 }
}
`,
			want: "unknown_modifier_field",
		},
		{
			name: "modifier definition field",
			path: "common/modifier_definition_formats/runtime_contract.txt",
			text: `feudal_government_opinion = {
	decimals = 0
	unknown_format_field = yes
}
`,
			want: "illegal_field_context",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analysis, err := AnalyzeVirtualFile(tc.path, "patch", 1, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if !runtimeContractHasCode(analysis.Diagnostics, tc.want) {
				t.Fatalf("expected %s, got:\n%s", tc.want, runtimeContractDetails(analysis.Diagnostics))
			}
		})
	}
}

func TestRuntimeContractsAcceptCurrentVanillaPatterns(t *testing.T) {
	cases := []struct {
		path string
		text string
	}{
		{
			path: "events/runtime_contract_valid.txt",
			text: `namespace = runtime_contract
runtime_contract.1 = {
	type = character_event
	hidden = yes
option = { name = runtime_contract.1.a }
}
`,
		},
		{
			path: "common/casus_belli_types/runtime_contract_valid.txt",
			text: `runtime_contract_cb = {
	ai_score_mult = {
		value = 1
		add = 1
	}
}
`,
		},
		{
			path: "common/governments/runtime_contract_valid.txt",
			text: `runtime_contract_government = {
	government_rules = { legitimacy = yes }
	court_generate_commanders = no
}
`,
		},
		{
			path: "common/on_action/runtime_contract_valid.txt",
			text: `runtime_contract_action = {
	events = { runtime_contract.1 }
	random_on_action = { 100 = runtime_contract_action }
}
`,
		},
		{
			path: "common/buildings/runtime_contract_valid.txt",
			text: `runtime_contract_building = {
	province_modifier = { garrison_size = 1 }
	province_terrain_modifier = {
		parameter = runtime_contract_parameter
		terrain = jungle
		is_coastal = yes
		build_speed = 0.1
	}
	character_culture_modifier = {
		parameter = runtime_contract_parameter
		monthly_prestige = 0.1
	}
}
`,
		},
		{
			path: "common/character_interactions/runtime_contract_valid.txt",
			text: `runtime_contract_interaction = {
	ai_accept = {
		scheme_modifier = {
			object = scope:scheme
			target = scope:recipient
		}
	}
}
`,
		},
		{
			path: "common/dynasty_perks/runtime_contract_valid.txt",
			text: `runtime_contract_legacy = {
	character_modifier = {
		name = runtime_contract_modifier_name
		monthly_piety = 0.1
	}
	doctrine_character_modifier = {
		name = runtime_contract_modifier_name
		doctrine = doctrine_no_head
		clergy_opinion = 5
	}
}
`,
		},
		{
			path: "common/modifier_definition_formats/runtime_contract_valid.txt",
			text: `feudal_government_opinion = {
	decimals = 0
	color = neutral
}
`,
		},
		{
			path: "events/runtime_contract_event_valid.txt",
			text: `namespace = runtime_contract
runtime_contract.2 = {
	option = {
		name = runtime_contract.2.a
		ai_chance = { base = 1 }
	}
}
`,
		},
		{
			path: "common/modifiers/runtime_contract_modifiers_valid.txt",
			text: `runtime_contract_modifier = {
	icon = prestige_positive
	stacking = yes
	hide_effects = yes
	monthly_income = 1
	scale = {
		value = 1
		desc = runtime_contract_desc
		display_mode = scaled
	}
}
`,
		},
		{
			path: "common/opinion_modifiers/runtime_contract_opinions_valid.txt",
			text: `runtime_contract_opinion = {
	opinion = 5
	decaying = yes
	monthly_change = 1
	delay_days = 1
}
`,
		},
		{
			path: "common/opinion_modifiers/runtime_contract_opinions_valid_zero.txt",
			text: `runtime_contract_opinion_zero = {
	opinion = 0
	monthly_change = 0
}
`,
		},
		{
			path: "common/scripted_relations/runtime_contract_relations_valid.txt",
			text: `runtime_contract_relation = {
	flags = { first second }
	modifier = {
		name = runtime_contract_relation_modifier
		monthly_merit = 1
	}
}
`,
		},
		{
			path: "common/story_cycles/runtime_contract_story_valid.txt",
			text: `runtime_contract_story = {
		effect_group = {
			days = 1
			triggered_effect = {
				trigger = { always = yes }
				effect = { add_gold = 1 }
			}
			random_valid = {
				triggered_effect = {
					trigger = { always = yes }
					effect = { add_gold = 2 }
				}
				triggered_effect = {
					trigger = { always = no }
					effect = { add_gold = 3 }
				}
			}
		}
	}
`,
		},
		{
			path: "common/religion/religion_types/runtime_contract_religion_valid.txt",
			text: `runtime_contract_religion = {
		doctrine = doctrine_no_head
		faiths = { }
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			analysis, err := AnalyzeVirtualFile(tc.path, "patch", 1, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range analysis.Diagnostics {
				if strings.HasPrefix(diagnostic.Code, "unsupported_") ||
					strings.HasPrefix(diagnostic.Code, "invalid_") ||
					diagnostic.Code == "illegal_field_context" ||
					diagnostic.Code == "unknown_modifier_field" ||
					diagnostic.Code == "illegal_modifier_container" ||
					diagnostic.Code == "unknown_modifier_definition" {
					t.Fatalf("unexpected runtime-contract diagnostic: %+v", diagnostic)
				}
			}
		})
	}
}

func TestModifierContractsRespectModuleContextAndGeneratedFormats(t *testing.T) {
	cases := []struct {
		path string
		text string
	}{
		{
			path: "common/traits/runtime_contract_trait_modifier.txt",
			text: `runtime_contract_trait = {
	culture_modifier = {
		parameter = heritage_parameter
		monthly_prestige = 1
	}
}`,
		},
		{
			path: "common/court_positions/runtime_contract_court_position.txt",
			text: `runtime_contract_position = {
	culture_modifier = {
		parameter = heritage_parameter
		monthly_piety = 1
	}
}`,
		},
		{
			path: "common/council_tasks/runtime_contract_task.txt",
			text: `runtime_contract_task = {
	county_modifier = {
		scale = 0.5
		development_growth = 0.1
	}
}`,
		},
		{
			path: "common/game_concepts/runtime_contract_concepts.txt",
			text: `character_modifier = {
	texture = "gfx/interface/icons/concepts/test.dds"
	parent = modifier
}`,
		},
		{
			path: "common/modifier_definition_formats/runtime_contract_generated.txt",
			text: `world_ga_aironoi_development_growth = {
	decimals = 2
}`,
		},
		{
			path: "common/buildings/runtime_contract_generated_field.txt",
			text: `runtime_contract_building = {
	character_modifier = {
		k10_archers_damage_add = 0.1
	}
}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			analysis, err := AnalyzeVirtualFile(tc.path, "patch", 1, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range analysis.Diagnostics {
				switch diagnostic.Code {
				case "unknown_modifier_field", "unknown_modifier_definition", "invalid_modifier_context":
					t.Fatalf("valid contextual/generated modifier was rejected: %+v", diagnostic)
				}
			}
		})
	}
}

func TestRuntimeContractsDetectExtendedModuleViolations(t *testing.T) {
	cases := []struct {
		name string
		path string
		text string
		want string
	}{
		{
			name: "event option AI selectors",
			path: "events/runtime_contract_event_ai.txt",
			text: `namespace = runtime_contract
runtime_contract.3 = {
	option = {
		ai_chance = { base = 1 }
		ai_will_select = { value = 1 }
	}
}
`,
			want: "event_option_selection_conflict",
		},
		{
			name: "character modifier field",
			path: "common/modifiers/runtime_contract_modifiers.txt",
			text: `runtime_contract_modifier = {
		icon = prestige_positive
		scale = { value = 1 desc = runtime_contract_desc }
		garrison_size_mult = 1
}
`,
			want: "unknown_modifier_field",
		},
		{
			name: "opinion mode and duration",
			path: "common/opinion_modifiers/runtime_contract_opinions.txt",
			text: `runtime_contract_opinion = {
		decaying = yes
		growing = yes
		monthly_change = -1
		days = 2
		delay_days = 1
}
`,
			want: "opinion_modifier_mode_conflict",
		},
		{
			name: "scripted relation modifier",
			path: "common/scripted_relations/runtime_contract_relations.txt",
			text: `runtime_contract_relation = {
		modifier = {
			seduce_scheme_phase_duration_mult = 1
		}
}
`,
			want: "unknown_scripted_relation_modifier",
		},
		{
			name: "religion doctrine order",
			path: "common/religion/religion_types/runtime_contract_religion.txt",
			text: `runtime_contract_religion = {
		faiths = { }
		doctrine = doctrine_no_head
}
`,
			want: "religion_doctrine_order",
		},
		{
			name: "name list probability sum",
			path: "common/culture/name_lists/runtime_contract_names.txt",
			text: `runtime_contract_names = {
		pat_grf_name_chance = 70
		mat_grf_name_chance = 20
		father_name_chance = 20
}
`,
			want: "name_list_probability_sum",
		},
		{
			name: "activity duplicate option",
			path: "common/activities/activity_types/runtime_contract_activity.txt",
			text: `runtime_contract_activity = {
		options = {
			category = {
				option = { }
				option = { }
			}
		}
		phases = {
			phase = { }
		}
}
`,
			want: "activity_duplicate_option",
		},
		{
			name: "activity missing phase",
			path: "common/activities/activity_types/runtime_contract_activity_missing.txt",
			text: `runtime_contract_activity_missing = {
		phases = { }
}
`,
			want: "activity_missing_phase",
		},
		{
			name: "situation takeover modes",
			path: "common/situation/situations/runtime_contract_situation.txt",
			text: `runtime_contract_situation = {
		phases = {
			phase = {
				future_phases = {
					next = {
						takeover_points = 1
						takeover_duration = { days = 2 }
					}
				}
				}
			}
}
`,
			want: "situation_takeover_conflict",
		},
		{
			name: "trait genetic manual inheritance",
			path: "common/traits/runtime_contract_traits.txt",
			text: `runtime_contract_trait = {
	genetic = yes
	inherit_chance = 25
	triggered_opinion = {
		male_only = yes
		female_only = yes
	}
	tracks = {
		main = {
			20 = { prowess = 1 }
			20 = { prowess = 2 }
			101 = { prowess = 3 }
		}
	}
}
`,
			want: "trait_genetic_inheritance_conflict",
		},
		{
			name: "innovation asset display",
			path: "common/culture/innovations/runtime_contract_innovations.txt",
			text: `runtime_contract_innovation = {
	asset = {
		trigger = { always = yes }
	}
}
`,
			want: "innovation_asset_display_missing",
		},
		{
			name: "event transition duration",
			path: "common/event_transitions/runtime_contract_transitions.txt",
			text: `runtime_contract_transition = {
	transition = {
		duration = 0
	}
}
`,
			want: "event_transition_invalid_duration",
		},
		{
			name: "event 2d duration",
			path: "common/event_2d_effects/runtime_contract_effects.txt",
			text: `runtime_contract_effect = {
		effect_2d = {
			duration = -1
		}
}
`,
			want: "event_2d_invalid_duration",
		},
		{
			name: "event theme required field",
			path: "common/event_themes/runtime_contract_themes.txt",
			text: `runtime_contract_theme = {
		background = { reference = throne_room }
		icon = { reference = icon_key }
}
`,
			want: "event_theme_missing_required_field",
		},
		{
			name: "house aspiration level",
			path: "common/house_aspirations/runtime_contract_aspirations.txt",
			text: `runtime_contract_aspiration = {
	show_in_main_hud = yes
}
`,
			want: "house_aspiration_missing_level",
		},
		{
			name: "dynasty perk zero trait chance",
			path: "common/dynasty_perks/runtime_contract_perks.txt",
			text: `runtime_contract_perk = {
	traits = {
		beauty_good_1 = 0
		intellect_good_1 = 0
	}
}
`,
			want: "dynasty_perk_trait_chance",
		},
		{
			name: "struggle required phase",
			path: "common/struggle/struggles/runtime_contract_struggle.txt",
			text: `runtime_contract_struggle = {
	phase_list = { }
}
`,
			want: "struggle_missing_phase_list",
		},
		{
			name: "situation required phase",
			path: "common/situation/situations/runtime_contract_situation_missing.txt",
			text: `runtime_contract_situation_missing = {
	phases = { }
}
`,
			want: "situation_missing_phase",
		},
		{
			name: "law succession field context",
			path: "common/laws/runtime_contract_laws.txt",
			text: `runtime_contract_law = {
	succession = {
		order_of_succession = election
		title_division = partition
		traversal_order = dynasty
		appointment_type = invalid_for_election
	}
}
`,
			want: "law_succession_field_context",
		},
		{
			name: "council clone field context",
			path: "common/council_tasks/runtime_contract_tasks.txt",
			text: `runtime_contract_task = {
	clone = task_source
	position = councillor_chancellor
	default_task = yes
}
`,
			want: "council_task_clone_context",
		},
		{
			name: "council task field context",
			path: "common/council_tasks/runtime_contract_task_fields.txt",
			text: `runtime_contract_task_fields = {
	task_type = task_type_general
	task_progress = task_progress_percentage
	county_target = realm
	task_current_value = 1
}
`,
			want: "council_task_field_context",
		},
		{
			name: "house relation missing level",
			path: "common/house_relation_types/runtime_contract_house_relation.txt",
			text: `runtime_contract_relation = {
	neutral_level = neutral
	levels = { }
}
`,
			want: "house_relation_missing_level",
		},
		{
			name: "domicile flavourization missing key",
			path: "common/flavorization/runtime_contract_flavourization.txt",
			text: `runtime_contract_domicile = {
	type = domicile
}
`,
			want: "flavorization_missing_domicile_type",
		},
		{
			name: "lease share range",
			path: "common/lease_contracts/runtime_contract_lease.txt",
			text: `runtime_contract_lease = {
		tax = {
			lease_liege = 101
			rest = {
				max = -1
				beneficiary = invalid
			}
		}
		hook_strength_max_opinion = invalid
}
`,
			want: "lease_contract_value_range",
		},
		{
			name: "subject contract contribution range",
			path: "common/subject_contracts/contracts/runtime_contract_subject.txt",
			text: `runtime_contract_subject = {
			display_mode = invalid
			obligation_levels = {
				low = {
					tax = 1.1
					levies = { base = 2 }
				}
			}
}
`,
			want: "subject_contract_contribution_range",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analysis, err := AnalyzeVirtualFile(tc.path, "patch", 1, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if !runtimeContractHasCode(analysis.Diagnostics, tc.want) {
				t.Fatalf("expected %s, got:\n%s", tc.want, runtimeContractDetails(analysis.Diagnostics))
			}
		})
	}
}

func TestRuntimeContractsAcceptExtendedVanillaPatterns(t *testing.T) {
	cases := []struct {
		path string
		text string
	}{
		{
			path: "events/runtime_contract_event_ai_valid.txt",
			text: `namespace = runtime_contract
runtime_contract.4 = {
	option = {
		ai_will_select = { value = 1 }
	}
}

`,
		},
		{
			path: "common/opinion_modifiers/runtime_contract_opinions_valid_duration.txt",
			text: `runtime_contract_opinion_duration = {
		growing = yes
		days = 2
}
`,
		},
		{
			path: "common/religion/religion_types/runtime_contract_religion_valid_nested.txt",
			text: `runtime_contract_religion_nested = {
			doctrine = doctrine_no_head
			faiths = {
				runtime_contract_faith = {
					doctrine = tenet_ancestor_worship
				}
			}
}
`,
		},
		{
			path: "common/culture/name_lists/runtime_contract_names_valid.txt",
			text: `runtime_contract_names_valid = {
		pat_grf_name_chance = 50
		mat_grf_name_chance = 25
		father_name_chance = 25
		pat_grm_name_chance = 30
		mat_grm_name_chance = 30
		mother_name_chance = 40
}
`,
		},
		{
			path: "common/activities/activity_types/runtime_contract_activity_valid.txt",
			text: `runtime_contract_activity_valid = {
		options = {
			category = {
				option_a = { }
				option_b = { }
			}
		}
		phases = {
			phase_a = { }
			phase_b = { }
		}
}
`,
		},
		{
			path: "common/situation/situations/runtime_contract_situation_valid.txt",
			text: `runtime_contract_situation_valid = {
		phases = {
			phase = {
				future_phases = {
					by_points = { takeover_points = 1 }
					by_duration = { takeover_duration = { days = 2 } }
				}
			}
		}
}
`,
		},
		{
			path: "common/traits/runtime_contract_traits_valid.txt",
			text: `runtime_contract_trait_valid = {
	genetic = yes
	triggered_opinion = {
		male_only = yes
	}
	tracks = {
		main = {
			20 = { prowess = 1 }
			50 = { prowess = 2 }
			100 = { prowess = 3 }
		}
	}
}
`,
		},
		{
			path: "common/culture/innovations/runtime_contract_innovations_valid.txt",
			text: `runtime_contract_innovation_valid = {
	asset = {
		trigger = { always = yes }
		icon = innovation_icon
	}
}
`,
		},
		{
			path: "common/event_transitions/runtime_contract_transitions_valid.txt",
			text: `runtime_contract_transition_valid = {
	transition = { duration = 1 }
}
`,
		},
		{
			path: "common/event_2d_effects/runtime_contract_effects_valid.txt",
			text: `runtime_contract_effect_valid = {
		effect_2d = { duration = 0 }
}
`,
		},
		{
			path: "common/event_themes/runtime_contract_themes_valid.txt",
			text: `runtime_contract_theme_valid = {
		background = { reference = throne_room }
		icon = { reference = icon_key }
		sound = { reference = event:/SFX/Test }
}
`,
		},
		{
			path: "common/house_aspirations/runtime_contract_aspirations_valid.txt",
			text: `runtime_contract_aspiration_valid = {
		level = { cost = { gold = 1 } }
}
`,
		},
		{
			path: "common/dynasty_perks/runtime_contract_perks_valid.txt",
			text: `runtime_contract_perk_valid = {
		traits = { beauty_good_1 = 1 }
}
`,
		},
		{
			path: "common/struggle/struggles/runtime_contract_struggle_valid.txt",
			text: `runtime_contract_struggle_valid = {
		start_phase = runtime_phase
		phase_list = {
			runtime_phase = {
				future_phases = { runtime_ending_phase = { } }
				ending_decisions = { runtime_decision }
			}
			runtime_ending_phase = {
				on_start = { }
			}
		}
}
`,
		},
		{
			path: "common/situation/situations/runtime_contract_situation_phase_valid.txt",
			text: `runtime_contract_situation_phase_valid = {
		phases = { runtime_phase = { } }
}
`,
		},
		{
			path: "common/laws/runtime_contract_laws_valid.txt",
			text: `runtime_contract_partition_law = {
	succession = {
		order_of_succession = inheritance
		title_division = partition
		traversal_order = children
	}
}
runtime_contract_election_law = {
	succession = {
		order_of_succession = election
		election_type = feudal_elective
	}
}
runtime_contract_appointment_law = {
	succession = {
		order_of_succession = appointment
		appointment_type = admin_governor
	}
}
runtime_contract_pool_law = {
	succession = {
		order_of_succession = theocratic
		pool_character_config = pool_theocratic_succession
	}
}
`,
		},
		{
			path: "common/council_tasks/runtime_contract_tasks_valid.txt",
			text: `runtime_contract_task_clone = {
	position = minister_justice
	clone = task_fabricate_claim
}
runtime_contract_task_county = {
	position = councillor_chancellor
	task_type = task_type_county
	county_target = realm
	ai_county_target = domain
	task_progress = task_progress_value
	task_current_value = 0
	task_max_value = 100
}
runtime_contract_task_default = {
	position = councillor_chancellor
	default_task = yes
	task_type = task_type_general
	task_progress = task_progress_infinite
}
`,
		},
		{
			path: "common/house_relation_types/runtime_contract_house_relation_valid.txt",
			text: `runtime_contract_relation_valid = {
	neutral_level = neutral
	levels = { neutral = { opinion = 0 } }
}
`,
		},
		{
			path: "common/flavorization/runtime_contract_flavourization_valid.txt",
			text: `runtime_contract_domicile_valid = {
	type = domicile
	domicile_type = camp
}
`,
		},
		{
			path: "common/lease_contracts/runtime_contract_lease_valid.txt",
			text: `runtime_contract_lease_valid = {
		hierarchy = { }
		tax = {
			lease_liege = 25
			rest = { max = 50 beneficiary = ruler rest = lessee }
		}
		levy = { lease_liege = 0 rest = ruler }
		hook_strength_max_opinion = strong
}
`,
		},
		{
			path: "common/subject_contracts/contracts/runtime_contract_subject_valid.txt",
			text: `runtime_contract_subject_valid = {
			display_mode = checkbox
			obligation_levels = {
				low = {
					tax = 0.2
					levies = { base = 2 }
				}
			}
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			analysis, err := AnalyzeVirtualFile(tc.path, "patch", 1, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range analysis.Diagnostics {
				switch diagnostic.Code {
				case "event_option_selection_conflict",
					"opinion_modifier_time_conflict",
					"opinion_modifier_mode_conflict",
					"opinion_modifier_invalid_delay",
					"opinion_modifier_missing_duration",
					"opinion_modifier_invalid_value",
					"unknown_scripted_relation_modifier",
					"scripted_relation_flag_limit",
					"religion_doctrine_order",
					"name_list_probability_sum",
					"activity_duplicate_category",
					"activity_duplicate_option",
					"activity_duplicate_phase",
					"activity_missing_phase",
					"situation_takeover_conflict",
					"trait_genetic_inheritance_conflict",
					"trait_opinion_gender_conflict",
					"trait_track_duplicate_name",
					"trait_track_xp_range",
					"trait_track_xp_order",
					"innovation_asset_display_missing",
					"event_transition_invalid_duration",
					"event_2d_invalid_duration",
					"event_theme_missing_required_field",
					"house_aspiration_missing_level",
					"dynasty_perk_trait_chance",
					"struggle_missing_phase_list",
					"struggle_missing_start_phase",
					"struggle_missing_ending_decision",
					"situation_missing_phase",
					"law_succession_field_context",
					"council_task_clone_context",
					"council_task_field_context",
					"house_relation_missing_level",
					"flavorization_missing_domicile_type",
					"lease_contract_value_range",
					"lease_contract_hierarchy_context",
					"lease_contract_enum",
					"subject_contract_contribution_range",
					"subject_contract_enum":
					t.Fatalf("unexpected extended runtime-contract diagnostic: %+v", diagnostic)
				}
			}
		})
	}
}
func TestRuntimeCatalogContractsDetectViolations(t *testing.T) {
	cases := []struct {
		name string
		path string
		text string
		want string
	}{
		{
			name: "accolade option count",
			path: "common/accolade_names/runtime_contract_accolade.txt",
			text: `runtime_accolade = {
	num_options = 1
	option = { }
	option = { }
}
`,
			want: "accolade_name_option_count",
		},
		{
			name: "culture era year",
			path: "common/culture/eras/runtime_contract_eras.txt",
			text: `runtime_era = { year = -1 }
`,
			want: "culture_era_year",
		},
		{
			name: "war stance side",
			path: "common/ai_war_stances/runtime_contract_stance.txt",
			text: `runtime_stance = {
	side = none
	behaviour_attributes = { stronger = yes }
}
`,
			want: "ai_war_stance_side",
		},
		{
			name: "war stance behaviour",
			path: "common/ai_war_stances/runtime_contract_behaviour.txt",
			text: `runtime_stance = {
	side = attacker
	behaviour_attributes = { stronger = no weaker = no }
}
`,
			want: "ai_war_stance_behaviour_attribute",
		},
		{
			name: "war stance objective context",
			path: "common/ai_war_stances/runtime_contract_objective.txt",
			text: `runtime_stance = {
	side = attacker
	behaviour_attributes = { stronger = yes }
	objectives = { enemy_province = { priority = 1 } }
}
`,
			want: "ai_war_stance_objective_context",
		},
		{
			name: "war stance area overlap",
			path: "common/ai_war_stances/runtime_contract_area.txt",
			text: `runtime_stance = {
	side = attacker
	behaviour_attributes = { stronger = yes }
	objectives = {
		enemy_unit_province = { priority = 1 area = wargoal }
		enemy_unit_province = { priority = 2 area = wargoal }
	}
}
`,
			want: "ai_war_stance_area_overlap",
		},
		{
			name: "house unity points",
			path: "common/house_unities/runtime_contract_unity.txt",
			text: `runtime_unity = {
	default_value = 0
	stage = { points = 0 }
}
`,
			want: "house_unity_stage_points",
		},
		{
			name: "story cycle duration",
			path: "common/story_cycles/runtime_contract_story_duration.txt",
			text: `runtime_story = {
	effect_group = { triggered_effect = { trigger = { always = yes } effect = { } } }
}
`,
			want: "story_cycle_duration_missing",
		},
		{
			name: "story cycle chance",
			path: "common/story_cycles/runtime_contract_story_chance.txt",
			text: `runtime_story = {
	effect_group = {
		days = 1
		chance = 101
		triggered_effect = { trigger = { always = yes } effect = { } }
	}
}
`,
			want: "story_cycle_chance_range",
		},
		{
			name: "story cycle triggered effect shape",
			path: "common/story_cycles/runtime_contract_story_shape.txt",
			text: `runtime_story = {
	effect_group = {
		days = 1
		triggered_effect = { trigger = { always = yes } }
	}
}
`,
			want: "story_cycle_triggered_effect_shape",
		},
		{
			name: "activity tier",
			path: "common/activities/activity_types/runtime_contract_activity_tier.txt",
			text: `runtime_activity = {
	ai_check_interval_by_tier = { barony = 1 }
	phases = { phase = { } }
}
`,
			want: "activity_ai_tier_missing",
		},
		{
			name: "activity intent default",
			path: "common/activities/activity_types/runtime_contract_activity_intent.txt",
			text: `runtime_activity = {
	host_intents = { intents = { valid_intent } default = invalid_intent }
	phases = { phase = { } }
}
`,
			want: "activity_intent_default_invalid",
		},
		{
			name: "decision interval",
			path: "common/decisions/runtime_contract_decision_interval.txt",
			text: `runtime_decision = { is_shown = { always = yes } }
`,
			want: "decision_ai_interval_missing",
		},
		{
			name: "interaction tier",
			path: "common/character_interactions/runtime_contract_interaction_tier.txt",
			text: `runtime_interaction = { ai_frequency_by_tier = { barony = 1 } }
`,
			want: "interaction_ai_tier_missing",
		},
		{
			name: "great project tier",
			path: "common/great_projects/types/runtime_contract_project_tier.txt",
			text: `runtime_project = { ai_check_interval_by_tier = { barony = 1 } }
`,
			want: "great_project_ai_tier_missing",
		},
		{
			name: "struggle future phase",
			path: "common/struggle/struggles/runtime_contract_struggle_future.txt",
			text: `runtime_struggle = {
	start_phase = phase_one
	phase_list = { phase_one = { ending_decisions = { runtime_decision } } }
}
`,
			want: "struggle_missing_future_phase",
		},
		{
			name: "struggle phase reference",
			path: "common/struggle/struggles/runtime_contract_struggle_reference.txt",
			text: `runtime_struggle = {
	start_phase = missing_phase
	phase_list = {
		phase_one = { future_phases = { missing_phase = { } } ending_decisions = { runtime_decision } }
	}
}
`,
			want: "struggle_phase_reference",
		},
		{
			name: "struggle ending fields",
			path: "common/struggle/struggles/runtime_contract_struggle_ending.txt",
			text: `runtime_struggle = {
	start_phase = phase_one
	phase_list = {
		phase_one = { future_phases = { ending_phase = { } } ending_decisions = { runtime_decision } }
		ending_phase = { ending_decisions = { runtime_decision } }
	}
}
`,
			want: "struggle_ending_phase_fields",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analysis, err := AnalyzeVirtualFile(tc.path, "patch", 1, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if !runtimeContractHasCode(analysis.Diagnostics, tc.want) {
				t.Fatalf("expected %s, got:\n%s", tc.want, runtimeContractDetails(analysis.Diagnostics))
			}
		})
	}
}
