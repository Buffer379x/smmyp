package prompts

import (
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
)

// TC-PRM-08: Bearbeiten erzeugt Versionen; eine alte Version lässt sich wiederherstellen.
func TestVersionsTC_PRM_08(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "V1", []string{"claude"}, map[string]string{"prompt": "erste Fassung"})
	p, _ := e.svc.Get(e.ctx, e.a, id)
	for _, txt := range []string{"zweite Fassung", "dritte Fassung"} {
		in := p.Input()
		in.Fields = map[string]string{"prompt": txt}
		if err := e.svc.Update(e.ctx, e.a, id, in); err != nil {
			t.Fatal(err)
		}
	}
	// Nur Favorit ändern erzeugt keine Version.
	in := p.Input()
	in.Fields = map[string]string{"prompt": "dritte Fassung"}
	in.Favorite = true
	e.svc.Update(e.ctx, e.a, id, in)
	vs, err := e.svc.Versions(e.ctx, e.a, id)
	if err != nil || len(vs) != 2 || vs[0].Fields["prompt"] != "zweite Fassung" || vs[1].Fields["prompt"] != "erste Fassung" {
		t.Fatalf("Versionen: %+v %v", vs, err)
	}
	if err := e.svc.RestoreVersion(e.ctx, e.a, id, vs[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = e.svc.Get(e.ctx, e.a, id)
	if p.Fields["prompt"] != "erste Fassung" {
		t.Fatalf("Wiederherstellen: %v", p.Fields)
	}
	if vs, _ := e.svc.Versions(e.ctx, e.a, id); len(vs) != 3 || vs[0].Fields["prompt"] != "dritte Fassung" {
		t.Fatalf("aktuelle Fassung wurde nicht gesichert: %d", len(vs))
	}
	if _, err := e.svc.Versions(e.ctx, e.b, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("fremde Versionen sichtbar")
	}
	if err := e.svc.RestoreVersion(e.ctx, e.b, id, vs[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("fremdes Wiederherstellen möglich")
	}
}

// TC-PRM-10: Geheimnisse werden erkannt (ohne Doppelmeldung).
func TestSecretsTC_PRM_10(t *testing.T) {
	got := Secrets("Nutze sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123 und ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	if !reflect.DeepEqual(got, []string{"Anthropic-API-Schlüssel", "GitHub-Token"}) {
		t.Fatalf("got %v", got)
	}
	if got := Secrets("sk-proj-abcdefghijklmnopqrstuvwx1234"); !reflect.DeepEqual(got, []string{"OpenAI-API-Schlüssel"}) {
		t.Fatalf("OpenAI: %v", got)
	}
	if got := Secrets("dark synthwave, sk-8 skateboard"); got != nil {
		t.Fatalf("Fehlalarm: %v", got)
	}
}

// TC-SRC-11: Teilwort findet Kompositum; Rückfall wird gekennzeichnet; ~ geht immer.
func TestTrigramTC_SRC_11(t *testing.T) {
	e := newEnv(t)
	e.create(e.a, "Rock-Ballade", []string{"suno"}, map[string]string{"style": "langes Gitarrensolo, Übersteuerung"})
	e.create(e.a, "Solo-Piano", []string{"suno"}, map[string]string{"style": "solo piano, ruhig"})
	r := e.search(e.a, "gitarrensol")
	if r.Total != 1 || r.Fuzzy {
		t.Fatalf("Präfixsuche: %d fuzzy=%v", r.Total, r.Fuzzy)
	}
	r = e.search(e.a, "solo")
	if r.Total != 2 || !r.Fuzzy {
		t.Fatalf("„solo\" sollte per Teilwortsuche beide finden: %d %v fuzzy=%v", r.Total, titles(r), r.Fuzzy)
	}
	if r := e.search(e.a, "~steuer"); r.Total != 1 {
		t.Fatalf("~steuer: %d", r.Total)
	}
	if r := e.search(e.b, "solo"); r.Total != 0 {
		t.Fatal("Teilwortsuche findet fremde Prompts")
	}
}

// TC-TAG-01 (vollständig) und TC-TAG-02: Aliase, Umbenennen, Zusammenführen.
func TestTagAliasesTC_TAG_01_02(t *testing.T) {
	e := newEnv(t)
	id1 := e.create(e.a, "A", []string{"suno"}, map[string]string{"style": "x"}, "lofi", "Study Beats")
	p, _ := e.svc.Get(e.ctx, e.a, id1)
	if !reflect.DeepEqual(p.Tags, []string{"lo-fi", "study-beats"}) {
		t.Fatalf("Standard-Alias: %v", p.Tags)
	}
	if r := e.search(e.a, "#lofi"); r.Total != 1 {
		t.Fatal("Suche über Alias")
	}
	id2 := e.create(e.a, "B", []string{"suno"}, map[string]string{"style": "y"}, "study")
	// Zusammenführen: study-beats → study
	if n, err := e.svc.RenameTag(e.ctx, e.a, "study-beats", "study"); err != nil || n != 1 {
		t.Fatalf("RenameTag: %d %v", n, err)
	}
	if r := e.search(e.a, "#study"); r.Total != 2 {
		t.Fatalf("nach Zusammenführen: %d", r.Total)
	}
	if r := e.search(e.a, "study"); r.Total != 2 {
		t.Fatal("Volltextindex nach Umbenennen nicht aktuell")
	}
	// alter Name ist jetzt Alias
	id3 := e.create(e.a, "C", []string{"suno"}, map[string]string{"style": "z"}, "study-beats")
	p3, _ := e.svc.Get(e.ctx, e.a, id3)
	if !reflect.DeepEqual(p3.Tags, []string{"study"}) {
		t.Fatalf("alter Name als Alias: %v", p3.Tags)
	}
	// Umbenennen auf neuen Namen
	e.svc.RenameTag(e.ctx, e.a, "study", "lernen")
	p2, _ := e.svc.Get(e.ctx, e.a, id2)
	if !reflect.DeepEqual(p2.Tags, []string{"lernen"}) {
		t.Fatalf("Umbenennen: %v", p2.Tags)
	}
	if err := e.svc.AddAlias(e.ctx, e.a, "lernen", "x"); err == nil {
		t.Fatal("Alias auf bestehenden Tag-Namen muss abgelehnt werden")
	}
	if _, err := e.svc.RenameTag(e.ctx, e.b, "lernen", "weg"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("fremder Tag umbenennbar")
	}
	if err := e.svc.AddRule(e.ctx, e.a, tags.Rule{Kind: "regex", Pattern: "(unclosed", Tag: "x"}); err == nil {
		t.Fatal("ungültige Regex angenommen")
	}
}

// TC-ML-07 (Integration): fast gleicher Prompt wird erkannt, fremde nicht.
func TestNearDuplicate(t *testing.T) {
	e := newEnv(t)
	f := map[string]string{"style": "dark synthwave with analog pads, gated reverb drums, breathy female vocals, night city, 98 bpm"}
	id := e.create(e.a, "Night", []string{"suno"}, f)
	near := map[string]string{"style": "dark synthwave with analog pads, gated reverb drums, breathy female vocals, night town, 98 bpm"}
	got, title, err := e.svc.NearDuplicate(e.ctx, e.a, "Night", near, 0)
	if err != nil || got != id || title != "Night" {
		t.Fatalf("NearDuplicate: %d %q %v", got, title, err)
	}
	if got, _, _ := e.svc.NearDuplicate(e.ctx, e.b, "Night", near, 0); got != 0 {
		t.Fatal("fremder Prompt als Duplikat gemeldet")
	}
	if got, _, _ := e.svc.NearDuplicate(e.ctx, e.a, "Night", near, id); got != 0 {
		t.Fatal("eigener Prompt beim Bearbeiten als Duplikat gemeldet")
	}
}

func TestRatingLinksVarsBulkImport(t *testing.T) {
	e := newEnv(t)
	id, err := e.svc.Create(e.ctx, e.a, Input{Title: "Bewertet", ProviderIDs: []int64{e.prov["suno"]}, Fields: map[string]string{"style": "a {{genre=jazz}}"},
		Rating: 4, Links: []Link{{Label: "Song", URL: "https://suno.com/song/1"}, {URL: "javascript:alert(1)"}}})
	if err == nil {
		t.Fatal("javascript:-Link angenommen")
	}
	id, err = e.svc.Create(e.ctx, e.a, Input{Title: "Bewertet", ProviderIDs: []int64{e.prov["suno"]}, Fields: map[string]string{"style": "a {{genre=jazz}}"},
		Rating: 4, Links: []Link{{Label: "Song", URL: "https://suno.com/song/1"}}, SourceURL: "javascript:x"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.svc.Get(e.ctx, e.a, id)
	if p.Rating != 4 || len(p.Links) != 1 || p.SourceURL != "" || p.SimHash == 0 {
		t.Fatalf("Rating/Links/Quelle/SimHash: %+v", p)
	}
	e.create(e.a, "Ohne", []string{"suno"}, map[string]string{"style": "b"})
	if r := e.search(e.a, "bew:4"); r.Total != 1 {
		t.Fatalf("bew:4 → %d", r.Total)
	}
	if r, _ := e.svc.List(e.ctx, e.a, ListOptions{Sort: SortRating}); r.Items[0].ID != id {
		t.Fatal("Sortierung nach Bewertung")
	}
	// Platzhalter-Werte
	if err := e.svc.SaveVarValues(e.ctx, e.a, id, map[string]string{"genre": "funk"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.VarValues(e.ctx, e.a, id); v["genre"] != "funk" {
		t.Fatalf("VarValues: %v", v)
	}
	if _, err := e.svc.VarValues(e.ctx, e.b, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("fremde Variablenwerte lesbar")
	}
	// Mehrfachaktionen: fremde IDs werden übersprungen
	foreign := e.create(e.b, "Fremd", []string{"suno"}, map[string]string{"style": "c"})
	n, err := e.svc.Bulk(e.ctx, e.a, []int64{id, foreign}, "tag", "Sammlung X")
	if err != nil || n != 1 {
		t.Fatalf("Bulk tag: %d %v", n, err)
	}
	if fp, _ := e.svc.Get(e.ctx, e.b, foreign); len(fp.Tags) != 0 {
		t.Fatal("fremder Prompt verändert")
	}
	n, _ = e.svc.Bulk(e.ctx, e.a, []int64{id}, "provider", strconv.FormatInt(e.prov["udio"], 10))
	if p, _ := e.svc.Get(e.ctx, e.a, id); n != 1 || len(p.Providers) != 2 {
		t.Fatal("Bulk provider")
	}
	// Export → Import (Duplikate übersprungen, unbekannte Anbieter gemeldet)
	exp, _ := e.svc.Export(e.ctx, e.a)
	exp = append(exp, ExportPrompt{Title: "Neu", Providers: []string{"Claude"}, Fields: map[string]string{"prompt": "neu"}},
		ExportPrompt{Title: "Kaputt", Providers: []string{"gibtsnicht"}, Fields: map[string]string{"prompt": "x"}})
	rep, err := e.svc.Import(e.ctx, e.a, exp)
	if err != nil || rep.Imported != 1 || rep.Duplicates != 2 || len(rep.Failed) != 1 {
		t.Fatalf("Import: %+v %v", rep, err)
	}
	if rep, _ := e.svc.Import(e.ctx, e.b, exp[:1]); rep.Imported != 1 {
		t.Fatal("Import für anderen Benutzer muss unabhängig sein")
	}
}
