// Package autotag führt die drei Stufen der KI-Hilfe zusammen (Concept 8.2):
// 1. Regeln und Wörterbücher, 2. ähnliche eigene Prompts (k-nächste Nachbarn), 3. gelernter Klassifikator.
// Alles läuft lokal; es gibt keine Verbindung zu externen Diensten.
package autotag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Buffer379x/smmyp/internal/ml"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
)

const (
	MinKNN       = 20  // getaggte Prompts, ab denen Stufe 2 greift
	MinModel     = 50  // Beispiele, ab denen ein Klassifikator trainiert wird
	MinPerClass  = 5   // Beispiele je Tag/Anbieter
	QualityMin   = 0.5 // Trefferquote@3, ab der Klassifikator-Vorschläge gezeigt werden
	modelVersion = 1
	maxSuggest   = 8
	cacheIdle    = 10 * time.Minute
)

type Suggestion struct {
	Slug   string  `json:"slug"`
	Source string  `json:"source"` // regel | wörterbuch | ähnlich | gelernt
	Reason string  `json:"reason"`
	Score  float64 `json:"score"`
}

type ProviderSuggestion struct {
	ID     int64  `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type Input struct {
	PromptID    int64
	Title       string
	Fields      map[string]string
	ProviderIDs []int64
	Tags        []string
	SourceURL   string
}

// NegativeFields beschreiben, was ausdrücklich NICHT gewünscht ist (Suno „Exclude Styles", Negativ-Prompt).
// Sie dürfen weder Vorschläge noch das Training beeinflussen, sonst wird z. B. „country" vorgeschlagen.
var NegativeFields = map[string]bool{"exclude": true, "negative": true}

func (in Input) text() string {
	var b strings.Builder
	b.WriteString(in.Title)
	keys := make([]string, 0, len(in.Fields))
	for k := range in.Fields {
		if !NegativeFields[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\n")
		b.WriteString(in.Fields[k])
	}
	return b.String()
}

type cached struct {
	model *ml.CNB
	used  time.Time
}

type Service struct {
	DB      *store.DB
	Prompts *prompts.Service
	Log     *slog.Logger

	// Allowed: Instanz-Schalter (SMMYP_ML). Ob ein Benutzer die KI-Hilfe nutzt, steht in seinen Einstellungen.
	Allowed bool

	mu        sync.Mutex
	cache     map[string]*cached
	attempted map[int64]int64 // Stand (max updated_at) beim letzten Training je Benutzer
}

func New(db *store.DB, ps *prompts.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{DB: db, Prompts: ps, Log: log, Allowed: true, cache: map[string]*cached{}, attempted: map[int64]int64{}}
}

// ---------- Vorschläge ----------

// SuggestTags liefert Tag-Vorschläge mit Begründung, ohne bereits gesetzte oder für diesen Prompt abgelehnte Tags.
func (s *Service) SuggestTags(ctx context.Context, sc store.Scope, in Input) ([]Suggestion, error) {
	text := in.text()
	all, err := providers.List(ctx, s.DB, sc)
	if err != nil {
		return nil, err
	}
	var cats []string
	for _, p := range providers.Visible(all, in.ProviderIDs) {
		cats = append(cats, p.Category)
	}
	best := map[string]Suggestion{}
	add := func(slug, source, reason string, score float64) {
		slug = tags.Normalize(slug)
		if slug == "" {
			return
		}
		if cur, ok := best[slug]; !ok || score > cur.Score {
			best[slug] = Suggestion{Slug: slug, Source: source, Reason: reason, Score: score}
		}
	}

	// Stufe 1a: eigene Regeln
	rules, err := tags.Rules(ctx, s.DB.R, sc.UserID)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if ruleMatches(r, text, cats) {
			add(r.Tag, "regel", "Regel: "+r.Pattern, 1.0)
		}
	}
	// Stufe 1b: Wörterbücher der gewählten Kategorien
	for _, h := range ml.MatchDictionary(text, cats) {
		add(h.Tag, "wörterbuch", "wegen „"+h.Because+"“", 0.8)
	}
	if t := ml.AspectTag(text); t != "" {
		add(t, "wörterbuch", "wegen --ar", 0.8)
	}

	// Stufe 2: ähnliche eigene Prompts
	var tagged int
	s.DB.R.QueryRowContext(ctx, `SELECT count(DISTINCT p.id) FROM prompts p JOIN prompt_tags pt ON pt.prompt_id = p.id
		WHERE p.owner_id = ? AND p.deleted_at IS NULL`, sc.UserID).Scan(&tagged)
	if tagged >= MinKNN {
		nb, err := s.Prompts.Neighbors(ctx, sc, text, in.PromptID, 10)
		if err != nil {
			return nil, err
		}
		total := 0.0
		weight := map[string]float64{}
		example := map[string]string{}
		for _, n := range nb {
			w := 1 / float64(n.Rank+1)
			total += w
			for _, t := range n.Tags {
				weight[t] += w
				if _, ok := example[t]; !ok {
					example[t] = n.Title
				}
			}
		}
		for t, w := range weight {
			if frac := w / total; total > 0 && frac >= 0.25 {
				add(t, "ähnlich", "ähnlich zu „"+example[t]+"“", 0.3+0.6*frac)
			}
		}
	}

	// Stufe 3: Klassifikator
	if m := s.model(ctx, sc.UserID, "tags"); m != nil && m.Quality >= QualityMin {
		f := ml.Features(text)
		for i, p := range m.Predict(f) {
			if i >= 5 || p.Z < 1.0 {
				break
			}
			add(p.Label, "gelernt", fmt.Sprintf("aus deinen Tags gelernt (Trefferquote %.0f %%)", m.Quality*100), p.Score)
		}
	}

	// Aliase auflösen, Gesetztes und Abgelehntes entfernen
	have := map[string]bool{}
	resolvedHave, _ := tags.Resolve(ctx, s.DB.R, sc.UserID, in.Tags)
	for _, t := range resolvedHave {
		have[t] = true
	}
	rejected, penalty, err := s.feedback(ctx, sc, in.PromptID)
	if err != nil {
		return nil, err
	}
	var out []Suggestion
	for _, sg := range best {
		r, _ := tags.Resolve(ctx, s.DB.R, sc.UserID, []string{sg.Slug})
		if len(r) == 1 {
			sg.Slug = r[0]
		}
		if have[sg.Slug] || rejected[sg.Slug] {
			continue
		}
		if penalty[sg.Slug] {
			sg.Score *= 0.5
		}
		dup := false
		for i := range out {
			if out[i].Slug == sg.Slug {
				dup = true
				if sg.Score > out[i].Score {
					out[i] = sg
				}
			}
		}
		if !dup {
			out = append(out, sg)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score || (out[i].Score == out[j].Score && out[i].Slug < out[j].Slug)
	})
	if len(out) > maxSuggest {
		out = out[:maxSuggest]
	}
	return out, nil
}

func ruleMatches(r tags.Rule, text string, cats []string) bool {
	if r.Category != "" {
		ok := false
		for _, c := range cats {
			ok = ok || c == r.Category
		}
		if !ok {
			return false
		}
	}
	switch r.Kind {
	case "word":
		w := ml.Fold(strings.TrimSpace(r.Pattern))
		for _, x := range ml.Words(text) {
			if x == w {
				return true
			}
		}
	case "phrase":
		return strings.Contains(" "+strings.Join(strings.Fields(ml.Fold(text)), " ")+" ", strings.Join(strings.Fields(ml.Fold(r.Pattern)), " "))
	case "regex":
		re, err := regexp.Compile("(?i)" + r.Pattern)
		return err == nil && re.MatchString(text)
	}
	return false
}

// AutoApply liefert Tags aus Regeln mit „automatisch übernehmen".
func (s *Service) AutoApply(ctx context.Context, sc store.Scope, in Input) []string {
	rules, err := tags.Rules(ctx, s.DB.R, sc.UserID)
	if err != nil {
		return nil
	}
	all, _ := providers.List(ctx, s.DB, sc)
	var cats []string
	for _, p := range providers.Visible(all, in.ProviderIDs) {
		cats = append(cats, p.Category)
	}
	var out []string
	text := in.text()
	for _, r := range rules {
		if r.AutoApply && ruleMatches(r, text, cats) {
			out = append(out, r.Tag)
		}
	}
	return out
}

// feedback: für diesen Prompt abgelehnte Tags und Tags, die der Benutzer häufig ablehnt.
func (s *Service) feedback(ctx context.Context, sc store.Scope, promptID int64) (map[string]bool, map[string]bool, error) {
	rejected, penalty := map[string]bool{}, map[string]bool{}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT value, prompt_id, accepted FROM suggestion_feedback WHERE owner_id = ? AND kind = 'tag'`, sc.UserID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	acc, rej := map[string]int{}, map[string]int{}
	for rows.Next() {
		var v string
		var pid int64
		var a bool
		if err := rows.Scan(&v, &pid, &a); err != nil {
			return nil, nil, err
		}
		if a {
			acc[v]++
		} else {
			rej[v]++
			if promptID > 0 && pid == promptID {
				rejected[v] = true
			}
		}
	}
	for v, n := range rej {
		if n >= 3 && n > acc[v] {
			penalty[v] = true
		}
	}
	return rejected, penalty, rows.Err()
}

// RecordFeedback speichert Übernahme oder Ablehnung eines Vorschlags (Lernsignal, TC-ML-03).
func (s *Service) RecordFeedback(ctx context.Context, sc store.Scope, promptID int64, kind, value string, accepted bool) error {
	if kind != "tag" && kind != "provider" {
		return fmt.Errorf("unbekannte Art %q", kind)
	}
	var n int
	s.DB.R.QueryRowContext(ctx, "SELECT count(*) FROM prompts WHERE id = ? AND owner_id = ?", promptID, sc.UserID).Scan(&n)
	if n == 0 {
		return store.ErrNotFound
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO suggestion_feedback(owner_id, prompt_id, kind, value, accepted, created_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(owner_id, prompt_id, kind, value) DO UPDATE SET accepted = excluded.accepted, created_at = excluded.created_at`,
			sc.UserID, promptID, kind, value, accepted, store.Now().Unix())
		return err
	})
}

// ---------- Anbieter-Vorschläge ----------

var providerHints = []struct {
	re     *regexp.Regexp
	slug   string
	reason string
}{
	{regexp.MustCompile(`(?i)--(ar|v|style|stylize|chaos|niji|sref|cref|weird|tile)\b`), "midjourney", "Midjourney-Parameter"},
	{regexp.MustCompile(`(?i)\[(verse|chorus|bridge|intro|outro|pre-chorus|hook|instrumental)[^\]]*\]`), "suno", "Song-Struktur in [ ]"},
	{regexp.MustCompile(`(?i)negative prompt|\(\(.+?\)\)|<lora:`), "stable-diffusion", "Stable-Diffusion-Syntax"},
	{regexp.MustCompile(`(?i)\b(bpm|tempo|vocals|chorus|verse)\b`), "suno", "Musik-Begriffe"},
}

var domains = map[string]string{
	"suno.com": "suno", "udio.com": "udio", "claude.ai": "claude", "chatgpt.com": "chatgpt", "chat.openai.com": "chatgpt",
	"midjourney.com": "midjourney", "gemini.google.com": "gemini", "perplexity.ai": "perplexity", "chat.mistral.ai": "le-chat",
	"copilot.microsoft.com": "copilot", "github.com": "github-copilot", "cursor.com": "cursor", "ideogram.ai": "ideogram",
	"runwayml.com": "runway", "app.runwayml.com": "runway", "sora.com": "sora", "sora.chatgpt.com": "sora", "elevenlabs.io": "elevenlabs",
	"klingai.com": "kling",
}

// SuggestProviders schlägt bis zu drei passende Anbieter vor (Heuristik, Quell-URL, gelerntes Modell).
// useModel=false nutzt nur feste Regeln (Quell-Domain, Parameter), kein gelerntes Modell.
func (s *Service) SuggestProviders(ctx context.Context, sc store.Scope, text, sourceURL string, selected []int64, useModel bool) ([]ProviderSuggestion, error) {
	all, err := providers.List(ctx, s.DB, sc)
	if err != nil {
		return nil, err
	}
	sel := map[int64]bool{}
	for _, id := range selected {
		sel[id] = true
	}
	var out []ProviderSuggestion
	seen := map[string]bool{}
	add := func(slug, reason string) {
		if seen[slug] || len(out) >= 3 {
			return
		}
		p, ok := providers.BySlug(all, slug)
		if !ok || sel[p.ID] {
			return
		}
		seen[slug] = true
		out = append(out, ProviderSuggestion{ID: p.ID, Slug: p.Slug, Name: p.Name, Reason: reason})
	}
	if u, err := url.Parse(sourceURL); err == nil && u.Host != "" {
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if slug, ok := domains[host]; ok {
			add(slug, "kopiert von "+host)
		}
	}
	for _, h := range providerHints {
		if h.re.MatchString(text) {
			add(h.slug, h.reason)
		}
	}
	if m := s.model(ctx, sc.UserID, "providers"); useModel && m != nil && m.Quality >= QualityMin {
		for i, p := range m.Predict(ml.Features(text)) {
			if i >= 2 || p.Z < 1.0 {
				break
			}
			add(p.Label, "ähnlich wie deine anderen Prompts")
		}
	}
	return out, nil
}

// ---------- Training ----------

type trainingRow struct {
	text      string
	tags      []string
	providers []string
}

func (s *Service) trainingData(ctx context.Context, owner int64) ([]trainingRow, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT p.id, p.title, p.fields,
		ifnull((SELECT group_concat(t.slug, ' ') FROM prompt_tags pt JOIN tags t ON t.id = pt.tag_id WHERE pt.prompt_id = p.id), ''),
		ifnull((SELECT group_concat(pv.slug, ' ') FROM prompt_providers pp JOIN providers pv ON pv.id = pp.provider_id WHERE pp.prompt_id = p.id), '')
		FROM prompts p WHERE p.owner_id = ? AND p.deleted_at IS NULL ORDER BY p.id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trainingRow
	for rows.Next() {
		var id int64
		var title, fields, tg, pv string
		if err := rows.Scan(&id, &title, &fields, &tg, &pv); err != nil {
			return nil, err
		}
		var f map[string]string
		json.Unmarshal([]byte(fields), &f)
		vals := make([]string, 0, len(f))
		for k, v := range f {
			if !NegativeFields[k] {
				vals = append(vals, v)
			}
		}
		sort.Strings(vals)
		out = append(out, trainingRow{text: title + "\n" + strings.Join(vals, "\n"), tags: strings.Fields(tg), providers: strings.Fields(pv)})
	}
	return out, rows.Err()
}

// Train trainiert Tag- und Anbieter-Modell eines Benutzers neu (oder entfernt sie bei zu wenig Daten).
func (s *Service) Train(ctx context.Context, owner int64) error {
	data, err := s.trainingData(ctx, owner)
	if err != nil {
		return err
	}
	var tagDocs, provDocs []ml.Doc
	for _, r := range data {
		f := ml.Features(r.text)
		if len(r.tags) > 0 {
			tagDocs = append(tagDocs, ml.Doc{Features: f, Labels: r.tags})
		}
		if len(r.providers) > 0 {
			provDocs = append(provDocs, ml.Doc{Features: f, Labels: r.providers})
		}
	}
	for kind, docs := range map[string][]ml.Doc{"tags": tagDocs, "providers": provDocs} {
		if err := s.trainOne(ctx, owner, kind, docs); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) trainOne(ctx context.Context, owner int64, kind string, docs []ml.Doc) error {
	key := fmt.Sprintf("%d/%s", owner, kind)
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
	var m *ml.CNB
	if len(docs) >= MinModel {
		m = ml.TrainCNB(docs, MinPerClass)
		if len(m.Classes) < 2 {
			m = nil
		} else {
			m.Quality = ml.CrossValidate(docs, MinPerClass, 5)
		}
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if m == nil {
			_, err := tx.ExecContext(ctx, "DELETE FROM ml_models WHERE owner_id = ? AND kind = ?", owner, kind)
			return err
		}
		data, err := m.Encode()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ml_models(owner_id, kind, version, trained_at, samples, quality, data) VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(owner_id, kind) DO UPDATE SET version = excluded.version, trained_at = excluded.trained_at,
			samples = excluded.samples, quality = excluded.quality, data = excluded.data`,
			owner, kind, modelVersion, store.Now().Unix(), m.Samples, m.Quality, data)
		return err
	})
}

// TrainDirty trainiert Benutzer neu, deren Prompts sich seit dem letzten Training geändert haben.
func (s *Service) TrainDirty(ctx context.Context) (int, error) {
	// Stand je Benutzer: jüngste Änderung, Anzahl und gelöschte (damit auch Löschen neu trainiert).
	if !s.Allowed {
		return 0, nil
	}
	// Nur Benutzer mit eingeschalteter KI-Hilfe (Einstellung „ml").
	rows, err := s.DB.R.QueryContext(ctx, `SELECT p.owner_id, max(p.updated_at) + count(*) * 1000003 + ifnull(max(p.deleted_at), 0), count(*)
		FROM prompts p JOIN users u ON u.id = p.owner_id
		WHERE json_extract(u.settings, '$.ml') = 1 AND u.disabled_at IS NULL GROUP BY p.owner_id`)
	if err != nil {
		return 0, err
	}
	var owners []int64
	marks := map[int64]int64{}
	for rows.Next() {
		var owner, mark, count int64
		if err := rows.Scan(&owner, &mark, &count); err != nil {
			rows.Close()
			return 0, err
		}
		s.mu.Lock()
		last, seen := s.attempted[owner]
		s.mu.Unlock()
		if count >= MinModel && (!seen || mark != last) {
			owners = append(owners, owner)
			marks[owner] = mark
		}
	}
	rows.Close()
	for _, o := range owners {
		s.mu.Lock()
		s.attempted[o] = marks[o]
		s.mu.Unlock()
		start := time.Now()
		if err := s.Train(ctx, o); err != nil {
			return 0, err
		}
		s.Log.Info("ML-Modelle trainiert", "user_id", o, "ms", time.Since(start).Milliseconds())
	}
	return len(owners), nil
}

// Forget löscht die Modelle eines Benutzers (beim Ausschalten der KI-Hilfe). Feedback bleibt erhalten.
func (s *Service) Forget(ctx context.Context, owner int64) error {
	s.mu.Lock()
	for _, k := range []string{"tags", "providers"} {
		delete(s.cache, fmt.Sprintf("%d/%s", owner, k))
	}
	delete(s.attempted, owner)
	s.mu.Unlock()
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM ml_models WHERE owner_id = ?", owner)
		return err
	})
}

// model lädt ein Modell (mit Cache); nil, wenn keins existiert.
func (s *Service) model(ctx context.Context, owner int64, kind string) *ml.CNB {
	key := fmt.Sprintf("%d/%s", owner, kind)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[key]; ok {
		c.used = time.Now()
		return c.model
	}
	var data []byte
	var version int
	err := s.DB.R.QueryRowContext(ctx, "SELECT version, data FROM ml_models WHERE owner_id = ? AND kind = ?", owner, kind).Scan(&version, &data)
	var m *ml.CNB
	if err == nil && version == modelVersion {
		if m, err = ml.DecodeCNB(data); err != nil {
			s.Log.Warn("ML-Modell nicht lesbar", "user_id", owner, "kind", kind, "err", err)
			m = nil
		}
	}
	s.cache[key] = &cached{model: m, used: time.Now()}
	return m
}

// Evict entfernt Modelle, die länger als cacheIdle nicht benutzt wurden (RAM-Budget).
func (s *Service) Evict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.cache {
		if time.Since(c.used) > cacheIdle {
			delete(s.cache, k)
		}
	}
}

// Status für die Anzeige in den Einstellungen.
type ModelStatus struct {
	Kind      string
	Samples   int
	Quality   float64
	TrainedAt int64
	Active    bool
}

type Status struct {
	Tagged    int
	KNNActive bool
	Models    []ModelStatus
}

func (s *Service) Status(ctx context.Context, sc store.Scope) (Status, error) {
	var st Status
	s.DB.R.QueryRowContext(ctx, `SELECT count(DISTINCT p.id) FROM prompts p JOIN prompt_tags pt ON pt.prompt_id = p.id
		WHERE p.owner_id = ? AND p.deleted_at IS NULL`, sc.UserID).Scan(&st.Tagged)
	st.KNNActive = st.Tagged >= MinKNN
	rows, err := s.DB.R.QueryContext(ctx, "SELECT kind, samples, ifnull(quality,0), trained_at FROM ml_models WHERE owner_id = ? ORDER BY kind DESC", sc.UserID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var m ModelStatus
		if err := rows.Scan(&m.Kind, &m.Samples, &m.Quality, &m.TrainedAt); err != nil {
			return st, err
		}
		m.Active = m.Quality >= QualityMin
		st.Models = append(st.Models, m)
	}
	return st, rows.Err()
}
