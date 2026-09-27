// Package gitinfo reads local git metadata (repo root, commit, branch,
// remote) so scan reports can identify what was scanned.
package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo is what a scan needs to know about a git checkout.
type Repo struct {
	Root      string
	RemoteURL string
	CommitSHA string
	Branch    string
}

// FindRoot returns the checkout containing dir by looking for .git in dir and
// its parents. .git is a file in worktrees and submodules, so either counts.
func FindRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Read returns the metadata of the checkout rooted at root. Fields git can't
// provide (no origin remote, detached HEAD) are left empty.
func Read(root string) Repo {
	return Repo{
		Root:      root,
		RemoteURL: RemoteURL(root, "origin"),
		CommitSHA: CommitSHA(root),
		Branch:    Branch(root),
	}
}

func run(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
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
