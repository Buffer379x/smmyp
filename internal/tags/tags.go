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
