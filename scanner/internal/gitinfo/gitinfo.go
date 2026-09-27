// Package gitinfo reads local git metadata (commit, branch, remote) so the
// CLI can auto-fill scan report fields when a pipeline doesn't pass them
// explicitly (e.g. via --commit/--branch/--repo-url flags).
package gitinfo

import (
	"os/exec"
	"strings"
)

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitSHA returns the current HEAD commit in dir, or "" if unavailable.
func CommitSHA(dir string) string {
	sha, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// Branch returns the current branch name in dir, or "" if unavailable
// (e.g. detached HEAD in a CI checkout).
func Branch(dir string) string {
	branch, err := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "HEAD" {
		return ""
	}
	return branch
}

// RemoteURL returns the URL of the named remote (typically "origin") in dir,
// or "" if unavailable.
func RemoteURL(dir, remoteName string) string {
	url, err := run(dir, "remote", "get-url", remoteName)
	if err != nil {
		return ""
	}
	return url
}
