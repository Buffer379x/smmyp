package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/backup"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/search"
	"github.com/Buffer379x/smmyp/internal/store"
)

// ---------- Anbieter ----------

type ProvidersData struct {
	Providers  []providers.Provider
	Counts     map[int64]int
	Catalog    []providers.Field
	Categories []providers.Category
	Colors     []string
}

func (s *Server) providersPage(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.renderProviders(w, r, u, http.StatusOK, nil)
}

func (s *Server) renderProviders(w http.ResponseWriter, r *http.Request, u *auth.User, status int, errs map[string]string) {
	p := s.page(w, r, "Anbieter")
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	counts, err := s.prompts.Counts(r.Context(), u.Scope())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	r.ParseForm()
	p.Form = r.PostForm
	p.Errors = errs
	p.Data = &ProvidersData{Providers: p.Rail.All, Counts: counts.ByProvider, Catalog: providers.Catalog, Categories: providers.Categories, Colors: providers.Colors}
	s.render(w, status, "providers", p)
}

func (s *Server) providerCreate(w http.ResponseWriter, r *http.Request, u *auth.User) {
	r.ParseForm()
	in := providers.Input{Name: r.PostForm.Get("name"), Slug: r.PostForm.Get("slug"), Category: r.PostForm.Get("category"),
		Color: r.PostForm.Get("color"), FieldKeys: r.PostForm["fields"], PrimaryField: r.PostForm.Get("primary"), Global: r.PostForm.Get("global") == "1"}
	_, err := providers.Create(r.Context(), s.db, u.Scope(), in)
	var verr providers.ValidationError
	if errors.As(err, &verr) {
		s.renderProviders(w, r, u, http.StatusUnprocessableEntity, verr)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.flash(w, "ok", "Anbieter angelegt.")
	s.redirect(w, r, "/providers")
}

func (s *Server) providerDelete(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := providers.Delete(r.Context(), s.db, u.Scope(), id)
	var verr providers.ValidationError
	switch {
	case errors.As(err, &verr):
		s.flash(w, "error", verr["delete"])
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		s.flash(w, "ok", "Anbieter gelöscht.")
	}
	s.redirect(w, r, "/providers")
}

// ---------- Papierkorb & Export ----------

type TrashData struct {
	Cards []CardVM
	Days  int
}

func (s *Server) trash(w http.ResponseWriter, r *http.Request, u *auth.User) {
	p := s.page(w, r, "Papierkorb")
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Rail.Active = "trash"
	res, err := s.prompts.List(r.Context(), u.Scope(), prompts.ListOptions{Trash: true, Limit: 500})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := &TrashData{Days: prompts.TrashDays}
	for _, it := range res.Items {
		d.Cards = append(d.Cards, card(it, nil, search.Query{}))
	}
	p.Data = d
	s.render(w, http.StatusOK, "trash", p)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, u *auth.User) {
	list, err := s.prompts.Export(r.Context(), u.Scope())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	name := fmt.Sprintf("smmyp-export-%s-%s.json", u.Username, time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]any{"format": "smmyp-export", "version": 1, "exported": time.Now().UTC(), "prompts": list})
}

// ---------- Admin ----------

type AdminData struct {
	Users      []auth.User
	Me         int64
	InviteLink string
	Backups    []backup.File
	Version    string
	Schema     int
	DataDir    string
	DBSize     int64
	HTTPS      bool
	BackupTime string
	BackupKeep int
	Register   string
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request, u *auth.User, status int, invite string) {
	p := s.page(w, r, "Verwaltung")
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	users, err := s.auth.Users(r.Context(), u.Scope())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	files, _ := backup.List(s.backupDir)
	schema, _ := s.db.Version(r.Context())
	var size int64
	if fi, err := os.Stat(s.db.Path); err == nil {
		size = fi.Size()
	}
	https := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	p.Data = &AdminData{Users: users, Me: u.ID, InviteLink: invite, Backups: files, Version: s.version, Schema: schema,
		DataDir: filepath.Dir(s.db.Path), DBSize: size, HTTPS: https, BackupTime: s.cfg.BackupTime, BackupKeep: s.cfg.BackupKeep, Register: s.cfg.Registration}
	s.render(w, status, "admin", p)
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.adminPage(w, r, u, http.StatusOK, "")
}

func (s *Server) adminInvite(w http.ResponseWriter, r *http.Request, u *auth.User) {
	r.ParseForm()
	tok, err := s.auth.CreateInvite(r.Context(), u.Scope(), r.PostForm.Get("role"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base := "http://" + r.Host
	if s.cfg.BaseURL != nil {
		base = s.cfg.BaseURL.Scheme + "://" + s.cfg.BaseURL.Host
	} else if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		base = "https://" + r.Host
	}
	s.adminPage(w, r, u, http.StatusOK, base+"/invite/"+tok)
}

func (s *Server) adminSetDisabled(w http.ResponseWriter, r *http.Request, u *auth.User, disabled bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.auth.SetDisabled(r.Context(), u.Scope(), id, disabled); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if disabled {
		s.flash(w, "ok", "Konto deaktiviert, alle Sitzungen beendet.")
	} else {
		s.flash(w, "ok", "Konto aktiviert.")
	}
	s.redirect(w, r, "/admin")
}

func (s *Server) adminDisable(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.adminSetDisabled(w, r, u, true)
}

func (s *Server) adminEnable(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.adminSetDisabled(w, r, u, false)
}

func (s *Server) adminBackup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	path, err := backup.Run(r.Context(), s.db, s.backupDir, s.cfg.BackupKeep)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("backup erstellt", "file", filepath.Base(path), "by", u.ID)
	s.flash(w, "ok", "Backup erstellt: "+filepath.Base(path))
	s.redirect(w, r, "/admin")
}

func (s *Server) adminBackupDownload(w http.ResponseWriter, r *http.Request, u *auth.User) {
	name := r.PathValue("name")
	if !backup.ValidName(name) {
		s.notFound(w, r)
		return
	}
	path := filepath.Join(s.backupDir, name)
	if _, err := os.Stat(path); err != nil {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}
