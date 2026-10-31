package tags

import (
	"reflect"
	"testing"
)

// TC-TAG-01 (Teil Phase 1): Schreibvarianten landen beim selben Tag. „lofi" → „lo-fi" braucht Aliase (Phase 2).
func TestNormalizeTC_TAG_01(t *testing.T) {
	for in, want := range map[string]string{
		"Lo Fi": "lo-fi", "LO-FI": "lo-fi", " #lo_fi ": "lo-fi", "#Synthwave": "synthwave",
		"Übersetzen": "übersetzen", "a  --  b": "a-b", "###": "", "C++": "c", "98 BPM": "98-bpm",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseList(t *testing.T) {
	got := Parse("#lofi, Study Beats #chill;lofi")
	want := []string{"lofi", "study-beats", "chill"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
