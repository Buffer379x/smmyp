// Package config liest die Konfiguration aus Umgebungsvariablen (SMMYP_*).
package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Config struct {
	Listen         string
	DataDir        string
	BaseURL        *url.URL // nil, wenn nicht gesetzt
	SecureCookies  bool
	TrustedProxies []netip.Prefix
	AllowedHosts   []string // zusätzlich zu IP-Literalen, localhost und dem Host aus BaseURL
	Registration   string   // invite | open | closed
	BackupTime     string   // "HH:MM" oder "" (aus)
	BackupKeep     int
	LogLevel       slog.Level
	AdminUser      string
	AdminPassword  string
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Load liest die Konfiguration. getenv ist austauschbar, damit Tests ohne echte Umgebung auskommen.
func Load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	c := Config{
		Listen:       get("SMMYP_LISTEN", ":8080"),
		DataDir:      get("SMMYP_DATA_DIR", "/data"),
		Registration: get("SMMYP_REGISTRATION", "invite"),
		BackupTime:   get("SMMYP_BACKUP_TIME", "03:30"),
		AdminUser:    get("SMMYP_ADMIN_USER", ""),
	}
	if getenv("SMMYP_BACKUP_TIME") == "off" {
		c.BackupTime = ""
	}
	if c.BackupTime != "" && !hhmm.MatchString(c.BackupTime) {
		return c, fmt.Errorf("SMMYP_BACKUP_TIME: %q ist keine Uhrzeit im Format HH:MM", c.BackupTime)
	}
	keep, err := strconv.Atoi(get("SMMYP_BACKUP_KEEP", "14"))
	if err != nil || keep < 1 {
		return c, fmt.Errorf("SMMYP_BACKUP_KEEP muss eine Zahl ≥ 1 sein")
	}
	c.BackupKeep = keep

	switch c.Registration {
	case "invite", "open", "closed":
	default:
		return c, fmt.Errorf("SMMYP_REGISTRATION: %q (erlaubt: invite, open, closed)", c.Registration)
	}

	if raw := get("SMMYP_BASE_URL", ""); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("SMMYP_BASE_URL: %q ist keine gültige http(s)-Adresse", raw)
		}
		c.BaseURL = u
	}
	secure := get("SMMYP_SECURE_COOKIES", "auto")
	switch secure {
	case "auto":
		c.SecureCookies = c.BaseURL != nil && c.BaseURL.Scheme == "https"
	case "true", "1", "on":
		c.SecureCookies = true
	case "false", "0", "off":
	default:
		return c, fmt.Errorf("SMMYP_SECURE_COOKIES: %q (erlaubt: auto, true, false)", secure)
	}

	for _, p := range splitList(getenv("SMMYP_TRUSTED_PROXIES")) {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return c, fmt.Errorf("SMMYP_TRUSTED_PROXIES: %q ist weder CIDR noch IP", p)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix)
	}
	for _, h := range splitList(getenv("SMMYP_ALLOWED_HOSTS")) {
		c.AllowedHosts = append(c.AllowedHosts, strings.ToLower(h))
	}

	switch strings.ToLower(get("SMMYP_LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		return c, fmt.Errorf("SMMYP_LOG_LEVEL: erlaubt sind debug, info, warn, error")
	}

	if f := get("SMMYP_ADMIN_PASSWORD_FILE", ""); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return c, fmt.Errorf("SMMYP_ADMIN_PASSWORD_FILE: %w", err)
		}
		c.AdminPassword = strings.TrimRight(string(b), "\r\n")
	}
	return c, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
