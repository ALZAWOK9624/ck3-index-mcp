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
	return out
}

// M19: trigger_if / trigger_else_if chains should end with trigger_else.
// Independent trigger_if blocks are valid. A chain exists only when a
// trigger_else_if immediately follows a trigger_if (or another trigger_else_if).
func checkTriggerElseTerminator(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	var walk func(ns []*script.Node)
	walk = func(ns []*script.Node) {
		for index := 0; index < len(ns); index++ {
			first := ns[index]
			if first.Key != "trigger_if" || first.Kind != "block" || index+1 >= len(ns) ||
				ns[index+1].Key != "trigger_else_if" || ns[index+1].Kind != "block" {
				continue
			}
			end := index + 1
			for end+1 < len(ns) && ns[end+1].Key == "trigger_else_if" && ns[end+1].Kind == "block" {
				end++
			}
			if end+1 >= len(ns) || ns[end+1].Key != "trigger_else" || ns[end+1].Kind != "block" {
				out = append(out, ctxDiag{
					severity: "warning",
					code:     "missing_trigger_else",
					msg:      fmt.Sprintf("trigger_if/trigger_else_if chain of %d blocks should end with trigger_else", end-index+1),
					line:     first.Line, col: first.Col,
				})
			}
			index = end
		}
		for _, n := range ns {
			walk(n.Children)
		}
	}
	walk(nodes)
	return out
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

// M17: Visible numeric event definitions should have at least one option
// block. Event files also contain helper scripted triggers/effects, and hidden
// events legitimately use immediate effects without presenting a choice.
func checkEventHasOption(nodes []*script.Node, relPath string) []ctxDiag {
	if !isEventPath(relPath) {
		return nil
	}
	var out []ctxDiag
	for _, n := range nodes {
		if !isNumericEventID(n) || hasDirectYesChild(n, "hidden") {
			continue
		}
		if !hasDirectChild(n, "option") {
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

func isNumericEventID(n *script.Node) bool {
	if n == nil || n.Kind != "block" || n.Operator != "=" {
		return false
	}
	parts := strings.Split(n.Key, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, r := range parts[0] {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasDirectChild(n *script.Node, key string) bool {
	for _, child := range n.Children {
		if child.Key == key {
			return true
		}
	}
	return false
}

func hasDirectYesChild(n *script.Node, key string) bool {
	for _, child := range n.Children {
		if child.Key == key && strings.EqualFold(strings.TrimSpace(child.Value), "yes") {
			return true
		}
	}
	return false
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

// --- Cross-file data collection ---

// savedScopeSetters are retained for index data collection. They are not a
// proof that a scope reference is invalid when no local setter is found:
// CK3 scopes can be provided by event context, callers, and switch branches.
var savedScopeSetters = map[string]bool{
	"save_scope_as":           true,
	"save_temporary_scope_as": true,
}

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
