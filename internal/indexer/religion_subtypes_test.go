package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestReligionSubdirectoriesUseCanonicalObjectTypes(t *testing.T) {
	want := map[string]string{
		"common/religion/religion_families/00_pagan.txt":           "religion_family",
		"common/religion/fervor_modifiers/00_fervor_modifiers.txt": "fervor_modifier",
	}
	for path, typ := range want {
		if got := objectTypeForPath(path); got != typ {
			t.Errorf("object type for %s=%q want %q", path, got, typ)
		}
	}
}

func TestExtractReligionFamilyReferences(t *testing.T) {
	religionFile := script.Parse(`test_religion = { family = rf_pagan faiths = {} }`)
	religionRecord := fileRecord{ID: 1, RelPath: "common/religion/religions/test.txt", SourceName: "game", SourceRank: 3}
	religionObjects := extractObjects(religionRecord, religionFile.Nodes)
	refs := extractRefs(religionRecord, religionFile.Nodes, religionObjects)

	familyFile := script.Parse(`rf_pagan = { hostility_doctrine = pagan_hostility_doctrine }`)
	familyRecord := fileRecord{ID: 2, RelPath: "common/religion/religion_families/00_pagan.txt", SourceName: "game", SourceRank: 3}
	familyObjects := extractObjects(familyRecord, familyFile.Nodes)
	refs = append(refs, extractRefs(familyRecord, familyFile.Nodes, familyObjects)...)

	want := map[string]bool{
		"religion:test_religion->religion_family:rf_pagan":            false,
		"religion_family:rf_pagan->doctrine:pagan_hostility_doctrine": false,
	}
	for _, ref := range refs {
		key := ref.FromType + ":" + ref.FromName + "->" + ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing religion-family reference %s: %+v", key, refs)
		}
	}
}
