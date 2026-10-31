package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TC-OPS-07: Eine Datenbank mit Schema v1 wird migriert; vorher entsteht ein Backup, Daten und Index bleiben erhalten.
func TestMigrationFromV1TC_OPS_07(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "smmyp.db")
	v1, err := migrations.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		string(v1),
		"INSERT INTO users(id, username, role, created_at) VALUES (1, 'alt', 'admin', 0)",
		`INSERT INTO prompts(id, owner_id, title, fields, content_hash, created_at, updated_at) VALUES (1, 1, 'Gitarrensolo', '{"prompt":"langes Gitarrensolo"}', x'00', 0, 0)`,
		"INSERT INTO prompt_fts(rowid, title, body, tags, notes) VALUES (1, 'Gitarrensolo', 'langes Gitarrensolo', 'rock', '')",
		"PRAGMA user_version = 1",
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("v1 anlegen: %v", err)
		}
	}
	raw.Close()

	backups := filepath.Join(dir, "backups")
	db, err := Open(ctx, path, backups)
	if err != nil {
		t.Fatalf("Migration: %v", err)
	}
	defer db.Close()
	if v, _ := db.Version(ctx); v < 2 {
		t.Fatalf("Schema-Version %d", v)
	}
	files, _ := os.ReadDir(backups)
	if len(files) != 1 || !strings.HasPrefix(files[0].Name(), "pre-migration-v1-") {
		t.Fatalf("Backup vor Migration fehlt: %v", files)
	}
	var title string
	db.R.QueryRow("SELECT title FROM prompts WHERE id = 1").Scan(&title)
	if title != "Gitarrensolo" {
		t.Fatal("Daten verloren")
	}
	var n int
	db.R.QueryRow(`SELECT count(*) FROM prompt_trigram WHERE prompt_trigram MATCH '"solo"'`).Scan(&n)
	if n != 1 {
		t.Fatalf("Trigram-Index nicht aus dem Bestand aufgebaut (%d)", n)
	}
	// Ein zweites Öffnen migriert nichts mehr und legt kein weiteres Backup an.
	db.Close()
	db2, err := Open(ctx, path, backups)
	if err != nil {
		t.Fatal(err)
	}
	db2.Close()
	if files, _ := os.ReadDir(backups); len(files) != 1 {
		t.Fatalf("unnötiges Backup beim erneuten Öffnen: %d", len(files))
	}
}
