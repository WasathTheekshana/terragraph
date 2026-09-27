// Package config loads server settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

type Config struct {
	Addr            string
	DatabaseURL     string
	IngestToken     string
	TrackedBranches []string
	LogLevel        slog.Level
}

// Load reads the configuration using getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:            withDefault(getenv("TERRAGRAPH_ADDR"), ":8080"),
		DatabaseURL:     getenv("TERRAGRAPH_DATABASE_URL"),
		IngestToken:     getenv("TERRAGRAPH_INGEST_TOKEN"),
		TrackedBranches: splitList(withDefault(getenv("TERRAGRAPH_TRACKED_BRANCHES"), "main,master")),
	}

	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("TERRAGRAPH_DATABASE_URL is required"))
	}
	if cfg.IngestToken == "" {
		errs = append(errs, errors.New("TERRAGRAPH_INGEST_TOKEN is required"))
	}
	if len(cfg.TrackedBranches) == 0 {
		errs = append(errs, errors.New("TERRAGRAPH_TRACKED_BRANCHES must list at least one branch"))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("TERRAGRAPH_LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("TERRAGRAPH_LOG_LEVEL: %w", err))
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
