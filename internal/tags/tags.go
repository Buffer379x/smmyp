// Package tags normalisiert Tags und legt sie pro Benutzer an.
package tags

import (
	"context"
	"database/sql"
	"strings"
	"unicode"
)

// MaxPerPrompt begrenzt die Anzahl Tags je Prompt.
const MaxPerPrompt = 30

// Normalize: „ Lo Fi " → „lo-fi", „#Synthwave" → „synthwave". Leere Ergebnisse bedeuten „kein Tag".
func Normalize(s string) string {
	s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "#"))
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ' || r == '/' || r == '.' || unicode.IsSpace(r):
			dash = true
		}
	}
	out := b.String()
	if len([]rune(out)) > 40 {
		out = string([]rune(out)[:40])
	}
	return strings.Trim(out, "-")
}

// Parse zerlegt eine Eingabe wie „#lofi, Study Beats #chill" in normalisierte, eindeutige Tags.
func Parse(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '#' || r == ';' || r == '\n' })
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		if t := Normalize(p); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Set ersetzt die Tags eines Prompts (innerhalb einer Transaktion).
func Set(ctx context.Context, tx *sql.Tx, ownerID, promptID int64, slugs []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM prompt_tags WHERE prompt_id = ?", promptID); err != nil {
		return err
	}
	for _, slug := range slugs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags(owner_id, name, slug) VALUES (?,?,?)
			ON CONFLICT(owner_id, slug) DO NOTHING`, ownerID, slug, slug); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO prompt_tags(prompt_id, tag_id)
			SELECT ?, id FROM tags WHERE owner_id = ? AND slug = ?`, promptID, ownerID, slug); err != nil {
			return err
		}
	}
	// Verwaiste Tags aufräumen, damit Vorschläge und Tag-Wolke sauber bleiben.
	_, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE owner_id = ? AND id NOT IN (SELECT tag_id FROM prompt_tags)`, ownerID)
	return err
}

type Count struct {
	Slug  string
	Count int
}

// Top liefert die häufigsten Tags eines Benutzers (für Autovervollständigung und Tag-Wolke).
func Top(ctx context.Context, db *sql.DB, ownerID int64, limit int) ([]Count, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.slug, count(*) c FROM tags t
		JOIN prompt_tags pt ON pt.tag_id = t.id JOIN prompts p ON p.id = pt.prompt_id
		WHERE t.owner_id = ? AND p.deleted_at IS NULL GROUP BY t.id ORDER BY c DESC, t.slug LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Count
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Slug, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DefaultAliases gelten, solange ein Benutzer keinen eigenen Alias für dieselbe Schreibweise hat.
var DefaultAliases = map[string]string{
	"lofi": "lo-fi", "hiphop": "hip-hop", "rnb": "r-n-b", "dnb": "drum-and-bass",
	"synth-wave": "synthwave", "lo-fi-hip-hop": "lo-fi", "codereview": "code-review",
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Resolve bildet Tags über Aliase auf ihre kanonische Form ab (eigene Aliase vor Standard-Aliasen).
func Resolve(ctx context.Context, q querier, ownerID int64, slugs []string) ([]string, error) {
	own := map[string]string{}
	if len(slugs) > 0 {
		args := []any{ownerID}
		ph := make([]string, len(slugs))
		for i, s := range slugs {
			args = append(args, s)
			ph[i] = "?"
		}
		rows, err := q.QueryContext(ctx, "SELECT alias, tag_slug FROM tag_aliases WHERE owner_id = ? AND alias IN ("+strings.Join(ph, ",")+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var a, t string
			if err := rows.Scan(&a, &t); err != nil {
				rows.Close()
				return nil, err
			}
			own[a] = t
		}
		rows.Close()
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range slugs {
		if t, ok := own[s]; ok {
			s = t
		} else if t, ok := DefaultAliases[s]; ok {
			s = t
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

type Alias struct{ Alias, Slug string }

func Aliases(ctx context.Context, db *sql.DB, ownerID int64) ([]Alias, error) {
	rows, err := db.QueryContext(ctx, "SELECT alias, tag_slug FROM tag_aliases WHERE owner_id = ? ORDER BY tag_slug, alias", ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.Alias, &a.Slug); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Rule: eigene Regel „enthält X → Tag Y" (Stufe 1 des Auto-Taggings).
type Rule struct {
	ID        int64
	Category  string
	Kind      string // word | phrase | regex
	Pattern   string
	Tag       string
	AutoApply bool
}

func Rules(ctx context.Context, db *sql.DB, ownerID int64) ([]Rule, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, category, kind, pattern, tag_slug, auto_apply FROM tag_rules WHERE owner_id = ? ORDER BY tag_slug, id", ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Category, &r.Kind, &r.Pattern, &r.Tag, &r.AutoApply); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
