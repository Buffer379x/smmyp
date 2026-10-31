// Package providers verwaltet KI-Anbieter, ihren Feldkatalog und die Startliste.
package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Buffer379x/smmyp/internal/store"
)

// Field beschreibt ein Eingabefeld eines Anbieters. Key stammt aus dem gemeinsamen Katalog.
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Long  bool   `json:"long,omitempty"`
}

// Catalog: gemeinsame Feldschlüssel, damit Prompts mit mehreren Anbietern Felder teilen (Feld-Union).
var Catalog = []Field{
	{Key: "prompt", Label: "Prompt", Long: true},
	{Key: "system", Label: "System / Anweisung", Long: true},
	{Key: "style", Label: "Style"},
	{Key: "lyrics", Label: "Lyrics", Long: true},
	{Key: "exclude", Label: "Exclude"},
	{Key: "negative", Label: "Negativ-Prompt"},
	{Key: "params", Label: "Parameter"},
}

func catalogField(key string) (Field, bool) {
	for _, f := range Catalog {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

type Category struct{ Key, Label string }

var Categories = []Category{
	{"text", "Text"}, {"code", "Code"}, {"bild", "Bild"},
	{"musik", "Musik"}, {"video", "Video"}, {"audio", "Audio"},
}

func CategoryLabel(key string) string {
	for _, c := range Categories {
		if c.Key == key {
			return c.Label
		}
	}
	return key
}

func categoryIndex(key string) int {
	for i, c := range Categories {
		if c.Key == key {
			return i
		}
	}
	return len(Categories)
}

// Colors: Reiterfarben (Concept 11.3). Der Wert ist der Klassenname-Suffix im CSS (.t-<farbe>).
var Colors = []string{"ziegel", "moos", "petrol", "schiefer", "pflaume", "rose", "indigo", "ocker", "oliv", "senf"}

func validColor(c string) bool {
	for _, x := range Colors {
		if x == c {
			return true
		}
	}
	return false
}

type Provider struct {
	ID           int64
	OwnerID      int64 // 0 = global
	Slug         string
	Name         string
	Category     string
	Color        string
	Fields       []Field
	PrimaryField string
	Sort         int
}

func (p Provider) Global() bool { return p.OwnerID == 0 }

func (p Provider) HasField(key string) bool {
	for _, f := range p.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func (p Provider) FieldKeys() []string {
	keys := make([]string, len(p.Fields))
	for i, f := range p.Fields {
		keys[i] = f.Key
	}
	return keys
}

// Union liefert die Vereinigung der Felder (Reihenfolge: Hauptanbieter zuerst, Label vom ersten Anbieter).
func Union(ps []Provider) []Field {
	var out []Field
	seen := map[string]bool{}
	for _, p := range ps {
		for _, f := range p.Fields {
			if !seen[f.Key] {
				seen[f.Key] = true
				out = append(out, f)
			}
		}
	}
	return out
}

const cols = "id, ifnull(owner_id,0), slug, name, category, color, fields, primary_field, sort"

func scan(row interface{ Scan(...any) error }) (Provider, error) {
	var p Provider
	var fields string
	if err := row.Scan(&p.ID, &p.OwnerID, &p.Slug, &p.Name, &p.Category, &p.Color, &fields, &p.PrimaryField, &p.Sort); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(fields), &p.Fields); err != nil {
		return p, fmt.Errorf("Feldschema von %s: %w", p.Slug, err)
	}
	return p, nil
}

// List liefert alle für den Benutzer sichtbaren Anbieter (global + eigene), sortiert nach Kategorie.
func List(ctx context.Context, db *store.DB, s store.Scope) ([]Provider, error) {
	rows, err := db.R.QueryContext(ctx, "SELECT "+cols+" FROM providers WHERE owner_id IS NULL OR owner_id = ?", s.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	SortProviders(out)
	return out, rows.Err()
}

// SortProviders: Kategorie, dann Sortierwert, dann Name.
func SortProviders(ps []Provider) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if ci, cj := categoryIndex(a.Category), categoryIndex(b.Category); ci != cj {
			return ci < cj
		}
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

// Visible filtert eine ID-Liste auf sichtbare Anbieter und behält die Reihenfolge.
func Visible(all []Provider, ids []int64) []Provider {
	byID := map[int64]Provider{}
	for _, p := range all {
		byID[p.ID] = p
	}
	var out []Provider
	seen := map[int64]bool{}
	for _, id := range ids {
		if p, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, p)
		}
	}
	return out
}

// BySlug sucht einen sichtbaren Anbieter; eigene Anbieter haben Vorrang vor globalen.
func BySlug(all []Provider, slug string) (Provider, bool) {
	var found Provider
	ok := false
	for _, p := range all {
		if p.Slug == slug && (!ok || !p.Global()) {
			found, ok = p, true
		}
	}
	return found, ok
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify: „Claude Code" → „claude-code".
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(s)
	return strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
}

// Input für einen neuen Anbieter.
type Input struct {
	Name         string
	Slug         string
	Category     string
	Color        string
	FieldKeys    []string
	PrimaryField string
	Global       bool
}

type ValidationError map[string]string

func (v ValidationError) Error() string { return "Eingaben prüfen" }

// Create legt einen Anbieter an. Global geht nur für Admins.
func Create(ctx context.Context, db *store.DB, s store.Scope, in Input) (int64, error) {
	verr := ValidationError{}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 60 {
		verr["name"] = "Name fehlt oder ist länger als 60 Zeichen."
	}
	if in.Slug == "" {
		in.Slug = Slugify(in.Name)
	} else {
		in.Slug = Slugify(in.Slug)
	}
	if in.Slug == "" {
		verr["slug"] = "Kurzname fehlt."
	}
	if categoryIndex(in.Category) == len(Categories) {
		verr["category"] = "Kategorie wählen."
	}
	if !validColor(in.Color) {
		verr["color"] = "Reiterfarbe wählen."
	}
	var fields []Field
	for _, k := range in.FieldKeys {
		if f, ok := catalogField(k); ok {
			fields = append(fields, f)
		}
	}
	if len(fields) == 0 {
		verr["fields"] = "Mindestens ein Feld wählen."
	}
	primaryOK := false
	for _, f := range fields {
		if f.Key == in.PrimaryField {
			primaryOK = true
		}
	}
	if !primaryOK && len(fields) > 0 {
		in.PrimaryField = fields[0].Key
	}
	if in.Global && !s.Admin {
		verr["global"] = "Nur Admins dürfen Anbieter für alle anlegen."
	}
	if len(verr) > 0 {
		return 0, verr
	}
	fj, _ := json.Marshal(fields)
	var owner any = s.UserID
	if in.Global {
		owner = nil
	}
	var id int64
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO providers(owner_id, slug, name, category, color, fields, primary_field, sort)
			VALUES (?,?,?,?,?,?,?,100)`, owner, in.Slug, in.Name, in.Category, in.Color, string(fj), in.PrimaryField)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ValidationError{"slug": "Diesen Kurznamen gibt es schon."}
			}
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// Delete löscht einen eigenen (oder als Admin: globalen) Anbieter, wenn kein Prompt ihn nutzt.
func Delete(ctx context.Context, db *store.DB, s store.Scope, id int64) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		var owner sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT owner_id FROM providers WHERE id = ?", id).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		if (owner.Valid && owner.Int64 != s.UserID) || (!owner.Valid && !s.Admin) {
			return store.ErrNotFound
		}
		var used int
		tx.QueryRowContext(ctx, "SELECT count(*) FROM prompt_providers WHERE provider_id = ?", id).Scan(&used)
		if used > 0 {
			return ValidationError{"delete": fmt.Sprintf("Wird noch von %d Prompt(s) genutzt.", used)}
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM providers WHERE id = ?", id)
		return err
	})
}

type seed struct {
	slug, name, cat, color, primary string
	fields                          []string
}

var seeds = []seed{
	{"claude", "Claude", "text", "ziegel", "prompt", []string{"system", "prompt"}},
	{"chatgpt", "ChatGPT", "text", "moos", "prompt", []string{"system", "prompt"}},
	{"gemini", "Gemini", "text", "petrol", "prompt", []string{"system", "prompt"}},
	{"perplexity", "Perplexity", "text", "schiefer", "prompt", []string{"prompt"}},
	{"le-chat", "Mistral Le Chat", "text", "oliv", "prompt", []string{"system", "prompt"}},
	{"copilot", "Copilot", "text", "indigo", "prompt", []string{"prompt"}},
	{"lokal", "Lokale Modelle", "text", "senf", "prompt", []string{"system", "prompt"}},
	{"claude-code", "Claude Code", "code", "ziegel", "prompt", []string{"prompt"}},
	{"github-copilot", "GitHub Copilot", "code", "moos", "prompt", []string{"prompt"}},
	{"cursor", "Cursor", "code", "indigo", "prompt", []string{"prompt"}},
	{"midjourney", "Midjourney", "bild", "indigo", "prompt", []string{"prompt", "params"}},
	{"flux", "Flux", "bild", "ocker", "prompt", []string{"prompt"}},
	{"stable-diffusion", "Stable Diffusion", "bild", "oliv", "prompt", []string{"prompt", "negative", "params"}},
	{"chatgpt-images", "ChatGPT Images", "bild", "moos", "prompt", []string{"prompt"}},
	{"ideogram", "Ideogram", "bild", "rose", "prompt", []string{"prompt", "negative"}},
	{"suno", "Suno", "musik", "pflaume", "style", []string{"style", "lyrics", "exclude"}},
	{"udio", "Udio", "musik", "rose", "style", []string{"style", "lyrics"}},
	{"runway", "Runway", "video", "schiefer", "prompt", []string{"prompt"}},
	{"sora", "Sora", "video", "petrol", "prompt", []string{"prompt"}},
	{"veo", "Veo", "video", "senf", "prompt", []string{"prompt"}},
	{"kling", "Kling", "video", "ocker", "prompt", []string{"prompt"}},
	{"elevenlabs", "ElevenLabs", "audio", "senf", "prompt", []string{"prompt"}},
}

// labels für Felder, die beim jeweiligen Anbieter anders heißen.
var seedLabels = map[string]map[string]string{
	"suno":       {"style": "Style of Music", "exclude": "Exclude Styles"},
	"udio":       {"style": "Prompt / Style"},
	"midjourney": {"params": "Parameter"},
}

// Seed legt die Startliste an, wenn noch kein globaler Anbieter existiert.
func Seed(ctx context.Context, db *store.DB) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM providers WHERE owner_id IS NULL").Scan(&n); err != nil || n > 0 {
			return err
		}
		for i, sd := range seeds {
			var fields []Field
			for _, k := range sd.fields {
				f, _ := catalogField(k)
				if l, ok := seedLabels[sd.slug][k]; ok {
					f.Label = l
				}
				fields = append(fields, f)
			}
			fj, _ := json.Marshal(fields)
			if _, err := tx.ExecContext(ctx, `INSERT INTO providers(owner_id, slug, name, category, color, fields, primary_field, sort)
				VALUES (NULL,?,?,?,?,?,?,?)`, sd.slug, sd.name, sd.cat, sd.color, string(fj), sd.primary, i); err != nil {
				return err
			}
		}
		return nil
	})
}
