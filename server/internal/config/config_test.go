package config

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TERRAGRAPH_DATABASE_URL": "postgres://localhost/terragraph",
		"TERRAGRAPH_INGEST_TOKEN": "secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.LogLevel != slog.LevelInfo || !slices.Equal(cfg.TrackedBranches, []string{"main", "master"}) {
		t.Errorf("cfg = %+v, want default addr, info level, main and master tracked", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TERRAGRAPH_ADDR":             ":9000",
		"TERRAGRAPH_DATABASE_URL":     "postgres://localhost/terragraph",
		"TERRAGRAPH_INGEST_TOKEN":     "secret",
		"TERRAGRAPH_TRACKED_BRANCHES": " main , release ,,",
		"TERRAGRAPH_LOG_LEVEL":        "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9000" || cfg.LogLevel != slog.LevelDebug || !slices.Equal(cfg.TrackedBranches, []string{"main", "release"}) {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadReportsAllMissing(t *testing.T) {
	_, err := Load(env(map[string]string{
		"TERRAGRAPH_TRACKED_BRANCHES": ",",
		"TERRAGRAPH_LOG_LEVEL":        "loud",
	}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"TERRAGRAPH_DATABASE_URL", "TERRAGRAPH_INGEST_TOKEN", "TERRAGRAPH_TRACKED_BRANCHES", "TERRAGRAPH_LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
