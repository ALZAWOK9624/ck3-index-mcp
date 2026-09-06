package indexer

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestStandaloneCheckCoverageAndDiagnostics(t *testing.T) {
	c, err := NewStandaloneChecker(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, text, code string }{
		{"events/test.txt", "test.1={ trigger={ AND={ add_gold=5 } } }", "effect_in_trigger"},
		{"common/scripted_triggers/test.txt", "test={ root={ add_gold=1 } }", "effect_in_trigger"},
		{"common/scripted_effects/test.txt", "test={ is_alive=yes }", "trigger_in_effect"},
		{"events/test.txt", "test.1={ is_triggered_only=yes }", "unsupported_event_field"},
		{"events/test.txt", "test.1={", "parse_error"},
		{"gui/test.gui", `types X { type a = widget { size={10 20} } }`, ""},
		{"common/scripted_effects/test.txt", `test={ my_helper={ AGE=1 } }`, ""},
		{"localization/english/test_l_english.yml", "l_english:\n test:0 \"unterminated", "localization_entry_syntax"},
		{"localization/english/test_l_english.yml", "l_english:\n b_mansa'l-kharaz:0 \"Mansa'l-Kharaz\"", ""},
		{"localization/english/test_l_english.yml", "plain text", "localization_missing_header"},
		{"common/scripted_effects/test.txt", "test={ switch={trigger=highest_skill diplomacy={add_gold=1}} random_trait_in_category={category=education add_gold=1} }", ""},
	} {
		t.Run(tc.path+tc.code, func(t *testing.T) {
			r, err := c.Check(context.Background(), CheckRequest{Files: []CheckFile{{tc.path, tc.text}}})
			if err != nil {
				t.Fatal(err)
			}
			if r.DatabaseUsed || r.Files[0].Coverage.References != "not_checked" {
				t.Fatalf("misleading coverage: %+v", r)
			}
			found := false
			for _, d := range r.Files[0].Diagnostics {
				if d.Code == tc.code {
					found = true
				}
			}
			if tc.code != "" && !found {
				t.Fatalf("missing %s: %+v", tc.code, r)
			}
			if tc.code == "" && r.Errors != 0 {
				t.Fatalf("valid source rejected: %+v", r)
			}
		})
	}
}

func TestStandaloneLimitsTruncationAndCancellation(t *testing.T) {
	c, _ := NewStandaloneChecker(context.Background(), "")
	for _, p := range []string{"../events/x.txt", "C:/events/x.txt", "gfx/file.dds", "common/traits/example.info"} {
		if _, err := c.Check(context.Background(), CheckRequest{Files: []CheckFile{{p, "x=yes"}}}); err == nil {
			t.Fatalf("accepted unsupported path %s", p)
		}
	}
	r, err := c.Check(context.Background(), CheckRequest{Limit: 1, Files: []CheckFile{{"events/a.txt", "} }"}, {"events/b.txt", "}"}}})
	if err != nil || r.Passed || r.Errors != 3 || !r.Truncated || len(r.Files[0].Diagnostics) != 1 || len(r.Files[1].Diagnostics) != 0 {
		t.Fatalf("truncation concealed errors: %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Check(ctx, CheckRequest{Files: []CheckFile{{"events/a.txt", "a={}"}}}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	_, err = c.Check(context.Background(), CheckRequest{Files: []CheckFile{{"events/a.txt", strings.Repeat(" ", CheckMaxFileBytes+1)}}})
	if err == nil {
		t.Fatal("accepted oversized file")
	}
}

func TestStandaloneRuleSnapshotsAreIsolated(t *testing.T) {
	var wg sync.WaitGroup
	for _, effect := range []bool{true, false} {
		wg.Add(1)
		go func(effect bool) {
			defer wg.Done()
			kind := "trigger"
			if effect {
				kind = "effect"
			}
			c := &StandaloneChecker{rules: &EngineRuleSet{rules: map[string]map[string][]string{"private_command": {kind: {"character"}}}}}
			for i := 0; i < 20; i++ {
				r, err := c.Check(context.Background(), CheckRequest{Files: []CheckFile{{"common/scripted_triggers/x.txt", "x={private_command=yes}"}}})
				if err != nil || (r.Errors > 0) != effect {
					t.Errorf("rule snapshot contamination: %+v %v", r, err)
				}
			}
		}(effect)
	}
	wg.Wait()
}

func TestStandaloneIgnoresLegacyCommentEncoding(t *testing.T) {
	c, _ := NewStandaloneChecker(context.Background(), "")
	for _, tc := range []struct {
		src    string
		passed bool
	}{{"a=yes # legacy \xe9", true}, {"a=\xe9", false}} {
		r, err := c.Check(context.Background(), CheckRequest{Files: []CheckFile{{"history/provinces/a.txt", tc.src}}})
		if err != nil || r.Passed != tc.passed {
			t.Fatalf("encoding: %+v %v", r, err)
		}
	}
}
