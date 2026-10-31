package autotag

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
)

type env struct {
	ctx  context.Context
	db   *store.DB
	ps   *prompts.Service
	svc  *Service
	a, b store.Scope
	prov map[string]int64
}

func newEnv(t testing.TB) *env {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	providers.Seed(ctx, db)
	db.W.Exec("INSERT INTO users(username, role, created_at) VALUES ('anna','member',0), ('ben','member',0)")
	ps := &prompts.Service{DB: db}
	e := &env{ctx: ctx, db: db, ps: ps, svc: New(db, ps, nil), a: store.Scope{UserID: 1}, b: store.Scope{UserID: 2}, prov: map[string]int64{}}
	all, _ := providers.List(ctx, db, e.a)
	for _, p := range all {
		e.prov[p.Slug] = p.ID
	}
	return e
}

func (e *env) add(t testing.TB, sc store.Scope, prov, field, text string, tg ...string) int64 {
	id, err := e.ps.Create(e.ctx, sc, prompts.Input{Title: text[:min(20, len(text))], ProviderIDs: []int64{e.prov[prov]}, Fields: map[string]string{field: text}, Tags: tg})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func sources(s []Suggestion) map[string]Suggestion {
	m := map[string]Suggestion{}
	for _, x := range s {
		m[x.Slug] = x
	}
	return m
}

// Korpus aus Musik- und Text-Prompts mit typischem Vokabular je Tag.
var vocab = map[string][]string{
	"synthwave": {"neon", "retro", "arpeggio", "gated", "outrun", "chrome", "nightdrive", "vhs"},
	"lo-fi":     {"vinyl", "crackle", "mellow", "dusty", "cozy", "tape", "rainy", "jazzy"},
	"metal":     {"distorted", "riffs", "growl", "breakdown", "heavy", "doublebass", "shred", "aggressive"},
	"klassik":   {"sonata", "violin", "cello", "baroque", "adagio", "chamber", "harpsichord", "orchestra"},
}

func (e *env) corpus(t testing.TB, sc store.Scope, n int, prefix string) {
	var names []string
	for k := range vocab {
		names = append(names, k)
	}
	for i := 0; i < n; i++ {
		g := names[i%len(names)]
		v := vocab[g]
		text := fmt.Sprintf("%s %s %s %s %s track %d", v[i%8], v[(i+3)%8], v[(i+5)%8], v[(i+1)%8], prefix, i)
		e.add(t, sc, "suno", "style", text, prefix+g)
	}
}

// TC-ML-01: Ohne Bestand nur Wörterbuch-Vorschläge, kein Fehler.
func TestColdStartTC_ML_01(t *testing.T) {
	e := newEnv(t)
	got, err := e.svc.SuggestTags(e.ctx, e.a, Input{Title: "Night", Fields: map[string]string{"style": "dark synthwave, 98 bpm"}, ProviderIDs: []int64{e.prov["suno"]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("keine Vorschläge")
	}
	for _, s := range got {
		if s.Source != "wörterbuch" || s.Reason == "" {
			t.Errorf("unerwartete Quelle/Begründung: %+v", s)
		}
	}
	m := sources(got)
	if _, ok := m["tempo-mittel"]; !ok {
		t.Errorf("TC-TAG-04 tempo-mittel fehlt: %v", got)
	}
	// Was im Exclude-Feld steht, ist unerwünscht und darf nicht vorgeschlagen werden.
	got = must(e.svc.SuggestTags(e.ctx, e.a, Input{Fields: map[string]string{"style": "synthwave", "exclude": "country, metal"}, ProviderIDs: []int64{e.prov["suno"]}}))
	if m := sources(got); m["country"].Slug != "" || m["metal"].Slug != "" || m["synthwave"].Slug == "" {
		t.Errorf("Exclude-Feld beeinflusst Vorschläge: %v", got)
	}
}

// TC-TAG-03: Regel schlägt vor; mit „automatisch übernehmen" wird sie angewendet.
func TestRulesTC_TAG_03(t *testing.T) {
	e := newEnv(t)
	e.ps.AddRule(e.ctx, e.a, tags.Rule{Kind: "regex", Pattern: `synthwave|retrowave`, Tag: "#Synthwave"})
	e.ps.AddRule(e.ctx, e.a, tags.Rule{Kind: "phrase", Pattern: "night drive", Tag: "nachtfahrt", AutoApply: true})
	in := Input{Fields: map[string]string{"style": "retrowave for a night drive"}, ProviderIDs: []int64{e.prov["suno"]}}
	m := sources(must(e.svc.SuggestTags(e.ctx, e.a, in)))
	if m["synthwave"].Source != "regel" || m["nachtfahrt"].Source != "regel" {
		t.Fatalf("Regeln: %+v", m)
	}
	if got := e.svc.AutoApply(e.ctx, e.a, in); len(got) != 1 || got[0] != "nachtfahrt" {
		t.Fatalf("AutoApply: %v", got)
	}
	if got := e.svc.AutoApply(e.ctx, e.b, in); len(got) != 0 {
		t.Fatal("Regeln von A gelten bei B")
	}
}

// TC-TAG-05: Ähnlichkeitsvorschläge nur aus eigenen Prompts. TC-ML-09: mit Begründung.
func TestSimilarOwnOnlyTC_TAG_05(t *testing.T) {
	e := newEnv(t)
	e.corpus(t, e.a, 24, "a-")
	e.corpus(t, e.b, 24, "b-")
	got := must(e.svc.SuggestTags(e.ctx, e.a, Input{Fields: map[string]string{"style": "neon arpeggio outrun chrome vhs"}, ProviderIDs: []int64{e.prov["suno"]}}))
	m := sources(got)
	if m["a-synthwave"].Source != "ähnlich" || !strings.HasPrefix(m["a-synthwave"].Reason, "ähnlich zu „") {
		t.Fatalf("Ähnlichkeit: %+v", got)
	}
	for slug := range m {
		if strings.HasPrefix(slug, "b-") {
			t.Fatalf("fremder Tag vorgeschlagen: %s", slug)
		}
	}
}

// TC-ML-03: abgelehnter Vorschlag erscheint für diesen Prompt nicht wieder; häufige Ablehnung wertet ab.
func TestRejectedTC_ML_03(t *testing.T) {
	e := newEnv(t)
	id := e.add(t, e.a, "suno", "style", "dark synthwave 98 bpm")
	in := Input{PromptID: id, Fields: map[string]string{"style": "dark synthwave 98 bpm"}, ProviderIDs: []int64{e.prov["suno"]}}
	if _, ok := sources(must(e.svc.SuggestTags(e.ctx, e.a, in)))["dark"]; !ok {
		t.Fatal("Vorbedingung: dark wird vorgeschlagen")
	}
	if err := e.svc.RecordFeedback(e.ctx, e.a, id, "tag", "dark", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := sources(must(e.svc.SuggestTags(e.ctx, e.a, in)))["dark"]; ok {
		t.Fatal("abgelehnter Tag wird erneut vorgeschlagen")
	}
	if err := e.svc.RecordFeedback(e.ctx, e.b, id, "tag", "dark", false); err == nil {
		t.Fatal("Feedback für fremden Prompt möglich")
	}
}

// TC-ML-04 + TC-ML-02 (Integration) + TC-ML-05: Modelle je Benutzer, Training gemessen.
func TestTrainingPerUserTC_ML_04(t *testing.T) {
	e := newEnv(t)
	e.corpus(t, e.a, 80, "a-")
	e.corpus(t, e.b, 80, "b-")
	if n, _ := e.svc.TrainDirty(e.ctx); n != 0 {
		t.Fatalf("ohne eingeschaltete KI-Hilfe darf nicht trainiert werden (%d)", n)
	}
	e.db.W.Exec(`UPDATE users SET settings = '{"ml":true}'`)
	n, err := e.svc.TrainDirty(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("TrainDirty: %d %v", n, err)
	}
	if n, _ := e.svc.TrainDirty(e.ctx); n != 0 {
		t.Fatalf("ohne Änderung erneut trainiert: %d", n)
	}
	st, _ := e.svc.Status(e.ctx, e.a)
	if len(st.Models) == 0 || st.Models[0].Kind != "tags" || !st.Models[0].Active {
		t.Fatalf("Status: %+v", st)
	}
	got := must(e.svc.SuggestTags(e.ctx, e.a, Input{Fields: map[string]string{"style": "violin cello adagio sonata"}, ProviderIDs: []int64{e.prov["suno"]}}))
	m := sources(got)
	if _, ok := m["a-klassik"]; !ok {
		t.Fatalf("gelerntes Tag fehlt: %+v", got)
	}
	for slug := range m {
		if strings.HasPrefix(slug, "b-") {
			t.Fatalf("Tag von B bei A: %s", slug)
		}
	}
	var models int
	e.db.R.QueryRow("SELECT count(*) FROM ml_models WHERE kind = 'tags'").Scan(&models)
	if models != 2 {
		t.Fatalf("Modelle getrennt gespeichert erwartet, got %d", models)
	}
}

// TC-ML-08: Bei schlechter Qualität keine Klassifikator-Vorschläge.
func TestLowQualityTC_ML_08(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 60; i++ { // zufällige Zuordnung: Text sagt nichts über den Tag
		e.add(t, e.a, "suno", "style", fmt.Sprintf("generic words alpha beta gamma %d", i), fmt.Sprintf("zufall-%d", i%6))
	}
	if err := e.svc.Train(e.ctx, 1); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.Status(e.ctx, e.a)
	if len(st.Models) > 0 && st.Models[0].Kind == "tags" && st.Models[0].Active {
		t.Fatalf("Qualität zu hoch für Zufallsdaten: %+v", st.Models[0])
	}
	for _, s := range must(e.svc.SuggestTags(e.ctx, e.a, Input{Fields: map[string]string{"style": "generic words alpha"}})) {
		if s.Source == "gelernt" {
			t.Fatalf("Klassifikator-Vorschlag trotz schlechter Qualität: %+v", s)
		}
	}
}

// TC-ML-06: Anbieter-Vorschlag aus Parametern und Quell-URL.
func TestProviderSuggestionsTC_ML_06(t *testing.T) {
	e := newEnv(t)
	got := must(e.svc.SuggestProviders(e.ctx, e.a, "cinematic portrait --ar 16:9 --style raw", "", nil, true))
	if len(got) == 0 || got[0].Slug != "midjourney" {
		t.Fatalf("Midjourney: %+v", got)
	}
	got = must(e.svc.SuggestProviders(e.ctx, e.a, "[Verse]\nneon lights", "https://suno.com/create", nil, false))
	if len(got) == 0 || got[0].Slug != "suno" || !strings.Contains(got[0].Reason, "suno.com") {
		t.Fatalf("Suno: %+v", got)
	}
	if got := must(e.svc.SuggestProviders(e.ctx, e.a, "--ar 1:1", "", []int64{e.prov["midjourney"]}, true)); len(got) != 0 {
		t.Fatalf("bereits gewählter Anbieter vorgeschlagen: %+v", got)
	}
}

// TC-ML-05: Training mit 10.000 Prompts (Laufzeit und Speicher). Nicht mit -short.
func TestTraining10kTC_ML_05(t *testing.T) {
	if testing.Short() {
		t.Skip("lang")
	}
	e := newEnv(t)
	e.corpus(t, e.a, 10000, "")
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	if err := e.svc.Train(e.ctx, 1); err != nil {
		t.Fatal(err)
	}
	dur := time.Since(start)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	var size int
	e.db.R.QueryRow("SELECT length(data) FROM ml_models WHERE owner_id = 1 AND kind = 'tags'").Scan(&size)
	t.Logf("Training 10.000 Prompts: %v, Allokationen %.0f MB gesamt, Modell %d KB", dur, float64(after.TotalAlloc-before.TotalAlloc)/1e6, size/1024)
	if dur > 60*time.Second {
		t.Fatalf("Training zu langsam: %v", dur)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
