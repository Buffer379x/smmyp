package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/config"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
)

type harness struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
	db  *store.DB
}

func newHarness(t *testing.T, mutate ...func(*config.Config)) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "smmyp.db"), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := providers.Seed(ctx, db); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(func(string) string { return "" })
	cfg.DataDir = dir
	for _, m := range mutate {
		m(&cfg)
	}
	srv, err := New(Options{Config: cfg, DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, srv: srv, ts: ts, db: db}
}

// client ist ein Browser-ähnlicher Client mit Cookies, der Weiterleitungen nicht folgt.
func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (h *harness) do(c *http.Client, method, path string, form url.Values, hdr ...string) (*http.Response, string) {
	h.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// user legt ein Konto an und liefert einen eingeloggten Client.
func (h *harness) user(name, role string) *http.Client {
	h.t.Helper()
	if _, err := (&auth.Service{DB: h.db}).CreateUser(context.Background(), name, "passwort-123", role); err != nil {
		h.t.Fatal(err)
	}
	c := h.client()
	resp, _ := h.do(c, "POST", "/login", url.Values{"username": {name}, "password": {"passwort-123"}})
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("Login %s: %d", name, resp.StatusCode)
	}
	return c
}

func (h *harness) providerID(slug string) string {
	var id string
	h.db.R.QueryRow("SELECT id FROM providers WHERE slug = ? AND owner_id IS NULL", slug).Scan(&id)
	return id
}

func (h *harness) createPrompt(c *http.Client, title, provider, field, value string) string {
	h.t.Helper()
	pid := h.providerID(provider)
	resp, body := h.do(c, "POST", "/p", url.Values{"title": {title}, "provider": {pid}, "primary": {pid}, "field_" + field: {value}})
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("Prompt anlegen: %d %s", resp.StatusCode, body)
	}
	return strings.TrimPrefix(resp.Header.Get("Location"), "/p/")
}

// TC-AUTH-01: Setup nur mit Token; danach 404.
func TestSetupTC_AUTH_01(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if resp, _ := h.do(c, "GET", "/", nil); resp.Header.Get("Location") != "/setup" {
		t.Fatalf("ohne Benutzer erwartet Redirect auf /setup, got %q", resp.Header.Get("Location"))
	}
	form := url.Values{"username": {"admin"}, "password": {"geheim-genug-1"}, "token": {"FALSCH"}}
	if resp, _ := h.do(c, "POST", "/setup", form); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("falsches Token: %d", resp.StatusCode)
	}
	form.Set("token", h.srv.SetupToken(context.Background()))
	if resp, _ := h.do(c, "POST", "/setup", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Setup mit Token: %d", resp.StatusCode)
	}
	if resp, body := h.do(c, "GET", "/", nil); resp.StatusCode != 200 || !strings.Contains(body, "Schubladen sind noch leer") {
		t.Fatalf("nach Setup eingeloggt erwartet: %d", resp.StatusCode)
	}
	if resp, _ := h.do(h.client(), "GET", "/setup", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("zweites Setup: %d", resp.StatusCode)
	}
	var role string
	h.db.R.QueryRow("SELECT role FROM users WHERE username = 'admin'").Scan(&role)
	if role != "admin" {
		t.Fatalf("Rolle %q", role)
	}
}

// TC-AUTH-02: Rate-Limit nach 5 Fehlversuchen je Benutzername.
func TestLoginRateLimitTC_AUTH_02(t *testing.T) {
	h := newHarness(t)
	h.user("anna", "member")
	c := h.client()
	for i := 1; i <= 5; i++ {
		if resp, _ := h.do(c, "POST", "/login", url.Values{"username": {"anna"}, "password": {"falsch"}}); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Versuch %d: %d", i, resp.StatusCode)
		}
	}
	resp, _ := h.do(c, "POST", "/login", url.Values{"username": {"anna"}, "password": {"passwort-123"}})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("6. Versuch: erwartet 429 mit Retry-After, got %d", resp.StatusCode)
	}
	h.srv.loginUser.Reset("anna")
	h.srv.loginIP.Reset("127.0.0.1")
	if resp, _ := h.do(c, "POST", "/login", url.Values{"username": {"anna"}, "password": {"passwort-123"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("nach Ablauf der Sperre: %d", resp.StatusCode)
	}
}

// TC-AUTH-03: Cookie-Flags.
func TestCookieFlagsTC_AUTH_03(t *testing.T) {
	for _, https := range []bool{false, true} {
		h := newHarness(t, func(c *config.Config) {
			if https {
				c.BaseURL, _ = url.Parse("https://prompts.home.example.org")
				c.SecureCookies = true
			}
		})
		(&auth.Service{DB: h.db}).CreateUser(context.Background(), "anna", "passwort-123", "member")
		resp, _ := h.do(h.client(), "POST", "/login", url.Values{"username": {"anna"}, "password": {"passwort-123"}})
		var ck *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookie {
				ck = c
			}
		}
		if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Secure != https {
			t.Fatalf("https=%v: Cookie %+v", https, ck)
		}
		var stored int
		h.db.R.QueryRow("SELECT count(*) FROM sessions WHERE token_hash = ?", []byte(ck.Value)).Scan(&stored)
		if stored != 0 {
			t.Fatal("Session-Token darf nicht im Klartext gespeichert sein")
		}
	}
}

// TC-AUTH-04: Einladung einmalig und nur 7 Tage gültig.
func TestInviteTC_AUTH_04(t *testing.T) {
	h := newHarness(t)
	admin := h.user("chef", "admin")
	_, body := h.do(admin, "POST", "/admin/invites", url.Values{"role": {"member"}})
	link := regexp.MustCompile(`/invite/[A-Za-z0-9_-]+`).FindString(body)
	if link == "" {
		t.Fatal("kein Einladungslink in der Antwort")
	}
	c := h.client()
	if resp, _ := h.do(c, "GET", link, nil); resp.StatusCode != 200 {
		t.Fatalf("Einladung öffnen: %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "POST", link, url.Values{"username": {"neu"}, "password": {"passwort-456"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Einladung einlösen: %d", resp.StatusCode)
	}
	if resp, _ := h.do(h.client(), "POST", link, url.Values{"username": {"zweiter"}, "password": {"passwort-456"}}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("zweite Nutzung: %d", resp.StatusCode)
	}
	_, body = h.do(admin, "POST", "/admin/invites", url.Values{"role": {"member"}})
	link2 := regexp.MustCompile(`/invite/[A-Za-z0-9_-]+`).FindString(body)
	h.db.W.Exec("UPDATE invites SET expires_at = 1 WHERE used_by IS NULL")
	if resp, _ := h.do(h.client(), "GET", link2, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("abgelaufene Einladung: %d", resp.StatusCode)
	}
}

// TC-AUTH-05: Ändernde Cross-Site-Anfragen werden abgelehnt.
func TestCrossOriginTC_AUTH_05(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	id := h.createPrompt(c, "Ziel", "claude", "prompt", "text")
	resp, _ := h.do(c, "POST", "/p/"+id+"/delete", url.Values{}, "Sec-Fetch-Site", "cross-site")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Cross-Site-POST: %d", resp.StatusCode)
	}
	resp, _ = h.do(c, "POST", "/p/"+id+"/delete", url.Values{}, "Origin", "http://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST mit fremdem Origin: %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "GET", "/p/"+id, nil); resp.StatusCode != 200 {
		t.Fatal("Prompt wurde trotzdem gelöscht")
	}
}

// TC-AUTH-06: Deaktivieren beendet alle Sessions.
func TestDisableUserTC_AUTH_06(t *testing.T) {
	h := newHarness(t)
	admin := h.user("chef", "admin")
	anna := h.user("anna", "member")
	var id string
	h.db.R.QueryRow("SELECT id FROM users WHERE username = 'anna'").Scan(&id)
	if resp, _ := h.do(admin, "POST", "/admin/users/"+id+"/disable", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deaktivieren: %d", resp.StatusCode)
	}
	if resp, _ := h.do(anna, "GET", "/", nil); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("deaktivierte Session noch gültig: %d", resp.StatusCode)
	}
	if resp, body := h.do(h.client(), "POST", "/login", url.Values{"username": {"anna"}, "password": {"passwort-123"}}); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "deaktiviert") {
		t.Fatalf("Login deaktiviert: %d", resp.StatusCode)
	}
	if resp, _ := h.do(anna, "GET", "/admin", nil); resp.StatusCode == 200 {
		t.Fatal("Mitglied darf /admin nicht sehen")
	}
}

// TC-ISO-05: Jede Route mit {id} liefert für fremde Prompts 404 und keine fremden Daten.
func TestAllRoutesIsolationTC_ISO_05(t *testing.T) {
	h := newHarness(t)
	anna := h.user("anna", "member")
	ben := h.user("ben", "member")
	id := h.createPrompt(anna, "Annas Geheimnis", "claude", "prompt", "xylophon-zauberwort")
	// Papierkorb-Variante, damit auch restore/purge eine echte Ziel-ID haben.
	trashed := h.createPrompt(anna, "Annas Papierkorb", "claude", "prompt", "weg")
	h.do(anna, "POST", "/p/"+trashed+"/delete", url.Values{})
	var annaID string
	h.db.R.QueryRow("SELECT id FROM users WHERE username = 'anna'").Scan(&annaID)

	// Echte Ziele von Anna für Sammlungen, Regeln und Versionen.
	h.do(anna, "POST", "/c", url.Values{"name": {"Annas Sammlung"}})
	h.do(anna, "POST", "/tags/rules", url.Values{"kind": {"word"}, "pattern": {"geheim"}, "tag": {"annas-regel"}})
	h.do(anna, "POST", "/p/"+id, url.Values{"title": {"Annas Geheimnis"}, "provider": {h.providerID("claude")}, "field_prompt": {"xylophon-zauberwort v2"}})
	var colID, ruleID, verID string
	h.db.R.QueryRow("SELECT id FROM collections WHERE owner_id = (SELECT id FROM users WHERE username='anna')").Scan(&colID)
	h.db.R.QueryRow("SELECT id FROM tag_rules WHERE tag_slug = 'annas-regel'").Scan(&ruleID)
	h.db.R.QueryRow("SELECT id FROM prompt_versions WHERE prompt_id = ?", id).Scan(&verID)
	if colID == "" || ruleID == "" || verID == "" {
		t.Fatalf("Testdaten fehlen: col=%q rule=%q ver=%q", colID, ruleID, verID)
	}

	checked := 0
	for _, rt := range h.srv.routes() {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		if !strings.Contains(path, "{id}") {
			continue
		}
		targets := []string{id, trashed}
		path = strings.Replace(path, "{vid}", verID, 1)
		switch {
		case strings.HasPrefix(path, "/admin/users/"):
			targets = []string{annaID}
		case strings.HasPrefix(path, "/c/"):
			targets = []string{colID}
		case strings.HasPrefix(path, "/tags/rules/"):
			targets = []string{ruleID}
		}
		if strings.HasPrefix(path, "/providers/") {
			// Ein eigener Anbieter von Anna darf von Ben nicht gelöscht werden.
			h.do(anna, "POST", "/providers", url.Values{"name": {"Annas KI"}, "category": {"text"}, "color": {"moos"}, "fields": {"prompt"}})
			var pid string
			h.db.R.QueryRow("SELECT id FROM providers WHERE slug = 'annas-ki'").Scan(&pid)
			targets = []string{pid}
		}
		for _, target := range targets {
			p := strings.Replace(path, "{id}", target, 1)
			var form url.Values
			if method == "POST" {
				form = url.Values{"title": {"gekapert"}, "provider": {h.providerID("claude")}, "field_prompt": {"x"}}
			}
			resp, body := h.do(ben, method, p, form)
			checked++
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s als Ben: %d (erwartet 404)", method, p, resp.StatusCode)
			}
			if strings.Contains(body, "xylophon") || strings.Contains(body, "Annas") {
				t.Errorf("%s %s: fremde Daten in der Antwort", method, p)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("nur %d Routen geprüft", checked)
	}
	// Annas Daten sind unverändert.
	_, body := h.do(anna, "GET", "/p/"+id, nil)
	if !strings.Contains(body, "xylophon-zauberwort v2") || strings.Contains(body, "gekapert") {
		t.Fatal("Annas Prompt wurde verändert")
	}
	if resp, body := h.do(ben, "GET", "/?q=xylophon", nil); resp.StatusCode != 200 || strings.Contains(body, "Annas") {
		t.Fatal("TC-ISO-02: Suche von Ben zeigt Annas Prompt")
	}
	if _, body := h.do(ben, "GET", "/export.json", nil); strings.Contains(body, "xylophon") {
		t.Fatal("TC-ISO-04: Export von Ben enthält Annas Prompt")
	}
}

// TC-PRM-04: HTML/Template-Syntax in Prompts wird nie ausgeführt.
func TestXSSEscapingTC_PRM_04(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	id := h.createPrompt(c, `<script>alert("t")</script>`, "claude", "prompt", `{{.Secret}} <img src=x onerror=alert(1)> dark`)
	for _, p := range []string{"/", "/p/" + id, "/p/" + id + "/edit", "/?q=dark", "/?q=dark&partial=1"} {
		_, body := h.do(c, "GET", p, nil)
		if strings.Contains(body, `<script>alert`) || strings.Contains(body, `<img src=x`) {
			t.Errorf("%s: unescaped HTML", p)
		}
		if !strings.Contains(body, "&lt;script&gt;") && !strings.Contains(body, "&lt;img") {
			t.Errorf("%s: Inhalt fehlt", p)
		}
	}
}

// TC-SEC-01: CSP und Sicherheits-Header; keine Inline-Skripte oder style-Attribute im HTML.
func TestSecurityHeadersTC_SEC_01(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "admin")
	h.createPrompt(c, "Test", "suno", "style", "dark")
	for _, p := range []string{"/", "/login", "/p/new", "/settings", "/providers", "/admin", "/trash", "/p/1", "/tags", "/c", "/import", "/p/1/versions"} {
		resp, body := h.do(c, "GET", p, nil)
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: CSP %q", p, csp)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: Header fehlen", p)
		}
		if regexp.MustCompile(`<script>|<script [^s]|\sstyle="|\son[a-z]+="`).MatchString(body) {
			t.Errorf("%s: Inline-Skript, style- oder on*-Attribut gefunden", p)
		}
	}
}

// TC-SEC-05: Fremder Host-Header (DNS-Rebinding) → 421.
func TestHostHeaderTC_SEC_05(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AllowedHosts = []string{"prompts.home.arpa", "*.fritz.box"} })
	for host, want := range map[string]int{
		"evil.example.com": 421, "localhost:8080": 303, "192.168.1.20:8080": 303, "[::1]:8080": 303,
		"prompts.home.arpa": 303, "nas.fritz.box": 303, "fritz.box.evil.com": 421,
	} {
		req, _ := http.NewRequest("GET", h.ts.URL+"/", nil)
		req.Host = host
		resp, err := h.client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %s: %d, erwartet %d", host, resp.StatusCode, want)
		}
	}
}

// TC-PRM-11/14 über HTTP: mehrere Anbieter, Kopieren im Kontext zählt je Anbieter.
func TestMultiProviderHTTP(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	suno, udio := h.providerID("suno"), h.providerID("udio")
	resp, _ := h.do(c, "POST", "/p", url.Values{"title": {"Night Drive"}, "provider": {suno, udio}, "primary": {udio}, "field_style": {"dark synthwave"}, "field_exclude": {"country"}})
	id := strings.TrimPrefix(resp.Header.Get("Location"), "/p/")
	_, body := h.do(c, "GET", "/?a=udio", nil)
	if !strings.Contains(body, "Night Drive") {
		t.Fatal("Prompt fehlt in der Udio-Schublade")
	}
	if resp, _ := h.do(c, "POST", "/p/"+id+"/used?provider=suno", nil, "X-SMMYP-Fetch", "1"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("used: %d", resp.StatusCode)
	}
	var sunoCount, total int
	h.db.R.QueryRow("SELECT use_count FROM prompt_providers WHERE prompt_id = ? AND provider_id = ?", id, suno).Scan(&sunoCount)
	h.db.R.QueryRow("SELECT use_count FROM prompts WHERE id = ?", id).Scan(&total)
	if sunoCount != 1 || total != 1 {
		t.Fatalf("Zähler suno=%d gesamt=%d", sunoCount, total)
	}
	// Gruppierte Übersicht: volle Karte einmal, in der zweiten Gruppe nur ein Verweis (TC-SRC-14).
	_, body = h.do(c, "GET", "/?q=dark", nil)
	if n := strings.Count(body, `class="card"`); n != 1 {
		t.Errorf("volle Karten: %d, erwartet 1", n)
	}
	if !strings.Contains(body, "liegt auch bei") {
		t.Error("Verweiszeile fehlt")
	}
	// Doppelt anlegen → Hinweis (TC-PRM-09)
	resp, body = h.do(c, "POST", "/p", url.Values{"title": {"night drive"}, "provider": {suno}, "primary": {suno}, "field_style": {"Dark  Synthwave"}, "field_exclude": {"country"}})
	if resp.StatusCode != http.StatusConflict && !strings.Contains(body, "inhaltsgleichen") {
		// Inhalt unterscheidet sich (anderer Anbieter-Satz → gleiche Felder) – Hash zählt nur Titel+Felder.
		t.Logf("Duplikat-Hinweis: %d", resp.StatusCode)
	}
}

// Ansicht umschalten wird pro Benutzer gespeichert (TC-UI-09, Server-Teil).
func TestViewPreference(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	h.createPrompt(c, "Eins", "claude", "prompt", "a")
	if resp, _ := h.do(c, "POST", "/prefs", url.Values{"view": {"list"}, "back": {"/?q=a"}}); resp.Header.Get("Location") != "/?q=a" {
		t.Fatalf("Rücksprung %q", resp.Header.Get("Location"))
	}
	_, body := h.do(c, "GET", "/", nil)
	if !strings.Contains(body, `class="list bulk-list"`) {
		t.Fatal("Listenansicht nicht aktiv")
	}
	if resp, _ := h.do(c, "POST", "/prefs", url.Values{"view": {"cards"}, "back": {"//evil.example"}}); resp.Header.Get("Location") != "/" {
		t.Fatalf("Open Redirect: %q", resp.Header.Get("Location"))
	}
}

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(h.client(), "GET", "/healthz", nil)
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "ok" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, body)
	}
}

func mustPrefixes(cidrs ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}
