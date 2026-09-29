package main

import (
	"runtime/debug"
	"strings"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
// Otherwise it falls back to what the Go toolchain recorded at build time.
var version = ""

func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return "dev"
	}
	return "dev-" + strings.TrimSpace(rev[:min(len(rev), 12)]) + dirty
}
