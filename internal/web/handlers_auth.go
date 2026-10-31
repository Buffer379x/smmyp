package web

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/store"
)

func (s *Server) plain(w http.ResponseWriter, r *http.Request, title string) *Page {
	p := s.page(w, r, title)
	p.Plain = true
	r.ParseForm()
	p.Form = r.PostForm
	return p
}

// rateLimited antwortet mit 429, wenn IP oder Benutzername gesperrt sind.
func (s *Server) rateLimited(w http.ResponseWriter, r *http.Request, username string, tmpl string, p *Page) bool {
	ipKey := s.clientIP(r)
	blocked, wait := s.loginIP.Blocked(ipKey)
	if !blocked && username != "" {
		blocked, wait = s.loginUser.Blocked(strings.ToLower(username))
	}
	if !blocked {
		return false
	}
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	p.Errors = map[string]string{"form": fmt.Sprintf("Zu viele Fehlversuche. Bitte in %d Sekunden erneut versuchen.", secs)}
	s.render(w, http.StatusTooManyRequests, tmpl, p)
	return true
}

func (s *Server) failAttempt(r *http.Request, username string) {
	s.loginIP.Fail(s.clientIP(r))
	if username != "" {
		s.loginUser.Fail(strings.ToLower(username))
	}
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	tok, err := s.auth.NewSession(r.Context(), userID, r.UserAgent())
	if err != nil {
		return err
	}
	s.setSessionCookie(w, tok)
	return nil
}

// ---------- Erst-Setup ----------

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	if s.SetupToken(r.Context()) == "" {
		s.notFound(w, r)
		return
	}
	s.render(w, http.StatusOK, "setup", s.plain(w, r, "Einrichtung"))
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	want := s.SetupToken(r.Context())
	if want == "" {
		s.notFound(w, r)
		return
	}
	p := s.plain(w, r, "Einrichtung")
	if s.rateLimited(w, r, "", "setup", p) {
		return
	}
	got := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("token")))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		s.failAttempt(r, "")
		s.log.Warn("setup: falsches Token", "ip", s.clientIP(r))
		p.Errors = map[string]string{"token": "Token stimmt nicht. Du findest es im Container-Log."}
		s.render(w, http.StatusForbidden, "setup", p)
		return
	}
	id, err := s.auth.CreateFirstAdmin(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"))
	var verr auth.ValidationError
	if errors.As(err, &verr) {
		p.Errors = verr
		s.render(w, http.StatusUnprocessableEntity, "setup", p)
		return
	} else if err != nil {
		s.notFound(w, r)
		return
	}
	s.mu.Lock()
	s.setupToken = ""
	s.mu.Unlock()
	s.log.Info("setup abgeschlossen", "user_id", id)
	if err := s.startSession(w, r, id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.flash(w, "ok", "Willkommen! Lege deinen ersten Prompt an.")
	s.redirect(w, r, "/")
}

// ---------- Login ----------

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if n, _ := s.auth.UserCount(r.Context()); n == 0 {
		s.redirect(w, r, "/setup")
		return
	}
	if currentUser(r) != nil {
		s.redirect(w, r, "/")
		return
	}
	p := s.plain(w, r, "Anmelden")
	p.Data = s.cfg.Registration == "open"
	s.render(w, http.StatusOK, "login", p)
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	p := s.plain(w, r, "Anmelden")
	p.Data = s.cfg.Registration == "open"
	username := strings.TrimSpace(r.PostForm.Get("username"))
	if s.rateLimited(w, r, username, "login", p) {
		return
	}
	u, err := s.auth.Authenticate(r.Context(), username, r.PostForm.Get("password"))
	if err != nil {
		s.failAttempt(r, username)
		s.log.Warn("login fehlgeschlagen", "ip", s.clientIP(r))
		msg := "Benutzername oder Passwort falsch."
		if errors.Is(err, auth.ErrDisabled) {
			msg = "Dieses Konto ist deaktiviert."
		}
		p.Errors = map[string]string{"form": msg}
		s.render(w, http.StatusUnauthorized, "login", p)
		return
	}
	s.loginUser.Reset(strings.ToLower(username))
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.DeleteSession(r.Context(), c.Value)
	}
	s.clearSessionCookie(w)
	s.redirect(w, r, "/login")
}

// ---------- Einladung & Registrierung ----------

func (s *Server) inviteForm(w http.ResponseWriter, r *http.Request) {
	if !s.auth.InviteValid(r.Context(), r.PathValue("token")) {
		s.inviteInvalid(w, r)
		return
	}
	p := s.plain(w, r, "Konto anlegen")
	p.Data = r.URL.Path
	s.render(w, http.StatusOK, "register", p)
}

func (s *Server) inviteInvalid(w http.ResponseWriter, r *http.Request) {
	p := s.plain(w, r, "Einladung ungültig")
	p.Data = map[string]string{"Code": "404", "Text": "Diese Einladung ist ungültig, abgelaufen oder wurde schon benutzt."}
	s.render(w, http.StatusNotFound, "error", p)
}

func (s *Server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	p := s.plain(w, r, "Konto anlegen")
	p.Data = r.URL.Path
	if s.rateLimited(w, r, "", "register", p) {
		return
	}
	id, err := s.auth.AcceptInvite(r.Context(), r.PathValue("token"), r.PostForm.Get("username"), r.PostForm.Get("password"))
	var verr auth.ValidationError
	switch {
	case errors.As(err, &verr):
		p.Errors = verr
		s.render(w, http.StatusUnprocessableEntity, "register", p)
		return
	case errors.Is(err, store.ErrNotFound):
		s.failAttempt(r, "")
		s.inviteInvalid(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.flash(w, "ok", "Konto angelegt. Willkommen!")
	s.redirect(w, r, "/")
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Registration != "open" {
		s.notFound(w, r)
		return
	}
	p := s.plain(w, r, "Konto anlegen")
	p.Data = "/register"
	s.render(w, http.StatusOK, "register", p)
}

func (s *Server) registerSubmit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Registration != "open" {
		s.notFound(w, r)
		return
	}
	p := s.plain(w, r, "Konto anlegen")
	p.Data = "/register"
	if s.rateLimited(w, r, "", "register", p) {
		return
	}
	s.failAttempt(r, "") // jede Registrierung zählt gegen das IP-Limit
	id, err := s.auth.CreateUser(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"), "member")
	var verr auth.ValidationError
	if errors.As(err, &verr) {
		p.Errors = verr
		s.render(w, http.StatusUnprocessableEntity, "register", p)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/")
}

// ---------- Einstellungen ----------

func (s *Server) settings(w http.ResponseWriter, r *http.Request, u *auth.User) {
	p := s.page(w, r, "Einstellungen")
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = s.bookmarklet(r)
	s.render(w, http.StatusOK, "settings", p)
}

// bookmarklet: Lesezeichen-Link, der markierten Text samt Titel und Quelle ins Formular schickt.
func (s *Server) bookmarklet(r *http.Request) template.URL {
	base := "http://" + r.Host
	if s.cfg.BaseURL != nil {
		base = s.cfg.BaseURL.Scheme + "://" + s.cfg.BaseURL.Host
	} else if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		base = "https://" + r.Host
	}
	js := "javascript:(function(){var s=String(window.getSelection()||'');window.open('" + base +
		"/p/new?text='+encodeURIComponent(s)+'&title='+encodeURIComponent(document.title)+'&source='+encodeURIComponent(location.href),'_blank');})();"
	return template.URL(js)
}

func (s *Server) settingsPassword(w http.ResponseWriter, r *http.Request, u *auth.User) {
	r.ParseForm()
	err := s.auth.ChangePassword(r.Context(), u.ID, r.PostForm.Get("current"), r.PostForm.Get("password"))
	var verr auth.ValidationError
	if errors.As(err, &verr) {
		p := s.page(w, r, "Einstellungen")
		if err := s.shell(r, p); err != nil {
			s.serverError(w, r, err)
			return
		}
		p.Errors = verr
		p.Data = s.bookmarklet(r)
		s.render(w, http.StatusUnprocessableEntity, "settings", p)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.flash(w, "ok", "Passwort geändert.")
	s.redirect(w, r, "/settings")
}

func (s *Server) settingsLogoutAll(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if err := s.auth.DeleteSessions(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	s.flash(w, "ok", "Auf allen Geräten abgemeldet.")
	s.redirect(w, r, "/login")
}

func (s *Server) prefs(w http.ResponseWriter, r *http.Request, u *auth.User) {
	r.ParseForm()
	st := u.Settings
	if v := r.PostForm.Get("theme"); v != "" {
		switch v {
		case "light", "dark":
			st.Theme = v
		default:
			st.Theme = ""
		}
	}
	if v := r.PostForm.Get("view"); v == "list" || v == "cards" {
		st.View = v
	}
	if r.PostForm.Get("ml_set") == "1" && s.cfg.MLAllowed {
		wasOn := st.ML
		st.ML = r.PostForm.Get("ml") == "1"
		if wasOn && !st.ML {
			if err := s.autotag.Forget(r.Context(), u.ID); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
	}
	if err := s.auth.SaveSettings(r.Context(), u.ID, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, localPath(r.PostForm.Get("back"), "/"))
}
