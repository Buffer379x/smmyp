// Package store kapselt SQLite: Verbindungen, Migrationen, Transaktionen und Backups.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound bedeutet „existiert nicht oder gehört jemand anderem". Handler machen daraus 404.
var ErrNotFound = errors.New("nicht gefunden")

// Scope ist der Benutzerkontext. Jede Abfrage auf Benutzerdaten braucht ihn.
type Scope struct {
	UserID int64
	Admin  bool
}

// DB hält eine Schreibverbindung (Schreibvorgänge laufen nacheinander) und einen Lese-Pool.
type DB struct {
	W, R *sql.DB
	Path string
}

const pragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)&_pragma=cache_size(-8000)&_pragma=temp_store(MEMORY)"

// Open öffnet (oder erzeugt) die Datenbank und spielt fehlende Migrationen ein.
// backupDir erhält vor jeder Migration einer bestehenden Datenbank eine Sicherung.
func Open(ctx context.Context, path, backupDir string) (*DB, error) {
	w, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxLifetime(0)
	r, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	db := &DB{W: w, R: r, Path: path}
	if err := db.migrate(ctx, backupDir); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) migrate(ctx context.Context, backupDir string) error {
	var version int
	if err := db.W.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("Datenbank nicht lesbar: %w", err)
	}
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		n, err := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("Migrationsname %s: %w", f, err)
		}
		if n <= version {
			continue
		}
		if version > 0 && backupDir != "" {
			dst := filepath.Join(backupDir, fmt.Sprintf("pre-migration-v%d-%s.db", version, time.Now().UTC().Format("20060102T150405")))
			if err := db.Backup(ctx, dst); err != nil {
				return fmt.Errorf("Backup vor Migration fehlgeschlagen: %w", err)
			}
		}
		body, err := migrations.ReadFile(f)
		if err != nil {
			return err
		}
		err = db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("Migration %s: %w", f, err)
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", n))
			return err
		})
		if err != nil {
			return err
		}
		version = n
	}
	return nil
}

// Version liefert die Schema-Version.
func (db *DB) Version(ctx context.Context) (int, error) {
	var v int
	err := db.R.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// Tx führt fn in einer Schreibtransaktion aus (BEGIN IMMEDIATE).
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Backup schreibt einen konsistenten Schnappschuss per VACUUM INTO, auch im laufenden Betrieb.
func (db *DB) Backup(ctx context.Context, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("Backup-Datei existiert bereits: %s", dst)
	}
	_, err := db.W.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}

// Close schreibt das WAL zurück und schließt alle Verbindungen.
func (db *DB) Close() error {
	db.W.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	db.W.Exec("PRAGMA optimize")
	return errors.Join(db.R.Close(), db.W.Close())
}

// Now ist die Zeitquelle (in Tests austauschbar).
var Now = func() time.Time { return time.Now() }

// In baut "?,?,?" für IN-Listen.
func In(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
