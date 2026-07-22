package indexer

import (
	"strings"
	"testing"

	"ck3-index/internal/script"
)

func TestScopeV2DoesNotAliasHighScopeTypes(t *testing.T) {
	if ScopeWar == ScopeVassalContract || ScopeWar == ScopeVassalObligationLevel || ScopeVassalContract == ScopeVassalObligationLevel {
		t.Fatalf("high scope types must have unique bits: war=%+v contract=%+v obligation=%+v", ScopeWar, ScopeVassalContract, ScopeVassalObligationLevel)
	}
	if ScopeWar.High == 0 || ScopeVassalObligationLevel.High == 0 {
		t.Fatalf("expected high scope word to be used: war=%+v obligation=%+v", ScopeWar, ScopeVassalObligationLevel)
	}
	if got := scopeMaskDesc(scopeUnion(ScopeCharacter, ScopeTitle)); got != "character|title" && got != "title|character" {
		t.Fatalf("expected readable composite scope names, got %q", got)
	}
}

func TestScopeV2TracksNestedContextAndTransitions(t *testing.T) {
	bad := script.Parse(`test_decision = {
		is_shown = {
			AND = { has_cultural_parameter = unlock_bad }
		}
	}`)
	diags := checkScopeTracker(bad.Nodes, "common/decisions/test.txt")
	if len(diags) != 1 || !strings.Contains(diags[0].msg, "character") || !strings.Contains(diags[0].msg, "culture") {
		t.Fatalf("expected one traced character-to-culture mismatch, got %+v", diags)
	}

	good := script.Parse(`k_test = {
		can_create = {
			culture = { has_cultural_parameter = unlock_good }
		}
	}`)
	if got := checkScopeTracker(good.Nodes, "common/landed_titles/test.txt"); len(got) != 0 {
		t.Fatalf("expected can_create character root plus culture transition to validate, got %+v", got)
	}
}

func TestScopeV2TracksDottedChainsAndIgnoresUnknownWidgetRoots(t *testing.T) {
	file := script.Parse(`test_decision = {
		is_shown = {
			capital_county.culture = { has_cultural_parameter = unlock_good }
			var:dynamic_title ?= { is_titular = no }
			any_engine_iterator_not_in_tables = { has_doctrine = doctrine_test }
			custom_scripted_trigger = { TERRAIN = woodlands }
		}
		widget = {
			barony_valid = {
				trigger_if = { is_capital_barony = yes }
			}
		}
	}`)
	if got := checkScopeTracker(file.Nodes, "common/decisions/test.txt"); len(got) != 0 {
		t.Fatalf("expected dotted scope chain and untyped widget root to avoid false positives, got %+v", got)
	}
}

func TestScopeV2TreatsTypedCourtPositionTargetAsCharacter(t *testing.T) {
	file := script.Parse(`test_decision = {
		effect = {
			court_position:court_scholar_court_position ?= {
				remove_character_flag = civil_research
			}
		}
	}`)
	if got := checkScopeTracker(file.Nodes, "common/decisions/test.txt"); len(got) != 0 {
		t.Fatalf("expected court_position target to resolve to its character output scope, got %+v", got)
	}
}

func TestScopeV2KeepsFlagSwitchBranchesInParentScope(t *testing.T) {
	file := script.Parse(`test_decision = {
		effect = {
			switch = {
				flag:council_seat = {
					ordered_courtier = { save_scope_as = selected_courtier }
				}
			}
		}
	}`)
	if got := checkScopeTracker(file.Nodes, "common/decisions/test.txt"); len(got) != 0 {
		t.Fatalf("expected a flag switch label to preserve the parent character scope, got %+v", got)
	}
}

func TestScopeV2TracksHistoryHolderAndRegimentIterator(t *testing.T) {
	file := script.Parse(`k_test = {
		1.1.1 = {
			effect = {
				holder = {
					random_maa_regiment = {
						limit = { is_maa_type = varangian_guards }
						change_maa_regiment_size = { size = 5 }
					}
				}
			}
		}
	}`)
	if got := checkScopeTracker(file.Nodes, "history/titles/test.txt"); len(got) != 0 {
		t.Fatalf("expected title-holder-regiment scope trace to validate, got %+v", got)
	}
}

func TestScopeV2UsesExplicitEventScope(t *testing.T) {
	good := script.Parse(`test.1 = {
		scope = culture
		trigger = { has_cultural_parameter = unlock_good }
	}`)
	if got := checkScopeTracker(good.Nodes, "events/test.txt"); len(got) != 0 {
		t.Fatalf("expected culture event scope to validate, got %+v", got)
	}

	bad := script.Parse(`test.2 = {
		trigger = { has_cultural_parameter = unlock_bad }
	}`)
	if got := checkScopeTracker(bad.Nodes, "events/test.txt"); len(got) != 1 {
		t.Fatalf("expected default character event scope mismatch, got %+v", got)
	}

	uiContainer := script.Parse(`test.3 = {
		title = {
			triggered_desc = {
				trigger = { has_trait = diligent }
			}
		}
	}`)
	if got := checkScopeTracker(uiContainer.Nodes, "events/test.txt"); len(got) != 0 {
		t.Fatalf("expected event title UI container to preserve the event root scope, got %+v", got)
	}

	scriptedEffect := script.Parse(`scripted_effect helper_effect = {
		hidden_effect = { has_current_phase = phase_test }
	}`)
	if got := checkScopeTracker(scriptedEffect.Nodes, "events/test.txt"); len(got) != 0 {
		t.Fatalf("expected inline scripted effect definitions to stay outside event-root validation, got %+v", got)
	}
}
