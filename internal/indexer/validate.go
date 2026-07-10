package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

var effectOnly = map[string]bool{
	"add_trait": true, "remove_trait": true, "add_character_modifier": true,
	"remove_character_modifier": true, "add_gold": true, "set_variable": true,
	"trigger_event": true, "death": true, "create_title": true,
	"create_character": true, "kill_character": true, "remove_character": true,
	"create_artifact": true, "destroy_artifact": true, "transfer_artifact": true,
	"create_secret": true, "remove_secret": true, "expose_secret": true,
	"add_realm_law": true, "remove_realm_law": true, "pass_law": true, "remove_law": true,
	"add_building": true, "remove_building": true,
	"add_men_at_arms": true, "recruit_men_at_arms": true, "disband_men_at_arms": true,
	"start_scheme": true, "cancel_scheme": true,
	"start_plot": true, "join_plot": true, "leave_plot": true, "end_plot": true,
	"start_war": true, "end_war": true,
	"set_owner": true, "set_controller": true, "take_title": true, "destroy_title": true,
	"grant_ownership": true, "transfer_title_ownership": true,
	"change_martial": true, "change_diplomacy": true, "change_stewardship": true,
	"change_intrigue": true, "change_learning": true, "change_prowess": true,
	"change_dread": true, "change_stress": true, "change_gold": true,
	"change_prestige": true, "change_piety": true, "change_lifestyle_xp": true,
	"change_diplomatic_range": true, "set_diplomatic_range": true,
	"set_age": true, "set_gender": true, "set_nickname": true, "give_nickname": true,
	"remove_nickname": true, "set_regnal_name": true, "set_name": true,
	"set_faith": true, "convert_to_faith": true, "switch_culture": true,
	"convert_to_culture": true, "set_culture": true,
	"set_focus": true, "set_employer": true, "set_domicile": true,
	"show_portrait": true, "hide_portrait": true, "set_portrait": true,
	"set_description": true, "set_sexuality": true,
	"send_message": true, "send_letter": true, "send_event_message": true,
	"add_piety": true, "add_prestige": true, "change_variable": true,
	"clear_variable": true, "clamp_variable": true,
	"set_relation_flag": true, "clear_relation_flag": true,
	"set_global_flag": true, "clear_global_flag": true,
	"set_character_flag": true, "clear_character_flag": true,
	"add_trait_xp":      true,
	"set_created_title": true, "set_title": true,
}

var triggerOnly = map[string]bool{
	"has_trait": true, "has_character_modifier": true, "is_alive": true,
	"is_ai": true, "exists": true, "is_adult": true, "is_landed": true,
	"has_law": true, "has_realm_law": true, "has_government": true, "has_focus": true,
	"has_artifact": true, "has_secret": true, "has_building": true, "has_domicile": true,
	"has_flag": true, "has_global_var": true, "has_character_flag": true,
	"has_global_flag":  true,
	"has_landed_title": true, "has_liege": true, "has_claim": true, "has_title": true,
	"has_lover": true, "has_marriage": true, "has_war": true, "has_war_with": true,
	"has_truce": true, "has_relation_target": true,
	"has_age_above": true, "has_age_below": true, "has_age": true,
	"has_martial": true, "has_diplomacy": true, "has_stewardship": true,
	"has_intrigue": true, "has_learning": true, "has_prowess": true,
	"has_dread": true, "has_stress": true, "has_gold": true, "has_piety": true,
	"has_prestige": true, "has_attribute": true,
	"is_at_war": true, "is_at_peace": true, "is_female": true, "is_male": true,
	"is_incapable": true, "is_pregnant": true, "is_landed_title_holder": true,
	"is_liege": true, "is_vassal_of": true, "is_imprisoned": true, "is_prisoner": true,
	"is_married": true, "is_betrothed": true, "is_bastard": true, "is_legitimate": true,
	"is_friend": true, "is_rival": true, "is_lover": true,
	"is_parent": true, "is_close_relative": true, "is_close_family": true,
	"is_within_diplo_range": true, "is_in_diplo_range": true,
	"has_council_position": true, "has_councillor": true,
	"has_council_task_cooldown": true, "has_scheme": true, "has_active_scheme": true,
	"has_active_plot": true, "has_income": true, "has_realm_size": true,
	"has_living_standard": true, "has_role": true,
}

func (db *DB) runCompilerChecks(ctx context.Context) error {
	// Context diagnostics (effect_in_trigger, trigger_in_effect) are now
	// produced during the parse pass and stored alongside parser diagnostics,
	// so we only need the cross-file duplicate check here. Keep the existing
	// compiler rows intact: delete only the duplicate_object rows we are about
	// to refresh.
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='compiler' AND code='duplicate_object'`); err != nil {
		return err
	}
	// Health checks produce "health" source diagnostics; delete stale ones.
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='health'`); err != nil {
		return err
	}
	if err := db.checkDuplicates(ctx); err != nil {
		return err
	}
	return db.runHealthChecks(ctx)
}

func (db *DB) checkDuplicates(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,COUNT(*)
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.source_rank=1 AND f.overridden=0
		GROUP BY o.object_type,o.name,o.source_name HAVING COUNT(*) > 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		var count int
		if err := rows.Scan(&typ, &name, &count); err != nil {
			return err
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message) VALUES(?,?,?,?)`,
			"compiler", "warning", "duplicate_object", fmt.Sprintf("%s %q has %d definitions in the same source layer", typ, name, count)); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (db *DB) checkContext(ctx context.Context) error {
	keys := make([]string, 0, len(effectOnly)+len(triggerOnly))
	for key := range effectOnly {
		keys = append(keys, key)
	}
	for key := range triggerOnly {
		keys = append(keys, key)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT n.key,n.file_id,n.start_line,n.start_col,f.path,
		(SELECT p.key FROM nodes p WHERE p.file_id=n.file_id AND p.local_id=n.parent_local_id) AS parent_key
		FROM nodes n JOIN files f ON f.id=n.file_id WHERE n.key IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, path string
		var parent sql.NullString
		var fileID int64
		var line, col int
		if err := rows.Scan(&key, &fileID, &line, &col, &path, &parent); err != nil {
			return err
		}
		ctxKind := ContextFor(parent.String)
		if ctxKind == "trigger" && effectOnly[key] {
			if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col) VALUES(?,?,?,?,?,?,?,?)`,
				"compiler", "error", "effect_in_trigger", fmt.Sprintf("effect %q appears inside a trigger-like block", key), fileID, path, line, col); err != nil {
				return err
			}
		}
		if ctxKind == "effect" && triggerOnly[key] {
			if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col) VALUES(?,?,?,?,?,?,?,?)`,
				"compiler", "warning", "trigger_in_effect", fmt.Sprintf("trigger %q appears inside an effect-like block", key), fileID, path, line, col); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

// ContextFor classifies a parent block key as "trigger" or "effect" context.
// Exported so the parse worker can run the same check directly on the AST.
func ContextFor(parent string) string {
	switch parent {
	case "trigger", "limit", "possible", "allow", "potential", "is_shown", "can_use", "show_if",
		"trigger_if", "trigger_else_if", "trigger_else", "major_trigger", "can_create", "can_recruit",
		"can_build", "can_construct", "can_send", "can_receive", "can_be_picked", "is_valid",
		"is_valid_target", "is_valid_showing_failures_only", "valid_for_maa_trigger":
		return "trigger"
	case "effect", "immediate", "hidden_effect", "after", "on_accept", "on_decline", "option",
		"on_add", "on_remove", "on_success", "on_failure", "on_send", "on_execute", "on_trigger_fail",
		"if", "else_if", "else":
		return "effect"
	}
	return ""
}

// EffectOnly and TriggerOnly are exported so the parse worker can run the
// same effect/trigger context check during the parse pass without touching
// the nodes table.
func IsEffectOnly(key string) bool  { return effectOnly[key] }
func IsTriggerOnly(key string) bool { return triggerOnly[key] }
