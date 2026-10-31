// Package collections: Sammlungen bündeln Prompts quer über Anbieter (Concept V9), z. B. ein Album-Projekt.
package collections

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Buffer379x/smmyp/internal/store"
)

type Collection struct {
	ID        int64
	Name      string
	Notes     string
	Count     int
	UpdatedAt int64
	Member    bool // nur bei ForPrompt: enthält den Prompt
}

type ValidationError map[string]string

func (v ValidationError) Error() string { return "Eingaben prüfen" }

func validate(name, notes string) (string, string, error) {
	name, notes = strings.TrimSpace(name), strings.TrimSpace(notes)
	if name == "" || len([]rune(name)) > 80 {
		return "", "", ValidationError{"name": "Name fehlt oder ist länger als 80 Zeichen."}
	}
	if len([]rune(notes)) > 5000 {
		return "", "", ValidationError{"notes": "Notizen höchstens 5000 Zeichen."}
	}
	return name, notes, nil
}

// List: alle Sammlungen des Benutzers mit Anzahl (nicht gelöschter) Prompts.
func List(ctx context.Context, db *store.DB, sc store.Scope) ([]Collection, error) {
	return query(ctx, db, sc, 0)
}

// ForPrompt: alle Sammlungen, Member = enthält den Prompt.
func ForPrompt(ctx context.Context, db *store.DB, sc store.Scope, promptID int64) ([]Collection, error) {
	return query(ctx, db, sc, promptID)
}

func query(ctx context.Context, db *store.DB, sc store.Scope, promptID int64) ([]Collection, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT c.id, c.name, c.notes, c.updated_at,
		(SELECT count(*) FROM collection_prompts cp JOIN prompts p ON p.id = cp.prompt_id WHERE cp.collection_id = c.id AND p.deleted_at IS NULL),
		EXISTS (SELECT 1 FROM collection_prompts m WHERE m.collection_id = c.id AND m.prompt_id = ?)
		FROM collections c WHERE c.owner_id = ? ORDER BY c.name COLLATE NOCASE`, promptID, sc.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Notes, &c.UpdatedAt, &c.Count, &c.Member); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func Get(ctx context.Context, db *store.DB, sc store.Scope, id int64) (Collection, error) {
	var c Collection
	err := db.R.QueryRowContext(ctx, `SELECT id, name, notes, updated_at FROM collections WHERE id = ? AND owner_id = ?`, id, sc.UserID).
		Scan(&c.ID, &c.Name, &c.Notes, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, store.ErrNotFound
	}
	return c, err
}

func Create(ctx context.Context, db *store.DB, sc store.Scope, name, notes string) (int64, error) {
	name, notes, err := validate(name, notes)
	if err != nil {
		return 0, err
	}
	var id int64
	now := store.Now().Unix()
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO collections(owner_id, name, notes, created_at, updated_at) VALUES (?,?,?,?,?)", sc.UserID, name, notes, now, now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func exec(ctx context.Context, db *store.DB, q string, args ...any) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func Update(ctx context.Context, db *store.DB, sc store.Scope, id int64, name, notes string) error {
	name, notes, err := validate(name, notes)
	if err != nil {
		return err
	}
	return exec(ctx, db, "UPDATE collections SET name = ?, notes = ?, updated_at = ? WHERE id = ? AND owner_id = ?", name, notes, store.Now().Unix(), id, sc.UserID)
}

// Delete löscht nur die Sammlung, nicht die Prompts.
func Delete(ctx context.Context, db *store.DB, sc store.Scope, id int64) error {
	return exec(ctx, db, "DELETE FROM collections WHERE id = ? AND owner_id = ?", id, sc.UserID)
}

// Add nimmt eigene Prompts in eine eigene Sammlung auf; fremde IDs werden ignoriert. Liefert die Anzahl neuer Einträge.
func Add(ctx context.Context, db *store.DB, sc store.Scope, id int64, promptIDs []int64) (int, error) {
	if _, err := Get(ctx, db, sc, id); err != nil {
		return 0, err
	}
	n := 0
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		for _, pid := range promptIDs {
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_prompts(collection_id, prompt_id, added_at)
				SELECT ?, id, ? FROM prompts WHERE id = ? AND owner_id = ?`, id, store.Now().Unix(), pid, sc.UserID)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		_, err := tx.ExecContext(ctx, "UPDATE collections SET updated_at = ? WHERE id = ?", store.Now().Unix(), id)
		return err
	})
	return n, err
}

func Remove(ctx context.Context, db *store.DB, sc store.Scope, id, promptID int64) error {
	if _, err := Get(ctx, db, sc, id); err != nil {
		return err
	}
	return exec(ctx, db, "DELETE FROM collection_prompts WHERE collection_id = ? AND prompt_id = ?", id, promptID)
}
