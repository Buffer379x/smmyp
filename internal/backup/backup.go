// Package backup erstellt konsistente Datenbank-Schnappschüsse und hält eine feste Anzahl vor.
package backup

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/Buffer379x/smmyp/internal/store"
)

// nameRe: nur diese Dateien werden angezeigt, heruntergeladen und rotiert.
var nameRe = regexp.MustCompile(`^(smmyp-\d{8}-\d{6}|pre-migration-v\d+-\d{8}T\d{6})\.db$`)

func ValidName(name string) bool { return nameRe.MatchString(name) && filepath.Base(name) == name }

type File struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Run schreibt ein Backup nach dir und löscht die ältesten regulären Backups über keep hinaus.
func Run(ctx context.Context, db *store.DB, dir string, keep int) (string, error) {
	name := "smmyp-" + store.Now().UTC().Format("20060102-150405") + ".db"
	dst := filepath.Join(dir, name)
	if err := db.Backup(ctx, dst); err != nil {
		return "", err
	}
	return dst, rotate(dir, keep)
}

func rotate(dir string, keep int) error {
	files, err := List(dir)
	if err != nil {
		return err
	}
	var regular []File
	for _, f := range files {
		if len(f.Name) > 6 && f.Name[:6] == "smmyp-" {
			regular = append(regular, f)
		}
	}
	// List sortiert absteigend nach Name (= Zeitstempel); alles ab Index keep fliegt raus.
	for i := keep; i < len(regular); i++ {
		if err := os.Remove(filepath.Join(dir, regular[i].Name)); err != nil {
			return err
		}
	}
	return nil
}

// List liefert vorhandene Backups, neueste zuerst.
func List(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range entries {
		if e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, File{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}
