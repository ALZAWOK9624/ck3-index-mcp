package indexer

import "testing"

func TestEngineDefinesUseCurrentVanillaSource(t *testing.T) {
	if !IsDefine("@NGameIcons|GREAT_PROJECT_ILLUSTRATION_PATH") {
		t.Fatal("current CK3 define missing from generated table")
	}
	if IsDefine("@NGameIcons|CK3_INDEX_NOT_A_REAL_DEFINE") {
		t.Fatal("unknown define was accepted")
	}
	if IsDefine("@NReligion|DEFAULT_FERVOR") {
		t.Fatal("retired legacy define leaked into the current table")
	}
}
