package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// checkScriptLint runs structural/best-practice checks on a parsed script
// file and returns applicable diagnostics. Called from parseOneFile alongside
// checkScriptContext.
//
// Rules:
//
//	M19 鈥?trigger_if chain must end with trigger_else
//	M9  鈥?on_action files: direct effect= blocks wipe vanilla effects
//	M21 鈥?GUI: known crash patterns (hbox/vbox with percent size, etc.)
//	M22 鈥?GUI: parentanchor inside hbox/vbox (use expand={} instead)
//	M6  鈥?nested iterator explosion detection
//	M20 鈥?scripted effect self-recursion
//	M17 鈥?event has at least one option
func checkScriptLint(nodes []*script.Node, relPath string, sourceName string) []ctxDiag {
	var out []ctxDiag
	// Structural checks: run on all sources.
	out = append(out, checkTriggerElseTerminator(nodes, relPath)...)
	out = append(out, checkOnActionOverride(nodes, relPath)...)
	out = append(out, checkGUISafety(nodes, relPath)...)
	out = append(out, checkIteratorDepth(nodes, relPath)...)
	out = append(out, checkEventHasOption(nodes, relPath)...)
	out = append(out, checkSavedScopes(nodes, relPath)...)
	return out
}

// M19: trigger_if / trigger_else_if chains must end with trigger_else.
// Single trigger_if without else is valid; only multi-block chains require it.
func checkTriggerElseTerminator(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	var walk func(ns []*script.Node)
	walk = func(ns []*script.Node) {
		// Count trigger_if / trigger_else_if / trigger_else in this block.
		ifCount := 0
		hasElse := false
		for _, sib := range ns {
			if sib.Key == "trigger_if" || sib.Key == "trigger_else_if" {
				ifCount++
			}
			if sib.Key == "trigger_else" {
				hasElse = true
			}
		}
		if ifCount >= 2 && !hasElse {
			for _, sib := range ns {
				if sib.Key == "trigger_if" || sib.Key == "trigger_else_if" {
					out = append(out, ctxDiag{
						severity: "warning",
						code:     "missing_trigger_else",
						msg:      fmt.Sprintf("%q chain of %d blocks should end with trigger_else (required by CK3)", sib.Key, ifCount),
						line:     sib.Line, col: sib.Col,
					})
				}
			}
		}
		for _, n := range ns {
			walk(n.Children)
		}
	}
	walk(nodes)

	seen := map[int]bool{}
	filtered := out[:0]
	for _, d := range out {
		if !seen[d.line] {
			seen[d.line] = true
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// M9: On_action files. Only flag effect/trigger blocks directly inside
// KNOWN vanilla on_actions (which would shadow the originals). Custom
// on_actions with effect blocks are the correct CK3 pattern.
var vanillaOnActions = map[string]bool{
	"on_birth": true, "on_death": true, "on_marriage": true,
	"on_concubinage": true, "on_divorce": true, "on_betrothal": true,
	"on_join_court": true, "on_leave_court": true,
	"on_title_gain": true, "on_title_lost": true,
	"on_war_started": true, "on_war_ended": true,
	"on_siege_won": true, "on_siege_lost": true,
	"on_battle_won": true, "on_battle_lost": true,
	"on_combat_end": true, "on_imprisoned": true,
	"on_released_from_prison": true, "on_actions": true,
	"on_holy_order_created": true, "on_holy_order_destroyed": true,
	"yearly_playable_pulse": true, "quarterly_playable_pulse": true,
	"monthly_playable_pulse": true, "five_year_playable_pulse": true,
	"on_startup": true, "on_host_change": true,
	"on_game_start": true, "on_game_start_after_lobby": true,
}

func checkOnActionOverride(nodes []*script.Node, relPath string) []ctxDiag {
	if !strings.Contains(relPath, "on_action") {
		return nil
	}
	var out []ctxDiag
	for _, n := range nodes {
		if n.Kind != "block" || n.Key == "" {
			continue
		}
		for _, c := range n.Children {
			if (c.Key == "effect" || c.Key == "trigger") && vanillaOnActions[n.Key] {
				out = append(out, ctxDiag{
					severity: "warning",
					code:     "on_action_direct_override",
					msg: fmt.Sprintf("direct %q block in vanilla on_action %q overwrites originals; use a custom on_action instead",
						c.Key, n.Key),
					line: c.Line, col: c.Col,
				})
			}
		}
	}
	return out
}

// M21+M22: GUI known crash patterns and layout misuse.
func checkGUISafety(nodes []*script.Node, relPath string) []ctxDiag {
	if !strings.HasSuffix(relPath, ".gui") {
		return nil
	}
	var out []ctxDiag
	walk := func(parentKey string) func(ns []*script.Node) {
		var inner func(ns []*script.Node)
		inner = func(ns []*script.Node) {
			for _, n := range ns {
				k := guiNodeKind(n)
				switch k {
				case "hbox", "vbox":
					// CK3 1.19 vanilla deliberately uses percent-sized hbox/vbox
					// controls for both window and row layouts. Percentage size alone
					// is therefore not evidence of a crash.
					// M22: hbox/vbox should not use parentanchor.
					for _, c := range n.Children {
						if c.Key == "parentanchor" {
							out = append(out, ctxDiag{
								severity: "warning",
								code:     "gui_layout_misuse",
								msg:      fmt.Sprintf("parentanchor inside %q should be replaced with expand={}", k),
								line:     c.Line, col: c.Col,
							})
						}
					}
				case "flowcontainer":
					// M21: hbox/vbox inside flowcontainer may crash.
					for _, c := range n.Children {
						childKind := guiNodeKind(c)
						if c.Kind == "block" && (childKind == "hbox" || childKind == "vbox") {
							out = append(out, ctxDiag{
								severity: "error",
								code:     "gui_crash_risk",
								msg:      fmt.Sprintf("%s nested inside flowcontainer may crash the game", childKind),
								line:     c.Line, col: c.Col,
							})
						}
					}
				case "resizeparent":
					// M21: multiple resizeparent on siblings.
					sibCount := 0
					for _, sib := range ns {
						if sib.Key == "resizeparent" {
							sibCount++
						}
					}
					if sibCount > 1 {
						out = append(out, ctxDiag{
							severity: "error",
							code:     "gui_crash_risk",
							msg:      "multiple resizeparent declarations may cause crashes",
							line:     n.Line, col: n.Col,
						})
					}
				}
				inner(n.Children)
			}
		}
		return inner
	}
	walk("")(nodes)

	// Deduplicate by line.
	seen := map[int]bool{}
	filtered := out[:0]
	for _, d := range out {
		if !seen[d.line] {
			seen[d.line] = true
			filtered = append(filtered, d)
		}
	}
	return filtered
}

func guiNodeKind(n *script.Node) string {
	if n != nil && n.Value != "" && (n.Operator == "type" || (n.Operator == "=" && n.Kind == "block")) {
		return n.Value
	}
	if n == nil {
		return ""
	}
	return n.Key
}

// M6: Nested iterator explosion (e.g., every_living_character inside every_province).
// Flags any iterator block that contains another iterator.
func checkIteratorDepth(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	depth := 0
	var walk func(ns []*script.Node)
	walk = func(ns []*script.Node) {
		for _, n := range ns {
			if isIteratorKey(n.Key) {
				depth++
				if depth >= 2 {
					out = append(out, ctxDiag{
						severity: "warning",
						code:     "nested_iterator",
						msg:      fmt.Sprintf("nested iterator %q (depth %d) may cause severe lag", n.Key, depth),
						line:     n.Line, col: n.Col,
					})
				}
				walk(n.Children)
				depth--
			} else {
				walk(n.Children)
			}
		}
	}
	walk(nodes)
	return out
}

// M20: Scripted effect should not call itself recursively.
func checkScriptEffectRecursion(nodes []*script.Node, relPath string, ownName string) []ctxDiag {
	if ownName == "" || !strings.Contains(relPath, "scripted_effects") {
		return nil
	}
	var out []ctxDiag
	walkNodes(nodes, func(n *script.Node) {
		if n.Kind == "bare" && n.Key == ownName {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "scripted_effect_recursion",
				msg:      fmt.Sprintf("scripted effect %q appears to call itself (recursion not supported)", ownName),
				line:     n.Line, col: n.Col,
			})
		}
	})
	return out
}

// M17: Events should have at least one option block.
func checkEventHasOption(nodes []*script.Node, relPath string) []ctxDiag {
	if !strings.Contains(relPath, "events/") {
		return nil
	}
	var out []ctxDiag
	for _, n := range nodes {
		if n.Kind != "block" || n.Key == "" {
			continue
		}
		hasOption := false
		for _, c := range n.Children {
			if c.Key == "option" {
				hasOption = true
				break
			}
		}
		if !hasOption && (strings.Contains(n.Key, "event") || strings.Contains(n.Key, ".")) {
			out = append(out, ctxDiag{
				severity: "warning",
				code:     "event_no_option",
				msg:      fmt.Sprintf("event %q has no option block; events normally need at least one option", n.Key),
				line:     n.Line, col: n.Col,
			})
		}
	}
	return out
}

// --- helpers ---

var iteratorKeys = map[string]bool{
	"every_child": true, "every_child_in_history": true,
	"every_courtier": true, "every_vassal": true,
	"every_vassal_or_below": true, "every_liege_or_above": true,
	"every_spouse": true, "every_sibling": true,
	"every_living_character": true, "every_character": true,
	"every_ruler": true, "every_councillor": true,
	"every_knight": true, "every_guest": true,
	"every_house_member": true, "every_dynasty_member": true,
	"every_close_family": true, "every_extended_family": true,
	"every_relation": true, "every_concubine": true,
	"every_lover": true, "every_friend": true,
	"every_rival": true, "every_ward": true,
	"every_prisoner": true, "every_potential_agent": true,
	"every_province": true, "every_neighbor_province": true,
	"every_realm_province": true, "every_domain_province": true,
	"every_held_title": true, "every_de_jure_title": true,
	"every_title": true, "every_realm_title": true,
	"every_county": true, "every_realm_county": true,
	"every_barony": true, "every_army": true,
	"every_war": true, "every_holy_order": true,
	"every_faith": true, "every_religion": true,
	"every_religion_global": true, "every_culture": true,
	"every_culture_global": true, "every_secret": true,
	"every_scheme": true, "every_legend": true,
	"every_activity": true, "every_dynasty": true,
	"every_house": true, "every_artifact": true,
	"every_accolade": true, "every_army_regiment": true,
	"every_building": true, "every_council_contract": true,
	"every_faction": true, "every_men_at_arms": true,
	"every_merc_company": true, "every_navy": true,
	"every_onion": true, "every_allied_character": true,
	"every_attacker": true, "every_defender": true,
	"every_in_list": true, "ordered_child": true,
	"ordered_in_list": true, "random_child": true,
	"random_in_list": true, "any_child": true,
	"any_in_list": true,
}

func isIteratorKey(key string) bool {
	return iteratorKeys[strings.ToLower(key)]
}

// --- M3: saved scope consistency ---

// savedScopeSetters are effect keys that create a named saved scope.
var savedScopeSetters = map[string]bool{
	"save_scope_as":           true,
	"save_temporary_scope_as": true,
}

// checkSavedScopes warns when scope:name references a name that was never
// saved via save_scope_as or save_temporary_scope_as in the same file.
// Only checks inside effect blocks to avoid false positives from scope
// target declarations and pre-saved scopes.
func checkSavedScopes(nodes []*script.Node, relPath string) []ctxDiag {
	builtin := map[string]bool{
		"actor": true, "recipient": true, "root": true, "prev": true, "this": true,
		// CK3 engine-provided scopes (set by game context, not save_scope_as):
		"activity": true, "host": true, "target": true, "owner": true,
		"scheme": true, "story": true, "war": true, "faction": true,
		"title": true, "character": true, "province": true, "county": true,
		"faith": true, "culture": true, "dynasty": true, "house": true,
		"army": true, "attacker": true, "defender": true,
		"location": true, "holder": true, "controller": true, "liege": true,
		"father": true, "mother": true, "spouse": true, "lover": true,
		"killer": true, "employer": true, "guardian": true, "ward": true,
		"concubine": true, "consort": true, "friend": true, "rival": true,
		"child": true, "sibling": true, "parent": true,
		"heir": true, "primary_heir": true, "primary_spouse": true,
		"court_owner": true, "courtier": true, "guest": true, "prisoner": true,
		"commander": true, "knight": true, "councillor": true,
		"religious_head": true, "physician": true,
		"artifact": true, "legend": true, "secret": true,
		"task_contract": true, "inspiration": true, "accolade": true,
		"travel": true, "travel_plan": true, "domicile": true,
		"struggle": true, "situation": true, "confederation": true,
		"diplomacy": true, "martial": true, "stewardship": true, "intrigue": true, "learning": true,
		"newly_created_artifact": true, "newly_created_character": true,
		"stop_host_scope": true, "visiting_liege": true,
		"court": true, "pool": true, "capital": true, "realm": true,
		"beneficiary": true, "designated_heir": true,
		"event_target": true, "saved_scope": true,
		"inspiration_owner": true, "headless_heir": true, "visitor": true,
		"new_memory": true, "versus_contestant": true, "new_title": true,
		"faction_leader": true, "raider": true, "previous_holder": true,
		"target_character": true, "councillor_liege": true, "secret_owner": true,
		"colony": true, "first": true, "second": true, "third": true,
		"courtier_spy": true, "sc_defender": true, "sc_attacker": true,
		"homage_vassal": true, "cultural_festival_scope": true,
	}
	saved := map[string]bool{}
	walkNodes(nodes, func(n *script.Node) {
		if savedScopeSetters[n.Key] && n.Value != "" {
			saved[n.Value] = true
		}
		if n.Key == "save_scope_as" && n.Kind == "block" {
			for _, c := range n.Children {
				if c.Key == "name" && c.Value != "" {
					saved[c.Value] = true
				}
			}
		}
	})
	if len(saved) == 0 {
		return nil
	}

	var out []ctxDiag
	var walkEffect func(ns []*script.Node, inEffect bool)
	walkEffect = func(ns []*script.Node, inEffect bool) {
		for _, n := range ns {
			// Only check inside effect-like blocks.
			eff := inEffect || n.Key == "effect" || n.Key == "immediate" ||
				n.Key == "option" || n.Key == "after" || n.Key == "hidden_effect"
			if eff && n.Kind == "block" && strings.HasPrefix(n.Key, "scope:") {
				name := n.Key[6:]
				// Scope chains like scope:scheme.task_contract are engine paths.
				if len(name) <= 1 || strings.Contains(name, ".") || builtin[name] {
					continue
				}
				if !saved[name] {
					out = append(out, ctxDiag{
						severity: "warning",
						code:     "scope_never_saved",
						msg:      fmt.Sprintf("scope:%s used as block opener but save_scope_as not found in this file", name),
						line:     n.Line, col: n.Col,
					})
				}
			}
			walkEffect(n.Children, eff)
		}
	}
	walkEffect(nodes, false)
	return out
}

// --- M4: variable existence ---

// varSetters are effect keys that define a variable.
var varSetters = map[string]bool{
	"set_variable":                true,
	"set_global_variable":         true,
	"set_local_variable":          true,
	"set_dead_character_variable": true,
}

// varUsers are trigger/effect keys that read or modify an existing variable.
var varUsers = map[string]bool{
	"has_variable":        true,
	"change_variable":     true,
	"remove_variable":     true,
	"clamp_variable":      true,
	"clear_variable":      true,
	"set_variable":        true, // may also set
	"set_global_variable": true,
}

// checkVariables warns when var:name, global_var:name, or local_var:name
// references a variable that was never set in the same file.
func checkVariables(nodes []*script.Node, relPath string) []ctxDiag {
	vars := map[string]bool{}
	// First pass: collect all SET variable names.
	walkNodes(nodes, func(n *script.Node) {
		if !varSetters[n.Key] {
			return
		}
		// Simple form: set_variable = name
		if n.Value != "" {
			vars[n.Value] = true
			return
		}
		// Block form: set_variable = { name = myvar value = 5 }
		if n.Kind == "block" {
			for _, c := range n.Children {
				if c.Key == "name" && c.Value != "" {
					vars[c.Value] = true
				}
			}
		}
	})

	if len(vars) == 0 {
		return nil
	}

	var out []ctxDiag
	walkNodes(nodes, func(n *script.Node) {
		if !varUsers[n.Key] {
			return
		}
		name := n.Value
		if name == "" && n.Kind == "block" {
			for _, c := range n.Children {
				if c.Key == "name" && c.Value != "" {
					name = c.Value
					break
				}
			}
			if name == "" {
				return
			}
		}
		if name != "" && name != "yes" && name != "no" && !strings.Contains(name, " ") && !vars[name] {
			out = append(out, ctxDiag{
				severity: "warning",
				code:     "variable_never_set",
				msg:      fmt.Sprintf("variable %q is used (by %s) but never set via set_variable in this file", name, n.Key),
				line:     n.Line, col: n.Col,
			})
		}
	})

	// Also check var: prefixes in values and keys.
	walkNodes(nodes, func(n *script.Node) {
		raws := []string{}
		if n.Value != "" {
			raws = append(raws, n.Value)
		}
		if n.Kind == "bare" {
			raws = append(raws, n.Key)
		}
		for _, raw := range raws {
			for _, prefix := range []string{"var:", "global_var:", "local_var:", "dead_var:"} {
				if after, ok := strings.CutPrefix(raw, prefix); ok {
					if after != "" && !strings.Contains(after, ".") && !vars[after] {
						// Don't re-flag if already reported above.
						already := false
						for _, d := range out {
							if d.line == n.Line {
								already = true
								break
							}
						}
						if !already {
							out = append(out, ctxDiag{
								severity: "warning",
								code:     "variable_never_set",
								msg:      fmt.Sprintf("variable %q referenced as %s but never set in this file", after, prefix),
								line:     n.Line, col: n.Col,
							})
						}
					}
				}
			}
		}
	})

	return out
}

func walkNodes(nodes []*script.Node, fn func(*script.Node)) {
	for _, n := range nodes {
		fn(n)
		walkNodes(n.Children, fn)
	}
}

// --- Cross-file data collection (for M3+M4 health checks) ---

// collectSavedScopes extracts all unique saved scope names from an AST.
func collectSavedScopes(nodes []*script.Node) []string {
	seen := map[string]bool{}
	walkNodes(nodes, func(n *script.Node) {
		if savedScopeSetters[n.Key] && n.Value != "" {
			seen[n.Value] = true
		}
		if n.Key == "save_scope_as" && n.Kind == "block" {
			for _, c := range n.Children {
				if c.Key == "name" && c.Value != "" {
					seen[c.Value] = true
				}
			}
		}
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// collectVariables extracts all unique variable names set via set_variable etc.
func collectVariables(nodes []*script.Node) []string {
	seen := map[string]bool{}
	walkNodes(nodes, func(n *script.Node) {
		if !varSetters[n.Key] {
			return
		}
		if n.Value != "" {
			seen[n.Value] = true
			return
		}
		if n.Kind == "block" {
			for _, c := range n.Children {
				if c.Key == "name" && c.Value != "" {
					seen[c.Value] = true
				}
			}
		}
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// --- define reference checking (uses current CK3 engineDefines) ---

func checkDefineRefs(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	walkNodes(nodes, func(n *script.Node) {
		candidates := []string{n.Value, n.Key}
		for _, raw := range candidates {
			if !strings.HasPrefix(raw, "@") || len(raw) < 3 || isArithmeticExpression(raw) {
				continue
			}
			if _, ok := engineDefines[raw]; !ok {
				out = append(out, ctxDiag{
					severity: "warning",
					code:     "unknown_define",
					msg:      fmt.Sprintf("@define %q not found in game defines", raw),
					line:     n.Line, col: n.Col,
				})
			}
		}
	})
	return out
}

func checkOnActionRefs(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	walkNodes(nodes, func(n *script.Node) {
		for _, c := range n.Children {
			if c.Kind == "bare" && c.Key != "" && n.Key == "on_actions" {
				// A published on_actions.log is newer and broader than the compact
				// Generated CK3 1.19 snapshot. Use the shared lookup so a live engine hook does
				// not become an avoidable unknown_on_action warning during scans.
				if !IsOnAction(c.Key) {
					out = append(out, ctxDiag{
						severity: "warning",
						code:     "unknown_on_action",
						msg:      fmt.Sprintf("on_action %q not found in known on_actions list", c.Key),
						line:     c.Line, col: c.Col,
					})
				}
			}
		}
	})
	return out
}
