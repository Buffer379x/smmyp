package vars

import (
	"reflect"
	"testing"
)

// TC-PRM-07 (Server-Teil): Platzhalter erkennen und füllen.
func TestParseFillTC_PRM_07(t *testing.T) {
	text := `lo-fi {{genre}}, {{ bpm = 72 }} bpm, {{genre}} again, \{{jinja}} {{Stimmung Lage}}`
	got := Parse(text)
	want := []Var{{"genre", ""}, {"bpm", "72"}, {"Stimmung Lage", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse: %+v", got)
	}
	out := Fill(text, map[string]string{"genre": "jazz", "Stimmung Lage": "ruhig"})
	if out != `lo-fi jazz, 72 bpm, jazz again, {{jinja}} ruhig` {
		t.Fatalf("Fill: %q", out)
	}
	if Has(`nur \{{escaped}} und {{}} und {{ }}`) {
		t.Fatal("leere/escapte Klammern sind keine Platzhalter")
	}
}
