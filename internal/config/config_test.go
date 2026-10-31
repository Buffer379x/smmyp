package config

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.DataDir != "/data" || c.Registration != "invite" || c.BackupTime != "03:30" || c.BackupKeep != 14 || c.SecureCookies {
		t.Fatalf("Defaults: %+v", c)
	}
}

func TestParse(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SMMYP_BASE_URL": "https://prompts.home.example.org", "SMMYP_TRUSTED_PROXIES": "172.16.0.0/12, 10.0.0.5",
		"SMMYP_ALLOWED_HOSTS": "NAS.local,*.fritz.box", "SMMYP_BACKUP_TIME": "off",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.SecureCookies || len(c.TrustedProxies) != 2 || c.AllowedHosts[0] != "nas.local" || c.BackupTime != "" {
		t.Fatalf("%+v", c)
	}
	for k, v := range map[string]string{"SMMYP_BASE_URL": "ftp://x", "SMMYP_BACKUP_TIME": "25:00", "SMMYP_REGISTRATION": "alle",
		"SMMYP_TRUSTED_PROXIES": "kein-netz", "SMMYP_BACKUP_KEEP": "0", "SMMYP_LOG_LEVEL": "laut", "SMMYP_AUTH_PROXY_HEADER": "Remote-User"} {
		if _, err := Load(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s sollte abgelehnt werden", k, v)
		}
	}
}
