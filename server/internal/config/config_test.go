package config

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func base(extra map[string]string) map[string]string {
	vars := map[string]string{
		"TERRAGRAPH_DATABASE_URL":  "postgres://localhost/terragraph",
		"TERRAGRAPH_AUTH_DISABLED": "true",
	}
	for k, v := range extra {
		vars[k] = v
	}
	return vars
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(base(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.LogLevel != slog.LevelInfo || !slices.Equal(cfg.TrackedBranches, []string{"main", "master"}) {
		t.Errorf("cfg = %+v, want default addr, info level, main and master tracked", cfg)
	}
	if !cfg.Auth.Disabled || cfg.Auth.OIDC != nil || cfg.Auth.GitHubOIDC != nil || cfg.Auth.SessionTTL != 12*time.Hour || cfg.IngestToken != "" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(base(map[string]string{
		"TERRAGRAPH_ADDR":             ":9000",
		"TERRAGRAPH_INGEST_TOKEN":     "secret",
		"TERRAGRAPH_TRACKED_BRANCHES": " main , release ,,",
		"TERRAGRAPH_LOG_LEVEL":        "debug",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9000" || cfg.LogLevel != slog.LevelDebug || !slices.Equal(cfg.TrackedBranches, []string{"main", "release"}) || cfg.IngestToken != "secret" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadOIDC(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TERRAGRAPH_DATABASE_URL":         "postgres://localhost/terragraph",
		"TERRAGRAPH_PUBLIC_URL":           "https://terragraph.acme.io/",
		"TERRAGRAPH_OIDC_ISSUER":          "https://acme.okta.com",
		"TERRAGRAPH_OIDC_CLIENT_ID":       "tg",
		"TERRAGRAPH_OIDC_CLIENT_SECRET":   "s",
		"TERRAGRAPH_OIDC_ALLOWED_DOMAINS": "Acme.io, acme.co.uk",
		"TERRAGRAPH_OIDC_ADMIN_GROUP":     "platform-admins",
		"TERRAGRAPH_ADMIN_EMAILS":         "Alice@Acme.io",
		"TERRAGRAPH_SESSION_TTL":          "8h",
		"TERRAGRAPH_GITHUB_OIDC_OWNERS":   "Acme",
	}))
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Auth.OIDC
	if o == nil || o.Issuer != "https://acme.okta.com" || !slices.Equal(o.AllowedDomains, []string{"acme.io", "acme.co.uk"}) ||
		o.AdminGroup != "platform-admins" || o.GroupsClaim != "groups" {
		t.Errorf("oidc = %+v", o)
	}
	if cfg.PublicURL != "https://terragraph.acme.io" || cfg.Auth.SessionTTL != 8*time.Hour ||
		!slices.Equal(cfg.Auth.AdminEmails, []string{"alice@acme.io"}) {
		t.Errorf("cfg = %+v", cfg)
	}
	if g := cfg.Auth.GitHubOIDC; g == nil || !slices.Equal(g.Owners, []string{"acme"}) || g.Audience != "terragraph" {
		t.Errorf("github oidc = %+v", g)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"nothing set", map[string]string{"TERRAGRAPH_TRACKED_BRANCHES": ",", "TERRAGRAPH_LOG_LEVEL": "loud"},
			[]string{"TERRAGRAPH_DATABASE_URL", "TERRAGRAPH_TRACKED_BRANCHES", "TERRAGRAPH_LOG_LEVEL", "sign-in isn't configured"}},
		{"oidc without client or public url", map[string]string{"TERRAGRAPH_DATABASE_URL": "x", "TERRAGRAPH_OIDC_ISSUER": "https://idp"},
			[]string{"TERRAGRAPH_OIDC_CLIENT_ID", "TERRAGRAPH_PUBLIC_URL"}},
		{"both open and oidc", map[string]string{"TERRAGRAPH_DATABASE_URL": "x", "TERRAGRAPH_AUTH_DISABLED": "true",
			"TERRAGRAPH_OIDC_ISSUER": "https://idp", "TERRAGRAPH_OIDC_CLIENT_ID": "c", "TERRAGRAPH_PUBLIC_URL": "https://tg"},
			[]string{"can't both be set"}},
		{"bad session ttl", base(map[string]string{"TERRAGRAPH_SESSION_TTL": "5s"}), []string{"TERRAGRAPH_SESSION_TTL"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.vars))
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %s", err, w)
				}
			}
		})
	}
}
