package indexer

import (
	"reflect"
	"testing"
)

func TestResolveTigerOnActionContractPreservesAliasesAndStaticBoundaries(t *testing.T) {
	if tigerOnActionTableVersion != "1.15.0" {
		t.Fatalf("unexpected generated Tiger version %q", tigerOnActionTableVersion)
	}
	if got, want := len(tigerOnActions), 199; got != want {
		t.Fatalf("on_action membership count=%d want=%d", got, want)
	}
	if got, want := len(tigerOnActionDirect), 135; got != want {
		t.Fatalf("direct on_action count=%d want=%d", got, want)
	}
	if got, want := len(tigerOnActionAliases), 64; got != want {
		t.Fatalf("alias on_action count=%d want=%d", got, want)
	}
	if !IsOnAction(" ON_ALLIANCE_REMOVED ") {
		t.Fatal("case-normalized Tiger on_action membership was lost")
	}
	if IsOnAction("list") {
		t.Fatal("nested list declaration was published as an on_action")
	}

	contract, found := ResolveTigerOnActionContract("ON_ALLIANCE_REMOVED")
	if !found {
		t.Fatal("alias contract was not resolved")
	}
	if contract.Key != "on_alliance_removed" || contract.Definition != "on_alliance_added" || !reflect.DeepEqual(contract.AliasPath, []string{"on_alliance_removed", "on_alliance_added"}) {
		t.Fatalf("alias provenance was not retained: %+v", contract)
	}
	if contract.Root.ValueKind != TigerOnActionValueKindNone || contract.Root.StaticType != "none" || len(contract.Named) != 2 || contract.Named[0].Name != "first" || contract.Named[0].ValueKind != TigerOnActionValueKindScope || contract.Named[0].StaticType != "character" {
		t.Fatalf("alias did not project definition bindings: %+v", contract)
	}
	if contract.RuleSource != "tiger_static" || contract.Confidence != "medium" || contract.DiagnosticEffect != "none" {
		t.Fatalf("static contract accidentally acquired authority: %+v", contract)
	}
	if _, found := ResolveTigerOnActionContract("not_a_real_on_action"); found {
		t.Fatal("unknown on_action unexpectedly resolved to Tiger evidence")
	}
}

func TestResolveTigerOnActionContractSeparatesListAndPrimitiveBindings(t *testing.T) {
	alliance, found := ResolveTigerOnActionContract("on_alliance_broken")
	if !found || len(alliance.Lists) != 2 || alliance.Lists[0].Name != "first" || alliance.Lists[0].StaticType != "character" || alliance.Lists[1].Name != "second" {
		t.Fatalf("alliance list bindings were not preserved separately: %+v", alliance)
	}
	if tigerBindingNamed(alliance.Named, "list") != nil {
		t.Fatalf("list binding leaked into named bindings: %+v", alliance)
	}

	siege, found := ResolveTigerOnActionContract("on_siege_completion")
	if !found || len(siege.Lists) != 1 || siege.Lists[0].Name != "occupied_baronies" || siege.Lists[0].StaticType != "landed_title" || siege.Lists[0].ValueKind != TigerOnActionValueKindScope {
		t.Fatalf("siege list binding was not projected: %+v", siege)
	}

	rank, found := ResolveTigerOnActionContract("on_accolade_rank_change")
	if !found || tigerBindingNamed(rank.Named, "positive") == nil || tigerBindingNamed(rank.Named, "positive").ValueKind != TigerOnActionValueKindBool {
		t.Fatalf("bool binding was not projected: %+v", rank)
	}
	glory, found := ResolveTigerOnActionContract("on_accolade_glory_change")
	if !found || tigerBindingNamed(glory.Named, "glory") == nil || tigerBindingNamed(glory.Named, "glory").ValueKind != TigerOnActionValueKindValue {
		t.Fatalf("value binding was not projected: %+v", glory)
	}
	tradition, found := ResolveTigerOnActionContract("on_tradition_removed")
	if !found || tigerBindingNamed(tradition.Named, "tradition") == nil || tigerBindingNamed(tradition.Named, "tradition").ValueKind != TigerOnActionValueKindFlag || !tigerBindingNamed(tradition.Named, "tradition").Review {
		t.Fatalf("review-marked flag binding was not projected: %+v", tradition)
	}
}

func TestResolveTigerOnActionContractReturnsDefensiveSlices(t *testing.T) {
	first, found := ResolveTigerOnActionContract("on_army_enter_province")
	if !found || first.Definition != "on_army_monthly" || first.Root.StaticType != "character" || len(first.Named) != 1 || first.Named[0].Name != "army" {
		t.Fatalf("army alias contract was not resolved: %+v", first)
	}
	first.Named[0].Name = "mutated"
	first.AliasPath[0] = "mutated"
	second, found := ResolveTigerOnActionContract("on_army_enter_province")
	if !found || second.Named[0].Name != "army" || second.AliasPath[0] != "on_army_enter_province" {
		t.Fatalf("returned Tiger contract mutated generated evidence: %+v", second)
	}
}

func tigerBindingNamed(bindings []TigerOnActionBinding, name string) *TigerOnActionBinding {
	for index := range bindings {
		if bindings[index].Name == name {
			return &bindings[index]
		}
	}
	return nil
}
