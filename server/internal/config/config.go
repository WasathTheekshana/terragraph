// Package config loads server settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Addr        string
	DatabaseURL string
	// IngestToken is an optional static token with read and ingest access,
	// for bootstrapping before API tokens exist.
	IngestToken     string
	TrackedBranches []string
	LogLevel        slog.Level
	// PublicURL is where users reach the server, e.g. https://terragraph.acme.io.
	PublicURL string
	Auth      Auth
}

type Auth struct {
	// Disabled opens the UI and API to everyone. Only for local use.
	Disabled    bool
	OIDC        *OIDC
	GitHubOIDC  *GitHubOIDC
	AdminEmails []string
	SessionTTL  time.Duration
}

// OIDC is the identity provider people sign in with.
type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// AllowedDomains, if set, limits sign-in to these email domains.
	AllowedDomains []string
	// AdminGroup, if set, makes members of this group (in GroupsClaim) admins.
	AdminGroup  string
	GroupsClaim string
}

// GitHubOIDC lets GitHub Actions workflows in Owners submit scans with
// GitHub's ID token instead of a stored secret.
type GitHubOIDC struct {
	Owners   []string
	Audience string
}

// Load reads the configuration using getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:            withDefault(getenv("TERRAGRAPH_ADDR"), ":8080"),
		DatabaseURL:     getenv("TERRAGRAPH_DATABASE_URL"),
		IngestToken:     getenv("TERRAGRAPH_INGEST_TOKEN"),
		TrackedBranches: splitList(withDefault(getenv("TERRAGRAPH_TRACKED_BRANCHES"), "main,master")),
		PublicURL:       strings.TrimRight(getenv("TERRAGRAPH_PUBLIC_URL"), "/"),
		Auth: Auth{
			Disabled:    getenv("TERRAGRAPH_AUTH_DISABLED") == "true",
			AdminEmails: lowerList(getenv("TERRAGRAPH_ADMIN_EMAILS")),
		},
	}

	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if cfg.DatabaseURL == "" {
		add("TERRAGRAPH_DATABASE_URL is required")
	}
	if len(cfg.TrackedBranches) == 0 {
		add("TERRAGRAPH_TRACKED_BRANCHES must list at least one branch")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("TERRAGRAPH_LOG_LEVEL"), "info"))); err != nil {
		add("TERRAGRAPH_LOG_LEVEL: %w", err)
	}

	ttl, err := time.ParseDuration(withDefault(getenv("TERRAGRAPH_SESSION_TTL"), "12h"))
	if err != nil || ttl < time.Minute {
		add("TERRAGRAPH_SESSION_TTL must be a duration of at least 1m, e.g. 12h")
	}
	cfg.Auth.SessionTTL = ttl

	if issuer := getenv("TERRAGRAPH_OIDC_ISSUER"); issuer != "" {
		cfg.Auth.OIDC = &OIDC{
			Issuer:         issuer,
			ClientID:       getenv("TERRAGRAPH_OIDC_CLIENT_ID"),
			ClientSecret:   getenv("TERRAGRAPH_OIDC_CLIENT_SECRET"),
			AllowedDomains: lowerList(getenv("TERRAGRAPH_OIDC_ALLOWED_DOMAINS")),
			AdminGroup:     getenv("TERRAGRAPH_OIDC_ADMIN_GROUP"),
			GroupsClaim:    withDefault(getenv("TERRAGRAPH_OIDC_GROUPS_CLAIM"), "groups"),
		}
		if cfg.Auth.OIDC.ClientID == "" {
			add("TERRAGRAPH_OIDC_CLIENT_ID is required with TERRAGRAPH_OIDC_ISSUER")
		}
		if u, err := url.Parse(cfg.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			add("TERRAGRAPH_PUBLIC_URL must be the server's absolute URL (e.g. https://terragraph.acme.io) when sign-in is on")
		}
	}

	if owners := lowerList(getenv("TERRAGRAPH_GITHUB_OIDC_OWNERS")); len(owners) > 0 {
		cfg.Auth.GitHubOIDC = &GitHubOIDC{
			Owners:   owners,
			Audience: withDefault(getenv("TERRAGRAPH_GITHUB_OIDC_AUDIENCE"), "terragraph"),
		}
	}

	switch {
	case cfg.Auth.Disabled && cfg.Auth.OIDC != nil:
		add("TERRAGRAPH_AUTH_DISABLED and TERRAGRAPH_OIDC_ISSUER can't both be set")
	case !cfg.Auth.Disabled && cfg.Auth.OIDC == nil:
		add("sign-in isn't configured: set TERRAGRAPH_OIDC_ISSUER (and client settings), or TERRAGRAPH_AUTH_DISABLED=true for local use only")
	}
	return cfg, errors.Join(errs...)
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func lowerList(s string) []string {
	return splitList(strings.ToLower(s))
}
