package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDSLAuditEvidenceIsDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "common", "traits"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"test.txt": "test={good=yes}", "test.info": "good = yes # example"} {
		if err := os.WriteFile(filepath.Join(root, "common", "traits", name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := NewStandaloneChecker(context.Background(), "")
	a, err := c.AuditDSL(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.AuditDSL(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.CorpusFingerprint != b.CorpusFingerprint || a.Files != 1 || a.DocumentationFiles != 1 || a.Families["common/traits"].Fields["good"].Shapes["atom"] != 1 {
		t.Fatalf("invalid evidence: %+v", a)
	}
	if err := os.WriteFile(filepath.Join(root, "common", "traits", "test.info"), []byte("good = no # changed documentation"), 0644); err != nil {
		t.Fatal(err)
	}
	d, err := c.AuditDSL(context.Background(), root)
	if err != nil || d.CorpusFingerprint == a.CorpusFingerprint {
		t.Fatalf("documentation drift concealed: %v", err)
	}
}

func TestLocalizationApostropheKeysAreIndexed(t *testing.T) {
	entries, err := parseLocBytes("localization/english/titles_l_english.yml", []byte("l_english:\n b_mansa'l-kharaz:0 \"Mansa'l-Kharaz\"\n b_ka'abir:0 \"Ka'abir\""))
	if err != nil || len(entries) != 2 || entries[0].key != "b_mansa'l-kharaz" {
		t.Fatalf("vanilla keys lost: %+v %v", entries, err)
	}
}
