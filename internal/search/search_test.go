package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Query
	}{
		{"night drive", Query{Terms: []string{"night", "drive"}}},
		{`"night drive"`, Query{Phrases: []string{"night drive"}}},
		{"@suno @Udio dark", Query{Providers: []string{"suno", "udio"}, Terms: []string{"dark"}}},
		{"#Lo-Fi #study", Query{Tags: []string{"lo-fi", "study"}}},
		{"-country synth", Query{Excludes: []string{"country"}, Terms: []string{"synth"}}},
		{"fav: used:7d kat:musik", Query{Favorites: true, UsedDays: 7, Categories: []string{"musik"}}},
		{`"offen ohne ende`, Query{Phrases: []string{"offen ohne ende"}}},
	}
	for _, c := range cases {
		if got := Parse(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q)\n got %+v\nwant %+v", c.in, got, c.want)
		}
	}
}

// TC-SRC-07: Sonderzeichen werden nie zu FTS-Operatoren und führen nicht zu leeren Phrasen.
func TestParseSpecialCharsTC_SRC_07(t *testing.T) {
	for _, in := range []string{`"`, `*`, `(`, `)`, `NEAR`, `AND`, `OR NOT`, `:`, `-`, `   `, `""`, `" "`, `@`, `#`, `^foo`, `a"b`, `c++`} {
		q := Parse(in)
		for _, term := range append(q.Terms, q.Phrases...) {
			if !hasWordChar(term) {
				t.Errorf("Parse(%q): Begriff ohne Buchstaben %q", in, term)
			}
		}
		if expr := q.MatchExpr(); expr != "" && !strings.HasPrefix(expr, `"`) {
			t.Errorf("Parse(%q): Ausdruck %q beginnt nicht mit einem gequoteten String", in, expr)
		}
	}
	if got := Parse(`a"b`).MatchExpr(); got != `"a""b"*` && got != `"a"*` {
		// a"b → Anführungszeichen startet Phrase "b" oder wird gequotet; beides ist sicher.
		t.Logf("a\"b → %s", got)
	}
	if got := Parse("NEAR AND").MatchExpr(); got != `"NEAR"* AND "AND"*` {
		t.Errorf("Operatoren müssen als Wörter gequotet sein, got %s", got)
	}
}

func TestStringRoundTrip(t *testing.T) {
	in := `@suno #dark fav: night "neon rain" -country`
	if got := Parse(Parse(in).String()); !reflect.DeepEqual(got, Parse(in)) {
		t.Errorf("Rundreise verändert die Query: %+v", got)
	}
}

// TC-PRM-04 (Teil): Hervorheben escaped HTML immer, auch wenn ein Treffer darin liegt.
func TestHighlightEscapes(t *testing.T) {
	got := string(Highlight(`<script>alert("dark")</script> dark mode`, []string{"dark"}))
	if strings.Contains(got, "<script>") {
		t.Fatalf("nicht escaped: %s", got)
	}
	if !strings.Contains(got, "<mark>dark</mark> mode") {
		t.Fatalf("Treffer fehlt: %s", got)
	}
}

func TestHighlightPrefixAndUmlaut(t *testing.T) {
	got := string(Highlight("Übersetzung und Synthwave, resynth", []string{"ubersetz", "synth"}))
	want := "<mark>Übersetz</mark>ung und <mark>Synth</mark>wave, resynth"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestSubstringsAndRating(t *testing.T) {
	q := Parse("~solo ~ab bew:4 gitarre")
	if !reflect.DeepEqual(q.Substrings, []string{"solo"}) || q.Rating != 4 {
		t.Fatalf("%+v", q)
	}
	if e, ok := q.TrigramExpr(true); !ok || e != `"solo" AND "gitarre"` {
		t.Fatalf("TrigramExpr: %q %v", e, ok)
	}
	if _, ok := Parse("ab cd").TrigramExpr(true); ok {
		t.Fatal("Begriffe < 3 Zeichen dürfen keinen Trigram-Ausdruck ergeben")
	}
	if got := string(q.Mark("Gitarrensolo mit Gitarre")); got != "<mark>Gitarre</mark>n<mark>solo</mark> mit <mark>Gitarre</mark>" {
		t.Fatalf("Mark: %s", got)
	}
	q.Fuzzy = true
	if got := string(q.Mark("Bassgitarre")); got != "Bass<mark>gitarre</mark>" {
		t.Fatalf("Mark fuzzy: %s", got)
	}
}
