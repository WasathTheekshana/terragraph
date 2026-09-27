package gittags

import (
	"regexp"
	"strings"
)

// scpLike matches git's scp-style syntax, e.g. git@github.com:org/repo.git.
var scpLike = regexp.MustCompile(`^[\w.-]+@[\w.-]+:.+$`)

// SourceURL returns the git remote a Terraform module source is fetched
// from, or false when the source isn't a git repo (a local path, a registry
// module, an HTTP archive, ...).
func SourceURL(source string) (string, bool) {
	s := strings.TrimSpace(source)
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return "", false
	}

	forcedGit := false
	if getter, rest, ok := strings.Cut(s, "::"); ok && !strings.Contains(getter, "/") {
		if getter != "git" {
			return "", false
		}
		s, forcedGit = rest, true
	}
	s = stripSubdir(stripQuery(s))

	switch {
	case scpLike.MatchString(s), strings.HasPrefix(s, "ssh://"):
		return s, true
	case forcedGit && strings.Contains(s, "://"):
		return s, true
	}
	// Terraform's shorthand for GitHub and Bitbucket clones over HTTPS.
	for _, host := range []string{"github.com/", "bitbucket.org/"} {
		if strings.HasPrefix(strings.ToLower(s), host) {
			if !strings.HasSuffix(s, ".git") {
				s += ".git"
			}
			return "https://" + s, true
		}
	}
	return "", false
}

func stripQuery(s string) string {
	s, _, _ = strings.Cut(s, "?")
	return s
}

// stripSubdir removes a "//subdir" suffix without touching the "//" after a
// URL scheme.
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
