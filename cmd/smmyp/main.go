// smmyp – Show Me My Prompts. Ein Binary: Webserver, Healthcheck und Wartungsbefehle.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // Zeitzonen auch im distroless-Image

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/backup"
	"github.com/Buffer379x/smmyp/internal/config"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/web"
)

var version = "dev"

const usage = `smmyp – Show Me My Prompts %s

Befehle:
  serve          Webserver starten (Standard)
  healthcheck    Prüft den laufenden Server (Exit-Code 0 = gesund)
  backup         Erstellt sofort ein Backup in <SMMYP_DATA_DIR>/backups
  reindex        Baut den Suchindex neu auf
  passwd <name>  Setzt ein neues Passwort (liest es von der Standardeingabe)
  version        Zeigt die Version

Konfiguration über Umgebungsvariablen (SMMYP_*), siehe README.
`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "backup", "reindex", "passwd":
		err = maintenance(cmd, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "smmyp:", err)
		os.Exit(1)
	}
}

func loadConfig() (config.Config, error) {
	return config.Load(os.Getenv)
}

func paths(cfg config.Config) (dbPath, backupDir string) {
	return filepath.Join(cfg.DataDir, "smmyp.db"), filepath.Join(cfg.DataDir, "backups")
}

// ensureWritable prüft das Datenverzeichnis und liefert eine verständliche Fehlermeldung (Concept R5).
func ensureWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return writableHint(dir, err)
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return writableHint(dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

func writableHint(dir string, err error) error {
	return fmt.Errorf("Datenverzeichnis %s ist nicht beschreibbar (%v). Lösung: Named Volume verwenden, "+
		"bei Bind-Mounts `chown 65532:65532 <Host-Pfad>` ausführen, oder (Unraid) mit --user 0:0 und PUID/PGID starten", dir, err)
}

func serve() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	if err := dropPrivileges(cfg.DataDir, log); err != nil {
		return err
	}
	if err := ensureWritable(cfg.DataDir); err != nil {
		return err
	}
	if fs := networkFS(cfg.DataDir); fs != "" {
		log.Warn("Datenverzeichnis liegt auf einem Netzlaufwerk – SQLite-Sperren sind dort unzuverlässig, Datenverlust möglich. Bitte lokales Volume nutzen.", "dir", cfg.DataDir, "fs", fs)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbPath, backupDir := paths(cfg)
	db, err := store.Open(ctx, dbPath, backupDir)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := providers.Seed(ctx, db); err != nil {
		return err
	}
	authSvc := &auth.Service{DB: db}
	if cfg.AdminUser != "" && cfg.AdminPassword != "" {
		if n, _ := authSvc.UserCount(ctx); n == 0 {
			if _, err := authSvc.CreateFirstAdmin(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
				return fmt.Errorf("Admin aus SMMYP_ADMIN_USER anlegen: %w", err)
			}
			log.Info("Admin aus Umgebungsvariablen angelegt", "user", cfg.AdminUser)
		}
	}

	srv, err := web.New(web.Options{Config: cfg, DB: db, Logger: log, Version: version, BackupDir: backupDir})
	if err != nil {
		return err
	}
	if tok := srv.SetupToken(ctx); tok != "" {
		log.Info("ERST-SETUP: Noch kein Konto vorhanden. Öffne /setup im Browser und gib dieses Setup-Token ein.", "setup_token", tok)
	}

	httpSrv := &http.Server{
		Addr: cfg.Listen, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	httpSrv.Handler = http.MaxBytesHandler(httpSrv.Handler, 512<<10)

	go background(ctx, cfg, db, backupDir, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("SMMYP gestartet", "version", version, "listen", cfg.Listen, "data", cfg.DataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("Beende …")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// background: tägliches Backup, Papierkorb leeren, abgelaufene Sessions entfernen.
func background(ctx context.Context, cfg config.Config, db *store.DB, backupDir string, log *slog.Logger) {
	ps := &prompts.Service{DB: db}
	as := &auth.Service{DB: db}
	housekeeping := func() {
		if n, err := ps.PurgeExpired(ctx); err != nil {
			log.Error("Papierkorb leeren", "err", err)
		} else if n > 0 {
			log.Info("Papierkorb geleert", "prompts", n)
		}
		if err := as.CleanupSessions(ctx); err != nil {
			log.Error("Sessions aufräumen", "err", err)
		}
	}
	housekeeping()
	lastBackup := ""
	lastHousekeeping := time.Now()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if cfg.BackupTime != "" && now.Format("15:04") == cfg.BackupTime && lastBackup != now.Format("2006-01-02") {
				lastBackup = now.Format("2006-01-02")
				if path, err := backup.Run(ctx, db, backupDir, cfg.BackupKeep); err != nil {
					log.Error("Backup fehlgeschlagen", "err", err)
				} else {
					log.Info("Backup erstellt", "file", filepath.Base(path))
				}
			}
			if now.Sub(lastHousekeeping) > time.Hour {
				lastHousekeeping = now
				housekeeping()
			}
		}
	}
}

func healthcheck() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("SMMYP_LISTEN %q: %w", cfg.Listen, err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: HTTP %d", resp.StatusCode)
	}
	return nil
}

func maintenance(cmd string, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()
	dbPath, backupDir := paths(cfg)
	db, err := store.Open(ctx, dbPath, backupDir)
	if err != nil {
		return err
	}
	defer db.Close()
	switch cmd {
	case "backup":
		path, err := backup.Run(ctx, db, backupDir, cfg.BackupKeep)
		if err != nil {
			return err
		}
		fmt.Println(path)
	case "reindex":
		n, err := (&prompts.Service{DB: db}).Reindex(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Suchindex neu aufgebaut: %d Prompts\n", n)
	case "passwd":
		if len(args) != 1 {
			return errors.New("Aufruf: smmyp passwd <benutzername>   (neues Passwort über die Standardeingabe)")
		}
		as := &auth.Service{DB: db}
		u, err := as.UserByName(ctx, args[0])
		if err != nil {
			return fmt.Errorf("Benutzer %q nicht gefunden", args[0])
		}
		fmt.Fprint(os.Stderr, "Neues Passwort: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		if err := as.SetPassword(ctx, u.ID, strings.TrimRight(line, "\r\n")); err != nil {
			return err
		}
		if err := as.DeleteSessions(ctx, u.ID); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Passwort gesetzt, alle Sitzungen beendet.")
	}
	return nil
}
