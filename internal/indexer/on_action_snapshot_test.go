package indexer

import "testing"

func TestResolveOnActionSnapshotContractUsesCK3119Evidence(t *testing.T) {
	if engineOnActionSnapshotVersion != "1.19.0-engine-log" {
		t.Fatalf("unexpected generated on_action version %q", engineOnActionSnapshotVersion)
	}
	if got, want := len(engineOnActions), 879; got != want {
		t.Fatalf("on_action membership count=%d want=%d", got, want)
	}
	if got, want := len(engineOnActionDirect), 879; got != want {
		t.Fatalf("direct on_action count=%d want=%d", got, want)
	}
	if got := len(engineOnActionAliases); got != 0 {
		t.Fatalf("engine-log ids must not inherit aliases, got %d", got)
	}
	if !IsOnAction(" ON_ALLIANCE_REMOVED ") {
		t.Fatal("case-normalized 1.19 on_action membership was lost")
	}
	if IsOnAction("list") {
		t.Fatal("nested list declaration was published as an on_action")
	}

	contract, found := ResolveOnActionSnapshotContract("ON_ALLIANCE_REMOVED")
	if !found {
		t.Fatal("engine-log on_action contract was not resolved")
	}
	if contract.Key != "on_alliance_removed" || contract.Definition != "on_alliance_removed" || len(contract.AliasPath) != 0 {
		t.Fatalf("engine-log hook must retain its own identity without a legacy alias: %+v", contract)
	}
	if contract.Root.ValueKind != OnActionSnapshotValueKindNone || contract.Root.StaticType != "none" {
		t.Fatalf("engine-log root scope was not preserved: %+v", contract)
	}
	if contract.RuleSource != "engine_1_19_snapshot" || contract.Confidence != "high" || contract.DiagnosticEffect != "none" {
		t.Fatalf("generated engine contract has unexpected authority metadata: %+v", contract)
	}
	if _, found := ResolveOnActionSnapshotContract("not_a_real_on_action"); found {
		t.Fatal("unknown on_action unexpectedly resolved to generated evidence")
	}
}

func TestResolveOnActionSnapshotContractUsesOnlyProvenVanillaBindings(t *testing.T) {
	rank, found := ResolveOnActionSnapshotContract("on_accolade_rank_change")
	if !found || snapshotBindingNamed(rank.Named, "positive") == nil || snapshotBindingNamed(rank.Named, "positive").ValueKind != OnActionSnapshotValueKindBool {
		t.Fatalf("vanilla-comment bool binding was not projected: %+v", rank)
	}
	glory, found := ResolveOnActionSnapshotContract("on_accolade_glory_change")
	if !found || snapshotBindingNamed(glory.Named, "glory") == nil || snapshotBindingNamed(glory.Named, "glory").ValueKind != OnActionSnapshotValueKindValue {
		t.Fatalf("vanilla-comment value binding was not projected: %+v", glory)
	}
	alliance, found := ResolveOnActionSnapshotContract("on_alliance_broken")
	if !found || len(alliance.Lists) != 0 || len(alliance.Named) != 0 {
		t.Fatalf("untyped legacy list bindings must not survive the 1.19 migration: %+v", alliance)
	}
}

func TestResolveOnActionSnapshotContractReturnsDefensiveSlices(t *testing.T) {
	first, found := ResolveOnActionSnapshotContract("on_accolade_rank_change")
	if !found || len(first.Named) != 1 || first.Named[0].Name != "positive" {
		t.Fatalf("expected one documented binding: %+v", first)
	}
	first.Named[0].Name = "mutated"
	second, found := ResolveOnActionSnapshotContract("on_accolade_rank_change")
	if !found || second.Named[0].Name != "positive" {
		t.Fatalf("returned generated contract mutated source evidence: %+v", second)
	}
}

func snapshotBindingNamed(bindings []OnActionSnapshotBinding, name string) *OnActionSnapshotBinding {
	for index := range bindings {
		if bindings[index].Name == name {
			return &bindings[index]
		}
	}
	return nil
}
