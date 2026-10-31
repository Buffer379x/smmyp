package backup

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Buffer379x/smmyp/internal/store"
)

// TC-OPS-05: Backup unter laufender Schreiblast ist konsistent. Rotation behält genau keep Dateien.
func TestBackupUnderWriteLoadAndRotationTC_OPS_05(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "smmyp.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.W.Exec("INSERT INTO users(username, role, created_at) VALUES ('u','member',0)")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			db.Tx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("INSERT INTO prompts(owner_id, title, fields, content_hash, created_at, updated_at) VALUES (1, ?, '{}', x'00', 0, 0)", fmt.Sprint("p", i))
				return err
			})
		}
	}()

	backups := filepath.Join(dir, "backups")
	real := store.Now
	defer func() { store.Now = real }()
	for i := 0; i < 4; i++ {
		ts := time.Date(2026, 10, 31, 3, 30, i, 0, time.UTC)
		store.Now = func() time.Time { return ts }
		path, err := Run(ctx, db, backups, 3)
		if err != nil {
			t.Fatal(err)
		}
		check, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var res string
		check.QueryRow("PRAGMA integrity_check").Scan(&res)
		check.Close()
		if res != "ok" {
			t.Fatalf("integrity_check: %s", res)
		}
	}
	close(stop)
	wg.Wait()
	files, _ := List(backups)
	if len(files) != 3 || files[0].Name != "smmyp-20261031-033003.db" {
		t.Fatalf("Rotation: %+v", files)
	}
	if ValidName("../smmyp.db") || ValidName("smmyp.db") || !ValidName("smmyp-20261031-033003.db") {
		t.Error("ValidName lässt falsche Namen zu")
	}
}
