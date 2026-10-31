package web

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
)

// Page enthält alles, was das Layout braucht; Data ist seitenspezifisch.
type Page struct {
	Title     string
	Plain     bool
	User      *auth.User
	Theme     string
	Version   string
	Flash     string
	FlashKind string
	Path      string
	Rail      *Rail
	Q         string
	Scope     string
	Scopes    []ScopeOption
	View      string
	Errors    map[string]string
	Form      url.Values
	Data      any
}

type ScopeOption struct {
	Value, Label, Group string
	Selected            bool
}

func (s *Server) loadTemplates() error {
	funcs := template.FuncMap{
		"catLabel": providers.CategoryLabel,
		"reltime":  relTime,
		"date":     func(ts int64) string { return time.Unix(ts, 0).Local().Format("02.01.2006 15:04") },
		"bytes":    humanBytes,
		"inc":      func(i int) int { return i + 1 },
		"has": func(list []string, v string) bool {
			for _, x := range list {
				if x == v {
					return true
				}
			}
			return false
		},
		"join": strings.Join,
		"initials": func(name string) string {
			r := []rune(strings.ToUpper(name))
			if len(r) > 2 {
				r = r[:2]
			}
			return string(r)
		},
		"query": func(pairs ...string) string {
			v := url.Values{}
			for i := 0; i+1 < len(pairs); i += 2 {
				if pairs[i+1] != "" {
					v.Set(pairs[i], pairs[i+1])
				}
			}
			if len(v) == 0 {
				return "/"
			}
			return "/?" + v.Encode()
		},
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.tpl = map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if name == "base" || name == "partials" {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/base.html", "templates/partials.html", p)
		if err != nil {
			return fmt.Errorf("Template %s: %w", name, err)
		}
		s.tpl[name] = t
	}
	return nil
}

// page füllt die gemeinsamen Felder.
func (s *Server) page(w http.ResponseWriter, r *http.Request, title string) *Page {
	p := &Page{Title: title, User: currentUser(r), Version: s.version, Path: r.URL.RequestURI()}
	if p.User != nil {
		p.Theme = p.User.Settings.Theme
		p.View = p.User.Settings.View
	}
	if p.View != "list" {
		p.View = "cards"
	}
	if c, err := r.Cookie("smmyp_flash"); err == nil {
		if v, err := url.QueryUnescape(c.Value); err == nil {
			kind, msg, _ := strings.Cut(v, "|")
			p.FlashKind, p.Flash = kind, msg
		}
		http.SetCookie(w, &http.Cookie{Name: "smmyp_flash", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	return p
}

// shell lädt Schubladen-Navigation und Suchbereich für App-Seiten.
func (s *Server) shell(r *http.Request, p *Page) error {
	rail, err := s.rail(r, p.User)
	if err != nil {
		return err
	}
	p.Rail = rail
	p.Scopes = scopeOptions(rail.All, p.Scope)
	return nil
}

func (s *Server) render(w http.ResponseWriter, status int, name string, p *Page) {
	s.renderTemplate(w, status, name, "base", p)
}

func (s *Server) renderTemplate(w http.ResponseWriter, status int, name, tmpl string, data any) {
	t, ok := s.tpl[name]
	if !ok {
		http.Error(w, "Template fehlt: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, tmpl, data); err != nil {
		s.log.Error("render", "template", name, "err", err)
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *Server) flash(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, &http.Cookie{Name: "smmyp_flash", Value: url.QueryEscape(kind + "|" + msg), Path: "/", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.SecureCookies})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	p := s.page(w, r, "Nicht gefunden")
	p.Plain = true
	p.Data = map[string]string{"Code": "404", "Text": "Diese Karte gibt es nicht, oder sie liegt in einer fremden Schublade."}
	s.render(w, http.StatusNotFound, "error", p)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("handler", "path", r.URL.Path, "err", err)
	p := s.page(w, r, "Fehler")
	p.Plain = true
	p.Data = map[string]string{"Code": "500", "Text": "Da ist etwas schiefgelaufen. Details stehen im Container-Log."}
	s.render(w, http.StatusInternalServerError, "error", p)
}

// localPath akzeptiert nur Pfade auf dieser Instanz (kein Open Redirect).
func localPath(s, fallback string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "/\\") && !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return fallback
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

func relTime(ts int64) string {
	if ts == 0 {
		return "noch nie"
	}
	d := store.Now().Sub(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "gerade eben"
	case d < time.Hour:
		return fmt.Sprintf("vor %d Min.", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("vor %d Std.", int(d.Hours()))
	case d < 48*time.Hour:
		return "gestern"
	case d < 14*24*time.Hour:
		return fmt.Sprintf("vor %d Tagen", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("vor %d Wochen", int(d.Hours()/24/7))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("vor %d Monaten", int(d.Hours()/24/30))
	}
	return time.Unix(ts, 0).Local().Format("02.01.2006")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
