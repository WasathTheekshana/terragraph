// Package source normalizes Terraform module sources and git repo URLs into
// stable keys, so the same module is recognized however it's written.
package source

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

type Kind string

const (
	KindLocal    Kind = "local"
	KindGit      Kind = "git"
	KindRegistry Kind = "registry"
	KindOther    Kind = "other"
)

const defaultRegistryHost = "registry.terraform.io"

// scpLike matches git's scp-style syntax, e.g. git@github.com:org/repo.git.
var scpLike = regexp.MustCompile(`^[\w.-]+@([\w.-]+):(.+)$`)

// Parse returns the kind of a module source and its key. The key identifies
// the versioned unit: a git repo (without subdirectory or ref) or a registry
// module address. Local sources have no key.
//
// Keys are lowercased; the major git hosts and all registries treat these
// paths case-insensitively.
func Parse(src string) (Kind, string) {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		return KindLocal, ""
	}

	if getter, rest, ok := strings.Cut(src, "::"); ok && !strings.Contains(getter, "/") {
		key := RepoKey(rest)
		if getter == "git" {
			return KindGit, key
		}
		return KindOther, key
	}

	if scpLike.MatchString(stripQuery(src)) || strings.Contains(src, "://") {
		return KindGit, RepoKey(src)
	}

	addr := stripSubdir(stripQuery(src))
	lower := strings.ToLower(addr)
	for _, host := range []string{"github.com/", "bitbucket.org/"} {
		if strings.HasPrefix(lower, host) {
			return KindGit, RepoKey(src)
		}
	}

	parts := strings.Split(lower, "/")
	switch {
	case len(parts) == 3 && !strings.Contains(parts[0], "."):
		return KindRegistry, defaultRegistryHost + "/" + lower
	case len(parts) == 4 && strings.Contains(parts[0], "."):
		return KindRegistry, lower
	}
	return KindOther, strings.TrimSuffix(lower, ".git")
}

// RepoKey normalizes a git repo URL in any common form (https, ssh, scp-like,
// with or without git:: prefix, .git suffix, subdirectory, or ?ref=) to
// host/path, e.g. github.com/org/repo.
func RepoKey(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git::")
	s = stripSubdir(stripQuery(s))

	var host, path string
	if m := scpLike.FindStringSubmatch(s); m != nil && !strings.Contains(s, "://") {
		host, path = m[1], m[2]
	} else if u, err := url.Parse(s); err == nil && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else {
		host, path, _ = strings.Cut(s, "/")
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return strings.ToLower(host)
	}
	return strings.ToLower(host + "/" + path)
}

// Subdir returns the "//subdir" part of a module source, cleaned of slashes, or
// "" when the source names the whole repo or registry module. A repo can hold
// many modules, and this is what tells them apart: Parse gives them all the
// same key.
func Subdir(src string) string {
	s := stripQuery(strings.TrimSpace(src))
	start := 0
	if i := strings.Index(s, "://"); i >= 0 {
		start = i + 3
	}
	i := strings.Index(s[start:], "//")
	if i < 0 {
		return ""
	}
	dir := strings.Trim(s[start+i+2:], "/")
	if dir == "" {
		return ""
	}
	dir = path.Clean(dir)
	if dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
		return ""
	}
	return dir
}

func stripQuery(s string) string {
	s, _, _ = strings.Cut(s, "?")
	return s
}

// stripSubdir removes a go-getter "//subdir" suffix without touching the
// "//" that follows a URL scheme.
func stripSubdir(s string) string {
	start := 0
	if i := strings.Index(s, "://"); i >= 0 {
		start = i + 3
	}
	if i := strings.Index(s[start:], "//"); i >= 0 {
		return s[:start+i]
	}
	return s
}
