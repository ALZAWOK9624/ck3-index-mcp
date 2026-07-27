package migrator

import (
	"strings"
	"testing"
)

func TestBuildRebaseProvinceMappingExactRGB(t *testing.T) {
	base := []byte("0;0;0;0;x;x\n10;11;22;33;base;x\n20;44;55;66;base2;x\n")
	project := []byte("0;0;0;0;x;x\n101;11;22;33;project;x\n202;44;55;66;project2;x\n")
	target := []byte("0;0;0;0;x;x\n1001;11;22;33;target;x\n2002;44;55;66;target2;x\n3003;77;88;99;new;x\n")

	mapping, err := buildRebaseProvinceMapping(base, project, target)
	if err != nil {
		t.Fatalf("build exact mapping: %v", err)
	}
	if mapping.Ambiguous {
		t.Fatal("exact mapping was unexpectedly ambiguous")
	}
	for targetID, wantProjectID := range map[int]int{0: 0, 1001: 101, 2002: 202} {
		gotProjectID, ok := mapping.RewriteExactProvinceID(targetID)
		if !ok || gotProjectID != wantProjectID {
			t.Fatalf("target ID %d mapping = (%d, %v), want (%d, true)", targetID, gotProjectID, ok, wantProjectID)
		}
	}
	if _, ok := mapping.RewriteExactProvinceID(3003); ok {
		t.Fatal("unmapped target ID was inferred as an identity mapping")
	}
	if got := mapping.UnmappedTargetIDs; len(got) != 1 || got[0] != 3003 {
		t.Fatalf("unmapped target IDs = %v, want [3003]", got)
	}
}

func TestBuildRebaseProvinceMappingChangedIDs(t *testing.T) {
	base := []byte("10;1;2;3;old;x\n")
	project := []byte("900;1;2;3;project;x\n")
	target := []byte("77;1;2;3;target;x\n")

	mapping, err := buildRebaseProvinceMapping(base, project, target)
	if err != nil {
		t.Fatalf("build renumbered mapping: %v", err)
	}
	if got, ok := mapping.RewriteExactProvinceID(77); !ok || got != 900 {
		t.Fatalf("renumbered target mapping = (%d, %v), want (900, true)", got, ok)
	}
	if _, ok := mapping.RewriteExactProvinceID(10); ok {
		t.Fatal("base ID must not be inferred as a target mapping")
	}
}

func TestBuildRebaseProvinceMappingRejectsDuplicateIDsAndRGB(t *testing.T) {
	valid := []byte("1;1;2;3;a;x\n")
	for name, target := range map[string][]byte{
		"duplicate ID":  []byte("1;1;2;3;a;x\n1;4;5;6;b;x\n"),
		"duplicate RGB": []byte("1;1;2;3;a;x\n2;1;2;3;b;x\n"),
	} {
		t.Run(name, func(t *testing.T) {
			mapping, err := buildRebaseProvinceMapping(valid, valid, target)
			if err == nil {
				t.Fatal("duplicate definition was accepted")
			}
			if !mapping.Ambiguous {
				t.Fatalf("duplicate definition did not mark mapping ambiguous: %+v", mapping)
			}
			if !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("duplicate error missing reason: %v", err)
			}
		})
	}
}

func TestBuildRebaseProvinceMappingAcceptsBOMCRLFAndComments(t *testing.T) {
	base := []byte("\ufeff# base comment\r\n\r\n0;0;0;0;x;x\r\n1;10;20;30;one;x\r\n")
	project := []byte("// project comment\r\n0;0;0;0;x;x\r\n11;10;20;30;one;x\r\n")
	target := []byte("# target comment; still a comment\r\n0;0;0;0;x;x\r\n21;10;20;30;one;x\r\n")

	mapping, err := buildRebaseProvinceMapping(base, project, target)
	if err != nil {
		t.Fatalf("parse BOM/CRLF definition: %v", err)
	}
	if got, ok := mapping.RewriteExactProvinceID(21); !ok || got != 11 {
		t.Fatalf("BOM/CRLF mapping = (%d, %v), want (11, true)", got, ok)
	}
}

func TestBuildRebaseProvinceMappingRejectsMalformedDefinitions(t *testing.T) {
	valid := []byte("1;1;2;3;a;x\n")
	for name, definition := range map[string][]byte{
		"empty":          nil,
		"too few fields": []byte("1;2;3\n"),
		"bad component":  []byte("1;999;2;3;x\n"),
		"negative ID":    []byte("-1;1;2;3;x\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildRebaseProvinceMapping(definition, valid, valid); err == nil {
				t.Fatal("malformed base definition was accepted")
			}
		})
	}
}

func TestRebaseMapPathRouting(t *testing.T) {
	for _, rel := range []string{
		"history/provinces/00_provinces.txt",
		"history\\provinces\\01_provinces.TXT",
		"common/province_terrain/00_terrain.txt",
		"history/titles/00_titles.txt",
		"common/landed_titles/00_titles.txt",
	} {
		if !rebaseMapReferenceFile(rel) {
			t.Fatalf("map reference path not routed: %q", rel)
		}
	}
	for _, rel := range []string{
		"history/provinces.txt",
		"history/provinces/readme.md",
		"common/province_terrain.txt",
		"common/landed_titles/00_titles.gui",
		"common/culture/landed_titles.txt",
		"map_data/definition.csv",
		"other/history/titles/00_titles.txt",
		"other/../history/titles/00_titles.txt",
	} {
		if rebaseMapReferenceFile(rel) {
			t.Fatalf("non-reference path was routed: %q", rel)
		}
	}
	for _, rel := range []string{
		"map_data/definition.csv",
		"MAP_DATA\\DEFINITION.CSV",
		"./map_data/definition.csv",
	} {
		if !rebaseCoreMapDefinitionPath(rel) {
			t.Fatalf("definition path not recognized: %q", rel)
		}
	}
	for _, rel := range []string{
		"definition.csv",
		"map_data/definitions.csv",
		"map_data/definition.csv.bak",
		"other/map_data/definition.csv",
		"other/../map_data/definition.csv",
	} {
		if rebaseCoreMapDefinitionPath(rel) {
			t.Fatalf("non-definition path was recognized: %q", rel)
		}
	}
}
