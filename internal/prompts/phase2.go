package prompts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Buffer379x/smmyp/internal/ml"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
)

// ---------- Versionen ----------

type Version struct {
	ID        int64
	Title     string
	Fields    map[string]string
	Notes     string
	CreatedAt int64
}

// Versions listet frühere Fassungen (neueste zuerst). Fremde Prompts → ErrNotFound.
func (s *Service) Versions(ctx context.Context, sc store.Scope, id int64) ([]Version, error) {
	if _, err := s.owned(ctx, sc, id); err != nil {
		return nil, err
	}
	rows, err := s.DB.R.QueryContext(ctx, "SELECT id, title, fields, notes, created_at FROM prompt_versions WHERE prompt_id = ? ORDER BY id DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var fields string
		if err := rows.Scan(&v.ID, &v.Title, &fields, &v.Notes, &v.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(fields), &v.Fields)
		out = append(out, v)
	}
	return out, rows.Err()
}

// owned prüft, dass der Prompt dem Benutzer gehört (auch im Papierkorb).
func (s *Service) owned(ctx context.Context, sc store.Scope, id int64) (bool, error) {
	var deleted sql.NullInt64
	err := s.DB.R.QueryRowContext(ctx, "SELECT deleted_at FROM prompts WHERE id = ? AND owner_id = ?", id, sc.UserID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, store.ErrNotFound
	}
	return deleted.Valid, err
}

// RestoreVersion macht eine frühere Fassung wieder aktuell; die aktuelle wird dabei selbst zur Version.
func (s *Service) RestoreVersion(ctx context.Context, sc store.Scope, id, versionID int64) error {
	p, err := s.Get(ctx, sc, id)
	if err != nil {
		return err
	}
	if p.DeletedAt != 0 {
		return store.ErrNotFound
	}
	var v Version
	var fields string
	err = s.DB.R.QueryRowContext(ctx, "SELECT title, fields, notes FROM prompt_versions WHERE id = ? AND prompt_id = ?", versionID, id).Scan(&v.Title, &fields, &v.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	json.Unmarshal([]byte(fields), &v.Fields)
	in := p.Input()
	in.Title, in.Fields, in.Notes = v.Title, v.Fields, v.Notes
	return s.Update(ctx, sc, id, in)
}

// Input baut aus einem gespeicherten Prompt die Eingabe für Update.
func (p Prompt) Input() Input {
	in := Input{Title: p.Title, Fields: p.Fields, Notes: p.Notes, Model: p.Model, Tags: p.Tags, Favorite: p.IsFavorite,
		Rating: p.Rating, SourceURL: p.SourceURL, VarsOff: p.VarsOff, Links: p.Links}
	for _, l := range p.Providers {
		in.ProviderIDs = append(in.ProviderIDs, l.ID)
	}
	return in
}

// ---------- Ähnlichkeit (k-nächste Nachbarn per FTS) ----------

type Neighbor struct {
	ID    int64
	Title string
	Tags  []string
	Rank  int
}

// significant wählt die markantesten Wörter (lang, eindeutig) für die Ähnlichkeitsabfrage.
func significant(text string, max int) []string {
	seen := map[string]bool{}
	var words []string
	for _, w := range ml.Words(text) {
		if len([]rune(w)) >= 3 && !seen[w] {
			seen[w] = true
			words = append(words, w)
		}
	}
	sort.SliceStable(words, func(i, j int) bool { return len([]rune(words[i])) > len([]rune(words[j])) })
	if len(words) > max {
		words = words[:max]
	}
	return words
}

// Neighbors liefert die ähnlichsten eigenen Prompts zu einem Text (ohne exceptID).
func (s *Service) Neighbors(ctx context.Context, sc store.Scope, text string, exceptID int64, limit int) ([]Neighbor, error) {
	words := significant(text, 12)
	if len(words) == 0 {
		return nil, nil
	}
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT p.id, p.title FROM prompts p JOIN prompt_fts ON prompt_fts.rowid = p.id
		WHERE prompt_fts MATCH ? AND p.owner_id = ? AND p.deleted_at IS NULL AND p.id <> ?
		ORDER BY bm25(prompt_fts, 2.0, 1.0, 1.0, 0.3) LIMIT ?`, strings.Join(parts, " OR "), sc.UserID, exceptID, limit)
	if err != nil {
		return nil, err
	}
	var out []Neighbor
	for rows.Next() {
		var n Neighbor
		if err := rows.Scan(&n.ID, &n.Title); err != nil {
			rows.Close()
			return nil, err
		}
		n.Rank = len(out)
		out = append(out, n)
	}
	rows.Close()
	for i := range out {
		trows, err := s.DB.R.QueryContext(ctx, "SELECT t.slug FROM prompt_tags pt JOIN tags t ON t.id = pt.tag_id WHERE pt.prompt_id = ?", out[i].ID)
		if err != nil {
			return nil, err
		}
		for trows.Next() {
			var slug string
			trows.Scan(&slug)
			out[i].Tags = append(out[i].Tags, slug)
		}
		trows.Close()
	}
	return out, nil
}

// Similar liefert ähnliche Prompts zu einem gespeicherten Prompt (Detailansicht).
func (s *Service) Similar(ctx context.Context, sc store.Scope, id int64, limit int) ([]Prompt, error) {
	p, err := s.Get(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	nb, err := s.Neighbors(ctx, sc, p.Text()+" "+strings.Join(p.Tags, " "), id, limit)
	if err != nil || len(nb) == 0 {
		return nil, err
	}
	var out []Prompt
	for _, n := range nb {
		if q, err := s.Get(ctx, sc, n.ID); err == nil {
			out = append(out, q)
		}
	}
	return out, nil
}

// NearDuplicate sucht einen fast gleichen eigenen Prompt (SimHash, TC-ML-07).
func (s *Service) NearDuplicate(ctx context.Context, sc store.Scope, title string, fields map[string]string, exceptID int64) (int64, string, error) {
	text := joinText(title, fields)
	if len(ml.Words(text)) < 5 {
		return 0, "", nil // zu kurz für eine verlässliche Aussage
	}
	h := ml.SimHash(text)
	rows, err := s.DB.R.QueryContext(ctx, "SELECT id, title, simhash FROM prompts WHERE owner_id = ? AND deleted_at IS NULL AND id <> ? AND simhash <> 0", sc.UserID, exceptID)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	best, bestID, bestTitle := ml.NearDupDistance+1, int64(0), ""
	for rows.Next() {
		var id, sh int64
		var t string
		if err := rows.Scan(&id, &t, &sh); err != nil {
			return 0, "", err
		}
		if d := ml.Hamming(h, uint64(sh)); d < best {
			best, bestID, bestTitle = d, id, t
		}
	}
	return bestID, bestTitle, rows.Err()
}

// ---------- Geheimnisse (TC-PRM-10) ----------

var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Anthropic-API-Schlüssel", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"OpenAI-API-Schlüssel", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{20,}`)},
	{"GitHub-Token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{20,}`)},
	{"AWS-Zugangsschlüssel", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"Google-API-Schlüssel", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"Slack-Token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`)},
	{"privater Schlüssel", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// Secrets meldet, welche Arten von Geheimnissen im Text stecken (ohne sie zu wiederholen).
func Secrets(text string) []string {
	var out []string
	for _, p := range secretPatterns {
		for _, m := range p.re.FindAllString(text, 5) {
			// Das OpenAI-Muster trifft auch Anthropic-Schlüssel; die zählen nur einmal.
			if p.name == "OpenAI-API-Schlüssel" && strings.HasPrefix(m, "sk-ant-") {
				continue
			}
			out = append(out, p.name)
			break
		}
	}
	return out
}

// ---------- Platzhalter-Werte ----------

func (s *Service) VarValues(ctx context.Context, sc store.Scope, id int64) (map[string]string, error) {
	if _, err := s.owned(ctx, sc, id); err != nil {
		return nil, err
	}
	rows, err := s.DB.R.QueryContext(ctx, "SELECT name, value FROM var_values WHERE owner_id = ? AND prompt_id = ?", sc.UserID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, rows.Err()
}

func (s *Service) SaveVarValues(ctx context.Context, sc store.Scope, id int64, values map[string]string) error {
	if _, err := s.owned(ctx, sc, id); err != nil {
		return err
	}
	if len(values) > 50 {
		return ValidationError{"vars": "Zu viele Platzhalter."}
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		for n, v := range values {
			if len([]rune(n)) > 40 || len([]rune(v)) > 2000 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO var_values(owner_id, prompt_id, name, value, updated_at) VALUES (?,?,?,?,?)
				ON CONFLICT(owner_id, prompt_id, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				sc.UserID, id, n, v, store.Now().Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- Mehrfachaktionen ----------

// Bulk führt eine Aktion für mehrere eigene Prompts aus; fremde oder unbekannte IDs werden übersprungen.
// Aktionen: tag (arg = Tag), provider (arg = Anbieter-ID), trash. Liefert die Anzahl geänderter Prompts.
func (s *Service) Bulk(ctx context.Context, sc store.Scope, ids []int64, action, arg string) (int, error) {
	n := 0
	for _, id := range ids {
		p, err := s.Get(ctx, sc, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && p.DeletedAt != 0) {
			continue
		} else if err != nil {
			return n, err
		}
		switch action {
		case "trash":
			err = s.Trash(ctx, sc, id)
		case "tag":
			t := tags.Normalize(arg)
			if t == "" {
				return n, ValidationError{"tags": "Tag angeben."}
			}
			in := p.Input()
			in.Tags = append(in.Tags, t)
			err = s.Update(ctx, sc, id, in)
		case "provider":
			var pid int64
			fmt.Sscan(arg, &pid)
			in := p.Input()
			in.ProviderIDs = append(in.ProviderIDs, pid)
			err = s.Update(ctx, sc, id, in)
		default:
			return n, ValidationError{"action": "Unbekannte Aktion."}
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---------- Import ----------

type ImportReport struct {
	Imported   int
	Duplicates int
	Failed     []string
}

// Import legt Prompts aus dem Exportformat an; inhaltsgleiche werden übersprungen.
func (s *Service) Import(ctx context.Context, sc store.Scope, items []ExportPrompt) (ImportReport, error) {
	var rep ImportReport
	all, err := providers.List(ctx, s.DB, sc)
	if err != nil {
		return rep, err
	}
	for i, it := range items {
		label := it.Title
		if label == "" {
			label = fmt.Sprintf("Eintrag %d", i+1)
		}
		in := Input{Title: it.Title, Fields: it.Fields, Notes: it.Notes, Model: it.Model, Tags: it.Tags, Favorite: it.Favorite, Rating: it.Rating, SourceURL: it.Source}
		for _, l := range it.Links {
			in.Links = append(in.Links, Link{Label: l.Label, URL: l.URL})
		}
		var unknown []string
		for _, slug := range it.Providers {
			if p, ok := providers.BySlug(all, providers.Slugify(slug)); ok {
				in.ProviderIDs = append(in.ProviderIDs, p.ID)
			} else {
				unknown = append(unknown, slug)
			}
		}
		if len(in.ProviderIDs) == 0 {
			rep.Failed = append(rep.Failed, fmt.Sprintf("%s: kein bekannter Anbieter (%s)", label, strings.Join(unknown, ", ")))
			continue
		}
		if dup, _ := s.Duplicate(ctx, sc, in.Title, in.Fields, 0); dup > 0 {
			rep.Duplicates++
			continue
		}
		if _, err := s.Create(ctx, sc, in); err != nil {
			rep.Failed = append(rep.Failed, label+": "+err.Error())
			continue
		}
		rep.Imported++
	}
	return rep, nil
}

// ---------- Tag-Verwaltung ----------

type TagInfo struct {
	Slug    string
	Count   int
	Aliases []string
}

// TagList liefert alle Tags mit Anzahl und Aliasen.
func (s *Service) TagList(ctx context.Context, sc store.Scope) ([]TagInfo, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT t.slug, count(p.id) FROM tags t LEFT JOIN prompt_tags pt ON pt.tag_id = t.id
		LEFT JOIN prompts p ON p.id = pt.prompt_id AND p.deleted_at IS NULL WHERE t.owner_id = ? GROUP BY t.id ORDER BY t.slug`, sc.UserID)
	if err != nil {
		return nil, err
	}
	var out []TagInfo
	idx := map[string]int{}
	for rows.Next() {
		var t TagInfo
		if err := rows.Scan(&t.Slug, &t.Count); err != nil {
			rows.Close()
			return nil, err
		}
		idx[t.Slug] = len(out)
		out = append(out, t)
	}
	rows.Close()
	al, err := tags.Aliases(ctx, s.DB.R, sc.UserID)
	if err != nil {
		return nil, err
	}
	for _, a := range al {
		if i, ok := idx[a.Slug]; ok {
			out[i].Aliases = append(out[i].Aliases, a.Alias)
		}
	}
	return out, nil
}

// RenameTag benennt einen Tag um oder führt ihn mit einem bestehenden zusammen (TC-TAG-02).
// Der alte Name wird zum Alias, damit spätere Eingaben automatisch beim neuen Tag landen.
func (s *Service) RenameTag(ctx context.Context, sc store.Scope, from, to string) (int, error) {
	from, to = tags.Normalize(from), tags.Normalize(to)
	if from == "" || to == "" || from == to {
		return 0, ValidationError{"tag": "Alten und neuen Namen angeben."}
	}
	var affected []int64
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var fromID int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM tags WHERE owner_id = ? AND slug = ?", sc.UserID, from).Scan(&fromID)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT prompt_id FROM prompt_tags WHERE tag_id = ?", fromID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			affected = append(affected, id)
		}
		rows.Close()
		var toID int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM tags WHERE owner_id = ? AND slug = ?", sc.UserID, to).Scan(&toID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, "UPDATE tags SET slug = ?, name = ? WHERE id = ?", to, to, fromID); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO prompt_tags(prompt_id, tag_id, source) SELECT prompt_id, ?, source FROM prompt_tags WHERE tag_id = ?", toID, fromID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM tags WHERE id = ?", fromID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tag_aliases SET tag_slug = ? WHERE owner_id = ? AND tag_slug = ?", to, sc.UserID, from); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tag_aliases(owner_id, alias, tag_slug) VALUES (?,?,?)
			ON CONFLICT(owner_id, alias) DO UPDATE SET tag_slug = excluded.tag_slug`, sc.UserID, from, to); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM tag_aliases WHERE owner_id = ? AND alias = tag_slug", sc.UserID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tag_rules SET tag_slug = ? WHERE owner_id = ? AND tag_slug = ?", to, sc.UserID, from); err != nil {
			return err
		}
		for _, id := range affected {
			if err := reindex(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	return len(affected), err
}

// AddAlias: Schreibweise alias landet künftig beim Tag slug.
func (s *Service) AddAlias(ctx context.Context, sc store.Scope, alias, slug string) error {
	alias, slug = tags.Normalize(alias), tags.Normalize(slug)
	if alias == "" || slug == "" || alias == slug {
		return ValidationError{"alias": "Alias und Tag angeben (verschieden)."}
	}
	var exists int
	s.DB.R.QueryRowContext(ctx, "SELECT count(*) FROM tags WHERE owner_id = ? AND slug = ?", sc.UserID, alias).Scan(&exists)
	if exists > 0 {
		return ValidationError{"alias": "„" + alias + "“ ist bereits ein Tag. Zum Zusammenführen bitte umbenennen."}
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tag_aliases(owner_id, alias, tag_slug) VALUES (?,?,?)
			ON CONFLICT(owner_id, alias) DO UPDATE SET tag_slug = excluded.tag_slug`, sc.UserID, alias, slug)
		return err
	})
}

func (s *Service) DeleteAlias(ctx context.Context, sc store.Scope, alias string) error {
	return s.exec(ctx, "DELETE FROM tag_aliases WHERE owner_id = ? AND alias = ?", sc.UserID, alias)
}

// AddRule legt eine Regel an; Regex wird vorab geprüft (RE2, keine Backtracking-Fallen).
func (s *Service) AddRule(ctx context.Context, sc store.Scope, r tags.Rule) error {
	r.Pattern = strings.TrimSpace(r.Pattern)
	r.Tag = tags.Normalize(r.Tag)
	verr := ValidationError{}
	if r.Pattern == "" || len(r.Pattern) > 200 {
		verr["pattern"] = "Muster fehlt oder ist länger als 200 Zeichen."
	}
	if r.Tag == "" {
		verr["tag"] = "Ziel-Tag angeben."
	}
	switch r.Kind {
	case "word", "phrase":
	case "regex":
		if _, err := regexp.Compile("(?i)" + r.Pattern); err != nil {
			verr["pattern"] = "Ungültiger regulärer Ausdruck: " + err.Error()
		}
	default:
		verr["kind"] = "Art wählen."
	}
	if r.Category != "" && providers.CategoryLabel(r.Category) == r.Category {
		r.Category = ""
	}
	if len(verr) > 0 {
		return verr
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO tag_rules(owner_id, category, kind, pattern, tag_slug, auto_apply, created_at) VALUES (?,?,?,?,?,?,?)",
			sc.UserID, r.Category, r.Kind, r.Pattern, r.Tag, r.AutoApply, store.Now().Unix())
		return err
	})
}

func (s *Service) DeleteRule(ctx context.Context, sc store.Scope, id int64) error {
	return s.exec(ctx, "DELETE FROM tag_rules WHERE id = ? AND owner_id = ?", id, sc.UserID)
}

// ---------- Nachberechnung ----------

// Backfill berechnet SimHash und Sprache für Prompts, die vor Schema v2 angelegt wurden.
func (s *Service) Backfill(ctx context.Context) (int, error) {
	rows, err := s.DB.R.QueryContext(ctx, "SELECT id, title, fields FROM prompts WHERE simhash = 0")
	if err != nil {
		return 0, err
	}
	type item struct {
		id   int64
		text string
	}
	var items []item
	for rows.Next() {
		var it item
		var title, fields string
		rows.Scan(&it.id, &title, &fields)
		var f map[string]string
		json.Unmarshal([]byte(fields), &f)
		it.text = joinText(title, f)
		items = append(items, it)
	}
	rows.Close()
	if len(items) == 0 {
		return 0, nil
	}
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			if _, err := tx.ExecContext(ctx, "UPDATE prompts SET simhash = ?, language = ? WHERE id = ?", int64(ml.SimHash(it.text)), ml.Language(it.text), it.id); err != nil {
				return err
			}
		}
		return nil
	})
	return len(items), err
}
