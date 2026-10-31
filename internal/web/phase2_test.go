package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Buffer379x/smmyp/internal/config"
)

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("JSON: %v: %s", err, body)
	}
}

// KI-Hilfe ist standardmäßig aus; erst nach dem Einschalten gibt es Vorschläge. Prüfungen laufen immer.
func TestMLOptInDefaultOff(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	form := url.Values{"title": {"x"}, "provider": {h.providerID("suno")}, "field_style": {"dark synthwave 98 bpm sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWX"}}
	_, body := h.do(c, "POST", "/api/suggest", form)
	var resp struct {
		ML      bool             `json:"ml"`
		Tags    []map[string]any `json:"tags"`
		Secrets []string         `json:"secrets"`
	}
	decode(t, body, &resp)
	if resp.ML || len(resp.Tags) != 0 {
		t.Fatalf("ohne Opt-in keine Vorschläge erwartet: %s", body)
	}
	if len(resp.Secrets) != 1 {
		t.Fatalf("Geheimnis-Prüfung muss unabhängig laufen: %s", body)
	}
	// Einschalten
	h.do(c, "POST", "/prefs", url.Values{"ml_set": {"1"}, "ml": {"1"}, "back": {"/settings"}})
	_, body = h.do(c, "POST", "/api/suggest", form)
	decode(t, body, &resp)
	if !resp.ML || len(resp.Tags) == 0 {
		t.Fatalf("nach Opt-in Vorschläge erwartet: %s", body)
	}
	_, page := h.do(c, "GET", "/settings", nil)
	if !strings.Contains(page, `name="ml" value="1" checked`) {
		t.Fatal("Schalter in den Einstellungen nicht gesetzt")
	}
	// Ausschalten löscht Modelle
	h.db.W.Exec(`INSERT INTO ml_models(owner_id, kind, version, trained_at, samples, quality, data) SELECT id, 'tags', 1, 0, 0, 0, x'00' FROM users WHERE username='anna'`)
	h.do(c, "POST", "/prefs", url.Values{"ml_set": {"1"}, "back": {"/settings"}})
	var n int
	h.db.R.QueryRow("SELECT count(*) FROM ml_models").Scan(&n)
	if n != 0 {
		t.Fatal("Modelle nach dem Ausschalten nicht gelöscht")
	}
	// Instanzweit abgeschaltet: Option nicht sichtbar, kein Einschalten möglich.
	h2 := newHarness(t, func(c *config.Config) { c.MLAllowed = false })
	c2 := h2.user("ben", "member")
	h2.do(c2, "POST", "/prefs", url.Values{"ml_set": {"1"}, "ml": {"1"}, "back": {"/"}})
	if _, page := h2.do(c2, "GET", "/settings", nil); strings.Contains(page, `name="ml"`) {
		t.Fatal("Option trotz SMMYP_ML=off sichtbar")
	}
	_, body = h2.do(c2, "POST", "/api/suggest", form)
	decode(t, body, &resp)
	if resp.ML {
		t.Fatal("KI-Hilfe trotz SMMYP_ML=off aktiv")
	}
}

// TC-SEC-04: Proxy-Header nur von vertrauenswürdigen Proxys.
func TestProxyLoginTC_SEC_04(t *testing.T) {
	trusted := newHarness(t, func(c *config.Config) {
		c.ProxyHeader, c.ProxyAutoUser = "Remote-User", true
		c.TrustedProxies = mustPrefixes("127.0.0.0/8")
	})
	resp, body := trusted.do(trusted.client(), "GET", "/", nil, "Remote-User", "sso-anna")
	if resp.StatusCode != 200 || !strings.Contains(body, "Schubladen sind noch leer") {
		t.Fatalf("Proxy-Login vom vertrauenswürdigen Proxy: %d", resp.StatusCode)
	}
	var role string
	trusted.db.R.QueryRow("SELECT role FROM users WHERE username = 'sso-anna'").Scan(&role)
	if role != "admin" {
		t.Fatalf("erster Proxy-Benutzer sollte Admin sein, ist %q", role)
	}
	untrusted := newHarness(t, func(c *config.Config) {
		c.ProxyHeader, c.ProxyAutoUser = "Remote-User", true
		c.TrustedProxies = mustPrefixes("10.9.9.0/24")
	})
	untrusted.user("echt", "admin")
	resp, _ = untrusted.do(untrusted.client(), "GET", "/", nil, "Remote-User", "echt")
	if resp.StatusCode == 200 {
		t.Fatal("Header von nicht vertrauenswürdiger Adresse wurde akzeptiert")
	}
}

func TestImportJSONAndCSV(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	upload := func(name, content string) (int, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write([]byte(content))
		mw.Close()
		req, _ := http.NewRequest("POST", h.ts.URL+"/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b := new(bytes.Buffer)
		b.ReadFrom(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, b.String()
	}
	code, body := upload("x.json", `{"format":"smmyp-export","version":1,"prompts":[{"title":"Importiert","providers":["suno"],"fields":{"style":"dark"},"tags":["lofi"]}]}`)
	if code != 200 || !strings.Contains(body, "<b>1</b> importiert") {
		t.Fatalf("JSON-Import: %d", code)
	}
	code, body = upload("x.csv", "\uFEFFtitle;providers;style;tags\nCSV-Prompt;suno, udio;neon synthwave;synthwave\nOhne Anbieter;;x;\n")
	if code != 200 || !strings.Contains(body, "<b>1</b> importiert") || !strings.Contains(body, "kein bekannter Anbieter") {
		t.Fatalf("CSV-Import: %d %s", code, body[strings.Index(body, "Ergebnis"):][:300])
	}
	_, body = h.do(c, "GET", "/?q=%23lo-fi", nil)
	if !strings.Contains(body, "Importiert") {
		t.Fatal("importierter Prompt nicht auffindbar (Alias lofi → lo-fi)")
	}
	if code, _ := upload("x.json", `{"prompts":[]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("fremdes Format: %d", code)
	}
}

func TestShareManifestBookmarklet(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	resp, _ := h.do(c, "GET", "/share?title=Song&text=%5BVerse%5D%20neon&url=https%3A%2F%2Fsuno.com%2Fsong%2F1", nil)
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/p/new?") || !strings.Contains(loc, "source=https") {
		t.Fatalf("Share-Redirect: %q", loc)
	}
	_, body := h.do(c, "GET", loc, nil)
	sunoID := h.providerID("suno")
	if !strings.Contains(body, `value="`+sunoID+`" data-fields="style,lyrics,exclude" checked`) || !strings.Contains(body, "[Verse] neon") {
		t.Fatal("Formular nicht mit Suno und Text vorbelegt")
	}
	resp, body = h.do(c, "GET", "/manifest.webmanifest", nil)
	var m map[string]any
	decode(t, body, &m)
	if m["share_target"] == nil || resp.Header.Get("Content-Type") != "application/manifest+json" {
		t.Fatal("Manifest")
	}
	if resp, _ := h.do(c, "GET", "/sw.js", nil); resp.StatusCode != 200 || resp.Header.Get("Service-Worker-Allowed") != "/" {
		t.Fatal("Service Worker")
	}
	_, body = h.do(c, "GET", "/settings", nil)
	if !strings.Contains(body, `href="javascript:%28function`) || !strings.Contains(body, "/p/new?text=") {
		t.Fatal("Bookmarklet fehlt oder wurde unbrauchbar escaped")
	}
}

func TestVersionsVarsBulkCollections(t *testing.T) {
	h := newHarness(t)
	c := h.user("anna", "member")
	id := h.createPrompt(c, "Lo-Fi", "suno", "style", "lo-fi {{instrument=piano}}, 72 bpm")
	h.do(c, "POST", "/p/"+id, url.Values{"title": {"Lo-Fi"}, "provider": {h.providerID("suno")}, "field_style": {"lo-fi {{instrument=piano}}, 80 bpm"}})
	_, body := h.do(c, "GET", "/p/"+id+"/versions", nil)
	if !strings.Contains(body, `<del class="diff-del">72</del>`) || !strings.Contains(body, `<ins class="diff-ins">80</ins>`) {
		t.Fatal("Diff fehlt")
	}
	// Platzhalter
	_, body = h.do(c, "GET", "/p/"+id, nil)
	if !strings.Contains(body, "data-vars") {
		t.Fatal("Platzhalter-Markierung fehlt")
	}
	req, _ := http.NewRequest("POST", h.ts.URL+"/p/"+id+"/vars", strings.NewReader(`{"instrument":"rhodes"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := c.Do(req); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("vars speichern: %d", resp.StatusCode)
	}
	_, body = h.do(c, "GET", "/p/"+id+"/vars", nil)
	if !strings.Contains(body, `"name":"instrument","default":"piano","last":"rhodes"`) {
		t.Fatalf("vars: %s", body)
	}
	// Sammlung per Mehrfachauswahl
	id2 := h.createPrompt(c, "Zweiter", "claude", "prompt", "x")
	resp, _ := h.do(c, "POST", "/bulk", url.Values{"id": {id, id2}, "action": {"collection"}, "new_collection": {"Album"}, "back": {"/"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bulk: %d", resp.StatusCode)
	}
	var colID string
	h.db.R.QueryRow("SELECT id FROM collections WHERE name = 'Album'").Scan(&colID)
	_, body = h.do(c, "GET", "/c/"+colID, nil)
	if !strings.Contains(body, "Lo-Fi") || !strings.Contains(body, "Zweiter") || !strings.Contains(body, "Album") {
		t.Fatal("Sammlungsansicht unvollständig")
	}
	// Bulk-Tag und Papierkorb
	h.do(c, "POST", "/bulk", url.Values{"id": {id, id2}, "action": {"tag"}, "tag": {"projekt"}, "back": {"/"}})
	if _, body := h.do(c, "GET", "/?q=%23projekt", nil); !strings.Contains(body, "2 Prompts") {
		t.Fatal("Bulk-Tag")
	}
	h.do(c, "POST", "/bulk", url.Values{"id": {id2}, "action": {"trash"}, "back": {"/"}})
	if _, body := h.do(c, "GET", "/trash", nil); !strings.Contains(body, "Zweiter") {
		t.Fatal("Bulk-Papierkorb")
	}
	// Seiten rendern ohne Fehler
	for _, p := range []string{"/tags", "/c", "/import", "/p/new", "/api/palette?q=lo"} {
		if resp, _ := h.do(c, "GET", p, nil); resp.StatusCode != 200 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	_ = context.Background()
}
