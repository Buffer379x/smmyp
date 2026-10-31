// Package web: HTTP-Server, Middleware, Handler und Templates.
package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/config"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg       config.Config
	db        *store.DB
	auth      *auth.Service
	prompts   *prompts.Service
	log       *slog.Logger
	version   string
	backupDir string
	tpl       map[string]*template.Template

	mu         sync.Mutex
	setupToken string

	loginIP   *auth.Limiter
	loginUser *auth.Limiter
}

type Options struct {
	Config    config.Config
	DB        *store.DB
	Logger    *slog.Logger
	Version   string
	BackupDir string
}

func New(o Options) (*Server, error) {
	s := &Server{
		cfg: o.Config, db: o.DB, log: o.Logger, version: o.Version, backupDir: o.BackupDir,
		auth:      &auth.Service{DB: o.DB},
		prompts:   &prompts.Service{DB: o.DB},
		loginIP:   auth.NewLimiter(10, time.Minute),
		loginUser: auth.NewLimiter(5, time.Minute),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetupToken liefert (und erzeugt bei Bedarf) das einmalige Token für das Erst-Setup.
func (s *Server) SetupToken(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, err := s.auth.UserCount(ctx); err != nil || n > 0 {
		s.setupToken = ""
		return ""
	}
	if s.setupToken == "" {
		s.setupToken = randomToken(9)
	}
	return s.setupToken
}

type ctxKey int

const userKey ctxKey = 1

func currentUser(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userKey).(*auth.User)
	return u
}

// Handler baut Routen und Middleware-Kette.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	fileServer := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fileServer.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "favicon.svg")
	})
	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /setup", s.setupForm)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /invite/{token}", s.inviteForm)
	mux.HandleFunc("POST /invite/{token}", s.inviteSubmit)
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.registerSubmit)

	for _, rt := range s.routes() {
		mux.Handle(rt.Pattern, rt.Handler)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.notFound(w, r) })

	cop := http.NewCrossOriginProtection()
	var h http.Handler = mux
	h = s.loadUser(h)
	h = cop.Handler(h)
	h = s.securityHeaders(h)
	h = s.hostCheck(h)
	h = s.logRequests(h)
	return s.recoverer(h)
}

type route struct {
	Pattern string
	Handler http.Handler
}

// routes: alle Seiten hinter dem Login. Der Isolations-Test (TC-ISO-05) läuft über genau diese Tabelle.
func (s *Server) routes() []route {
	u, a := s.requireUser, s.requireAdmin
	return []route{
		{"GET /{$}", u(s.browse)},
		{"GET /p/new", u(s.promptNew)},
		{"POST /p", u(s.promptCreate)},
		{"GET /p/{id}", u(s.promptShow)},
		{"GET /p/{id}/edit", u(s.promptEdit)},
		{"POST /p/{id}", u(s.promptUpdate)},
		{"POST /p/{id}/delete", u(s.promptTrash)},
		{"POST /p/{id}/restore", u(s.promptRestore)},
		{"POST /p/{id}/purge", u(s.promptPurge)},
		{"POST /p/{id}/fav", u(s.promptFav)},
		{"POST /p/{id}/used", u(s.promptUsed)},
		{"GET /trash", u(s.trash)},
		{"GET /export.json", u(s.export)},
		{"GET /settings", u(s.settings)},
		{"POST /settings/password", u(s.settingsPassword)},
		{"POST /settings/logout-all", u(s.settingsLogoutAll)},
		{"POST /prefs", u(s.prefs)},
		{"GET /providers", u(s.providersPage)},
		{"POST /providers", u(s.providerCreate)},
		{"POST /providers/{id}/delete", u(s.providerDelete)},
		{"GET /admin", a(s.admin)},
		{"POST /admin/invites", a(s.adminInvite)},
		{"POST /admin/users/{id}/disable", a(s.adminDisable)},
		{"POST /admin/users/{id}/enable", a(s.adminEnable)},
		{"POST /admin/backup", a(s.adminBackup)},
		{"GET /admin/backups/{name}", a(s.adminBackupDownload)},
	}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.R.PingContext(ctx); err != nil {
		http.Error(w, "db", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

// hostCheck schützt vor DNS-Rebinding: nur IP-Literale, localhost, der Host aus BASE_URL und SMMYP_ALLOWED_HOSTS.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusMisdirectedRequest)
			w.Write([]byte("SMMYP: Hostname nicht erlaubt. Ergänze ihn in SMMYP_ALLOWED_HOSTS oder setze SMMYP_BASE_URL.\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if s.cfg.BaseURL != nil && strings.EqualFold(s.cfg.BaseURL.Hostname(), host) {
		return true
	}
	for _, a := range s.cfg.AllowedHosts {
		if a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:])) {
			return true
		}
	}
	return false
}

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.cfg.BaseURL != nil && s.cfg.BaseURL.Scheme == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests protokolliert Methode, Pfad (ohne Query: keine Suchbegriffe/Prompt-Inhalte im Log), Status, Dauer.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		path := r.URL.Path
		if strings.HasPrefix(path, "/invite/") {
			path = "/invite/…"
		}
		s.log.Info("request", "method", r.Method, "path", path, "status", sw.status, "ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "path", r.URL.Path, "err", v)
				http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP berücksichtigt X-Forwarded-For nur, wenn die Verbindung von einem vertrauenswürdigen Proxy kommt.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !s.trusted(addr) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		a, err := netip.ParseAddr(p)
		if err != nil {
			break
		}
		if !s.trusted(a) {
			return p
		}
	}
	return host
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

const sessionCookie = "smmyp_session"

func (s *Server) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && !strings.HasPrefix(r.URL.Path, "/static/") {
			if u, err := s.auth.SessionUser(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey, &u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, *auth.User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			if n, _ := s.auth.UserCount(r.Context()); n == 0 {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
				return
			}
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				http.Error(w, "Nicht angemeldet", http.StatusUnauthorized)
			}
			return
		}
		h(w, r, u)
	})
}

func (s *Server) requireAdmin(h func(http.ResponseWriter, *http.Request, *auth.User)) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		if !u.Admin() {
			s.notFound(w, r)
			return
		}
		h(w, r, u)
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
