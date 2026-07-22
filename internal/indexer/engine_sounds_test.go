package indexer

import "testing"

func TestEngineSoundsUseCurrentVanillaEvidence(t *testing.T) {
	for _, event := range []string{
		"event:/artifact_generic",
		"event:/DLC/BP1/MUSIC/moodtrack/mx_BP1Mood_Generic",
	} {
		if !IsSound(event) {
			t.Fatalf("current vanilla sound event %q is missing", event)
		}
	}
	if IsSound("event:/ck3_index_not_a_real_sound") {
		t.Fatal("unknown sound was accepted")
	}
	for event := range engineSounds {
		if event[len(event)-1] == ']' {
			t.Fatalf("list delimiter leaked into generated sound id %q", event)
		}
	}
}
