package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/script"
)

func TestExtractCharacterHistoryFieldsAndRelations(t *testing.T) {
	parsed := script.Parse(characterHistoryFixture)
	if len(parsed.Errors) > 0 {
		t.Fatalf("fixture parse errors: %+v", parsed.Errors)
	}
	rec := fileRecord{ID: 1, RelPath: "history/characters/test.txt", Path: "history/characters/test.txt", SourceName: "project", SourceRank: 1}
	objects := extractObjects(rec, parsed.Nodes)
	fields := extractObjectFields(rec, parsed.Nodes, objects)

	wantFields := map[string]int{
		"name": 0, "father": 0, "mother": 0, "employer": 0, "dynasty": 0, "dynasty_house": 0,
		"birth": 10000102, "add_spouse": 10200203, "add_trait": 10200203,
		"effect": 10300304, "set_father": 10300304, "death": 10600607,
	}
	for field, date := range wantFields {
		if !hasCharacterField(fields, "child", field, date) {
			t.Errorf("missing character field %s date=%d in %+v", field, date, fields)
		}
	}
	for _, field := range fields {
		if _, ok := parseDateKey(field.Field); ok {
			t.Fatalf("date block leaked as empirical field: %+v", field)
		}
	}

	refs := extractRefs(rec, parsed.Nodes, objects)
	wantRefs := []struct {
		kind, name, relation, phase string
	}{
		{"character", "parent", "father", ""},
		{"character", "mother", "mother", ""},
		{"character", "parent", "employer", ""},
		{"dynasty", "dyn_test", "dynasty", ""},
		{"dynasty_house", "house_test", "dynasty_house", ""},
		{"character", "spouse", "add_spouse", "1020.2.3"},
		{"trait", "test_trait", "add_trait", "1020.2.3"},
		{"character", "adopter", "set_father", "1030.3.4"},
		{"death_reason", "death_old_age", "death_reason", "1060.6.7"},
	}
	for _, want := range wantRefs {
		if !hasCharacterRef(refs, "child", want.kind, want.name, want.relation, want.phase) {
			t.Errorf("missing ref %+v in %+v", want, refs)
		}
	}
}

func TestCharacterHistoryScanQueryAndLLMEvidence(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("history/characters/test.txt", characterHistoryFixture)
	write("common/traits/test.txt", "test_trait = {}\n")
	write("common/deathreasons/test.txt", "death_old_age = {}\n")
	write("common/dynasties/test.txt", "dyn_test = {}\n")
	write("common/dynasty_houses/test.txt", "house_test = {}\n")

	cfgPath := filepath.Join(dir, "ck3-index.toml")
	cfgText := "database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	patterns, err := db.QueryPatterns(context.Background(), "character")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPattern(patterns.Fields, "birth") || !hasPattern(patterns.Fields, "father") || hasPattern(patterns.Fields, "1000.1.2") {
		t.Fatalf("unexpected character patterns: %+v", patterns.Fields)
	}

	object, err := db.QueryObject(context.Background(), "character:child")
	if err != nil {
		t.Fatal(err)
	}
	if len(object.CharacterProfiles) != 1 {
		t.Fatalf("character profile count=%d: %+v", len(object.CharacterProfiles), object)
	}
	profile := object.CharacterProfiles[0]
	if len(profile.StaticFields) == 0 || len(profile.Timeline) != 4 || profile.Timeline[0].Date != "1000.1.2" {
		t.Fatalf("unexpected character profile: %+v", profile)
	}

	refs, err := db.QueryRefs(context.Background(), "character:child")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRefHit(refs.Outgoing, "character", "spouse", "add_spouse", "1020.2.3", "resolved") ||
		!hasRefHit(refs.Outgoing, "dynasty_house", "house_test", "dynasty_house", "", "resolved") {
		t.Fatalf("character refs were not resolved with relation/date metadata: %+v", refs.Outgoing)
	}

	result, err := db.LLMQueryObject(context.Background(), "character:child", LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts["character_history_dates"] != 4 || !hasEvidenceKind(result.Evidence, "character_history") {
		t.Fatalf("missing model-facing character timeline evidence: %+v", result)
	}
	public, err := db.LLMQueryObject(context.Background(), "character:child", LLMOptions{Mode: "public", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range public.Evidence {
		if evidence.Source == "project" {
			t.Fatalf("public result leaked character history evidence: %+v", public)
		}
	}
}

func hasCharacterField(fields []objectFieldRow, objectName, field string, date int) bool {
	for _, item := range fields {
		if item.ObjectName == objectName && item.Field == field && item.DateKey == date {
			return true
		}
	}
	return false
}

func hasCharacterRef(refs []refRow, from, kind, name, relation, phase string) bool {
	for _, ref := range refs {
		if ref.FromName == from && ref.Kind == kind && ref.Name == name && ref.Relation == relation && ref.Phase == phase && ref.Confidence == "exact" {
			return true
		}
	}
	return false
}

func hasPattern(fields []PatternField, name string) bool {
	for _, field := range fields {
		if field.Field == name {
			return true
		}
	}
	return false
}

func hasRefHit(hits []RefHit, kind, name, relation, phase, resolution string) bool {
	for _, hit := range hits {
		if hit.Kind == kind && hit.Name == name && hit.Relation == relation && hit.Phase == phase && hit.Resolution == resolution {
			return true
		}
	}
	return false
}

func hasEvidenceKind(evidence []LLMEvidence, kind string) bool {
	for _, item := range evidence {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

const characterHistoryFixture = `parent = {
	name = "Parent"
	980.1.1 = { birth = yes }
}
mother = {
	name = "Mother"
	982.1.1 = { birth = yes }
}
spouse = {
	name = "Spouse"
	1001.1.1 = { birth = yes }
}
adopter = {
	name = "Adopter"
	970.1.1 = { birth = yes }
}
child = {
	name = "Child"
	dynasty = dyn_test
	dynasty_house = house_test
	father = parent
	mother = mother
	employer = parent
	trait = test_trait
	1000.1.2 = { birth = yes }
	1020.2.3 = {
		add_spouse = spouse
		add_trait = test_trait
	}
	1030.3.4 = {
		effect = {
			set_father = character:adopter
		}
	}
	1060.6.7 = {
		death = { death_reason = death_old_age }
	}
}
`
