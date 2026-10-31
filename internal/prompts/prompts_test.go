package prompts

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/search"
	"github.com/Buffer379x/smmyp/internal/store"
)

type env struct {
	t    *testing.T
	ctx  context.Context
	svc  *Service
	a, b store.Scope
	prov map[string]int64
}

func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := providers.Seed(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"anna", "ben"} {
		if _, err := db.W.Exec("INSERT INTO users(username, role, created_at) VALUES (?, 'member', 0)", u); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{ctx: ctx, svc: &Service{DB: db}, a: store.Scope{UserID: 1}, b: store.Scope{UserID: 2}, prov: map[string]int64{}}
	all, _ := providers.List(ctx, db, e.a)
	for _, p := range all {
		e.prov[p.Slug] = p.ID
	}
	if tt, ok := t.(*testing.T); ok {
		e.t = tt
	}
	return e
}

func (e *env) create(sc store.Scope, title string, provs []string, fields map[string]string, tg ...string) int64 {
	e.t.Helper()
	var ids []int64
	for _, s := range provs {
		ids = append(ids, e.prov[s])
	}
	id, err := e.svc.Create(e.ctx, sc, Input{Title: title, ProviderIDs: ids, Fields: fields, Tags: tg})
	if err != nil {
		e.t.Fatalf("Create %q: %v", title, err)
	}
	return id
}

func (e *env) search(sc store.Scope, q string) Result {
	e.t.Helper()
	r, err := e.svc.List(e.ctx, sc, ListOptions{Query: search.Parse(q)})
	if err != nil {
		e.t.Fatalf("List %q: %v", q, err)
	}
	return r
}

func titles(r Result) []string {
	var out []string
	for _, p := range r.Items {
		out = append(out, p.Title)
	}
	return out
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Create(e.ctx, e.a, Input{})
	var verr ValidationError
	if !errors.As(err, &verr) || verr["title"] == "" || verr["providers"] == "" {
		t.Fatalf("TC-PRM-02: erwartet Feldfehler, got %v", err)
	}
	// TC-PRM-03: Feldlänge
	long := make([]rune, MaxFieldLen+1)
	for i := range long {
		long[i] = 'x'
	}
	_, err = e.svc.Create(e.ctx, e.a, Input{Title: "lang", ProviderIDs: []int64{e.prov["claude"]}, Fields: map[string]string{"prompt": string(long)}})
	if !errors.As(err, &verr) || verr["field_prompt"] == "" {
		t.Fatalf("TC-PRM-03: 50.001 Zeichen müssen abgelehnt werden, got %v", err)
	}
	if _, err := e.svc.Create(e.ctx, e.a, Input{Title: "genau", ProviderIDs: []int64{e.prov["claude"]}, Fields: map[string]string{"prompt": string(long[:MaxFieldLen])}}); err != nil {
		t.Fatalf("TC-PRM-03: 50.000 Zeichen müssen gehen: %v", err)
	}
}

// TC-PRM-01 + TC-PRM-11 + TC-PRM-12: Felder je Anbieter, Feld-Union, mehrere Anbieter.
func TestMultiProviderAndFieldUnion(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "Night Drive", []string{"suno", "udio"}, map[string]string{
		"style": "dark synthwave", "lyrics": "[Verse]", "exclude": "country", "params": "--ar 1:1",
	})
	p, err := e.svc.Get(e.ctx, e.a, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Providers) != 2 || p.Primary().Slug != "suno" {
		t.Fatalf("TC-PRM-11: erwartet Suno (Haupt) + Udio, got %+v", p.Providers)
	}
	if _, ok := p.Fields["params"]; ok {
		t.Error("TC-PRM-12: params gehört zu keinem zugeordneten Anbieter und muss verworfen werden")
	}
	if p.Fields["exclude"] != "country" || p.Fields["style"] != "dark synthwave" {
		t.Errorf("Felder falsch: %v", p.Fields)
	}
	for _, slug := range []string{"suno", "udio"} {
		r, _ := e.svc.List(e.ctx, e.a, ListOptions{ContextID: e.prov[slug]})
		if r.Total != 1 {
			t.Errorf("TC-PRM-11: Prompt muss in der Schublade %s liegen", slug)
		}
	}
	// Hauptanbieter wechseln: Positionen tauschen darf den Teilindex nicht verletzen.
	err = e.svc.Update(e.ctx, e.a, id, Input{Title: "Night Drive", ProviderIDs: []int64{e.prov["udio"], e.prov["suno"]}, Fields: p.Fields})
	if err != nil {
		t.Fatalf("Hauptanbieter wechseln: %v", err)
	}
	p, _ = e.svc.Get(e.ctx, e.a, id)
	if p.Primary().Slug != "udio" {
		t.Errorf("Hauptanbieter sollte udio sein, ist %s", p.Primary().Slug)
	}
	// TC-PRM-13 (Server): ohne Anbieter wird das Speichern verweigert.
	if err := e.svc.Update(e.ctx, e.a, id, Input{Title: "x", Fields: p.Fields}); err == nil {
		t.Error("TC-PRM-13: Entfernen des letzten Anbieters muss scheitern")
	}
	// Suno entfernen: exclude gehört nur zu Suno und fällt weg.
	e.svc.Update(e.ctx, e.a, id, Input{Title: "Night Drive", ProviderIDs: []int64{e.prov["udio"]}, Fields: p.Fields})
	p, _ = e.svc.Get(e.ctx, e.a, id)
	if _, ok := p.Fields["exclude"]; ok || len(p.Providers) != 1 {
		t.Errorf("nach Entfernen von Suno: %v %v", p.Fields, p.Providers)
	}
}

// TC-ISO-01..03: fremde IDs verhalten sich wie nicht existent.
func TestIsolationTC_ISO(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "Geheim", []string{"claude"}, map[string]string{"prompt": "zauberwort xylophon"}, "privat")
	if _, err := e.svc.Get(e.ctx, e.b, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TC-ISO-01: Get fremd → %v", err)
	}
	if r := e.search(e.b, "xylophon"); r.Total != 0 || r.FacetTotal != 0 || len(r.Facets) != 0 {
		t.Errorf("TC-ISO-02: Suche fremd findet %d", r.Total)
	}
	if r := e.search(e.b, "#privat"); r.Total != 0 {
		t.Errorf("TC-ISO-02: Tag-Suche fremd findet %d", r.Total)
	}
	checks := map[string]error{
		"Update":   e.svc.Update(e.ctx, e.b, id, Input{Title: "x", ProviderIDs: []int64{e.prov["claude"]}, Fields: map[string]string{"prompt": "y"}}),
		"Trash":    e.svc.Trash(e.ctx, e.b, id),
		"MarkUsed": e.svc.MarkUsed(e.ctx, e.b, id, 0),
	}
	_, favErr := e.svc.ToggleFavorite(e.ctx, e.b, id)
	checks["ToggleFavorite"] = favErr
	for name, err := range checks {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("TC-ISO-03: %s fremd → %v", name, err)
		}
	}
	p, _ := e.svc.Get(e.ctx, e.a, id)
	if p.Title != "Geheim" || p.DeletedAt != 0 || p.IsFavorite || p.UseCount != 0 {
		t.Errorf("TC-ISO-03: Daten von A verändert: %+v", p)
	}
	if exp, _ := e.svc.Export(e.ctx, e.b); len(exp) != 0 {
		t.Errorf("TC-ISO-04: Export von B enthält %d Prompts", len(exp))
	}
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	e.create(e.a, "Dark Synthwave Night Drive", []string{"suno", "udio"}, map[string]string{"style": "dark synthwave, analog pads", "exclude": "country"}, "dark", "synthwave")
	e.create(e.a, "Lo-Fi Lernsession", []string{"suno"}, map[string]string{"style": "lo-fi hip hop, mellow, dark jazzy chords"}, "lo-fi", "study")
	e.create(e.a, "Country Ballade", []string{"suno"}, map[string]string{"style": "country ballad, acoustic"}, "country")
	e.create(e.a, "Übersetzung DE→EN", []string{"claude"}, map[string]string{"prompt": "Übersetze den Text ins Englische"}, "übersetzen")
	e.create(e.a, "Code-Review", []string{"claude"}, map[string]string{"prompt": "Prüfe diesen Code auf synthwave-Anspielungen"})

	expect := func(id, q string, want int) Result {
		t.Helper()
		r := e.search(e.a, q)
		if r.Total != want {
			t.Errorf("%s: %q → %d Treffer %v, erwartet %d", id, q, r.Total, titles(r), want)
		}
		return r
	}
	expect("TC-SRC-01", "synthwave", 2)
	expect("TC-SRC-02", "synth", 2)
	expect("TC-SRC-03", "@suno dark", 2)
	expect("TC-SRC-04", "#lo-fi #study", 1)
	expect("TC-SRC-05", "-country", 3) // Night Drive hat „country" im Exclude-Feld
	expect("TC-SRC-06", "ubersetzung", 1)
	expect("TC-SRC-13", "@udio", 1)
	expect("Kategorie", "kat:musik", 3)
	expect("Phrase", `"analog pads"`, 1)
	expect("unbekannter Anbieter", "@gibtsnicht", 0)
	for _, q := range []string{`"`, `*`, `(`, `NEAR`, `AND`, `:`, `-`, "   ", `""`, `a"b`, `c++ -`, `NOT`} {
		if _, err := e.svc.List(e.ctx, e.a, ListOptions{Query: search.Parse(q)}); err != nil {
			t.Errorf("TC-SRC-07: %q → Fehler %v", q, err)
		}
	}
	// TC-SRC-09: Titeltreffer vor Texttreffer.
	if r := e.search(e.a, "synthwave"); len(r.Items) < 2 || r.Items[0].Title != "Dark Synthwave Night Drive" {
		t.Errorf("TC-SRC-09: Reihenfolge %v", titles(r))
	}
	// TC-SRC-08 + TC-SRC-14: Facetten zählen einen Prompt bei jedem Anbieter, „Alle" einmal.
	r := e.search(e.a, "@suno dark")
	if r.FacetTotal != 2 {
		t.Errorf("TC-SRC-14: FacetTotal %d, erwartet 2", r.FacetTotal)
	}
	got := map[string]int{}
	for _, f := range r.Facets {
		got[f.Provider.Slug] = f.Count
	}
	if got["suno"] != 2 || got["udio"] != 1 || len(got) != 2 {
		t.Errorf("TC-SRC-08/14: Facetten %v", got)
	}
	if r.Total != got["suno"] {
		t.Errorf("TC-SRC-08: Liste %d ≠ Facette %d", r.Total, got["suno"])
	}
}

// TC-SRC-10: Index ist nach Ändern und Löschen sofort aktuell.
func TestIndexInSyncTC_SRC_10(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "Alpha", []string{"claude"}, map[string]string{"prompt": "erstes wort"})
	e.svc.Update(e.ctx, e.a, id, Input{Title: "Alpha", ProviderIDs: []int64{e.prov["claude"]}, Fields: map[string]string{"prompt": "zweites wort"}})
	if e.search(e.a, "erstes").Total != 0 || e.search(e.a, "zweites").Total != 1 {
		t.Error("Index nach Update veraltet")
	}
	e.svc.Trash(e.ctx, e.a, id)
	if e.search(e.a, "zweites").Total != 0 {
		t.Error("Papierkorb-Prompt taucht in der Suche auf")
	}
	if r, _ := e.svc.List(e.ctx, e.a, ListOptions{Trash: true}); r.Total != 1 {
		t.Error("Prompt fehlt im Papierkorb")
	}
}

// TC-PRM-05: Papierkorb → Wiederherstellen → nach 30 Tagen endgültig entfernt.
func TestTrashLifecycleTC_PRM_05(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "Weg damit", []string{"claude"}, map[string]string{"prompt": "vergänglich"}, "temp")
	e.svc.Trash(e.ctx, e.a, id)
	if err := e.svc.Restore(e.ctx, e.a, id); err != nil {
		t.Fatal(err)
	}
	if e.search(e.a, "vergänglich").Total != 1 {
		t.Fatal("nach Wiederherstellen nicht auffindbar")
	}
	e.svc.Trash(e.ctx, e.a, id)
	real := store.Now
	defer func() { store.Now = real }()
	store.Now = func() time.Time { return real().Add(31 * 24 * time.Hour) }
	n, err := e.svc.PurgeExpired(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpired: %d, %v", n, err)
	}
	if _, err := e.svc.Get(e.ctx, e.a, id); !errors.Is(err, store.ErrNotFound) {
		t.Error("Prompt existiert noch")
	}
	var fts, tg int
	e.svc.DB.R.QueryRow("SELECT count(*) FROM prompt_fts").Scan(&fts)
	e.svc.DB.R.QueryRow("SELECT count(*) FROM tags").Scan(&tg)
	if fts != 0 || tg != 0 {
		t.Errorf("Reste: fts=%d tags=%d", fts, tg)
	}
}

// TC-PRM-06 + TC-PRM-14: Zähler gesamt und je Anbieter.
func TestUsageCountsTC_PRM_06_14(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "Zähler", []string{"suno", "udio"}, map[string]string{"style": "x"})
	e.svc.MarkUsed(e.ctx, e.a, id, e.prov["udio"])
	e.svc.MarkUsed(e.ctx, e.a, id, 0) // ohne Kontext → Hauptanbieter
	p, _ := e.svc.Get(e.ctx, e.a, id)
	suno, _ := p.Provider(e.prov["suno"])
	udio, _ := p.Provider(e.prov["udio"])
	if p.UseCount != 2 || suno.UseCount != 1 || udio.UseCount != 1 || p.LastUsedAt == 0 {
		t.Errorf("Zähler: gesamt %d, suno %d, udio %d", p.UseCount, suno.UseCount, udio.UseCount)
	}
}

func TestDuplicate(t *testing.T) {
	e := newEnv(t)
	id := e.create(e.a, "Doppelt", []string{"claude"}, map[string]string{"prompt": "Gleicher   Text"})
	dup, err := e.svc.Duplicate(e.ctx, e.a, "doppelt", map[string]string{"prompt": "gleicher text"}, 0)
	if err != nil || dup != id {
		t.Errorf("TC-PRM-09: Duplikat nicht erkannt (%d, %v)", dup, err)
	}
	if dup, _ := e.svc.Duplicate(e.ctx, e.b, "doppelt", map[string]string{"prompt": "gleicher text"}, 0); dup != 0 {
		t.Error("Duplikat-Suche darf fremde Prompts nicht finden")
	}
}

// TC-SRC-12: Suche bei 10.000 Prompts. Ausführen mit: go test -bench Search -run XXX ./internal/prompts
func BenchmarkSearch10k(b *testing.B) {
	e := newEnv(b)
	words := []string{"dark", "synthwave", "lofi", "portrait", "cinematic", "review", "code", "neon", "rain", "jazz", "ambient", "drums", "vocals", "golang", "summary"}
	provs := []string{"suno", "claude", "midjourney", "udio", "chatgpt"}
	for i := 0; i < 10_000; i++ {
		w := func(k int) string { return words[(i*7+k*13)%len(words)] }
		prov := provs[i%len(provs)]
		field := "prompt"
		if prov == "suno" || prov == "udio" {
			field = "style"
		}
		text := fmt.Sprintf("%s %s %s with %s and %s, variant %d", w(1), w(2), w(3), w(4), w(5), i)
		if _, err := e.svc.Create(e.ctx, e.a, Input{Title: fmt.Sprintf("%s %s #%d", w(6), w(7), i), ProviderIDs: []int64{e.prov[prov]},
			Fields: map[string]string{field: text}, Tags: []string{w(8)}}); err != nil {
			b.Fatal(err)
		}
	}
	queries := []string{"dark", "synth neon", "@suno rain", "#jazz cinematic", `"with neon"`, "-code portrait"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.svc.List(e.ctx, e.a, ListOptions{Query: search.Parse(queries[i%len(queries)])}); err != nil {
			b.Fatal(err)
		}
	}
}
