// Package gittags enumerates version tags on a remote git repo without
// cloning it, for scanning module repos (design.md §5, scanner_type
// "module-repo").
package gittags

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// Tag is one tag found on the remote, with its semver parse if the tag name
// is valid semver (many real-world tags aren't, e.g. "latest" or "prod").
type Tag struct {
	Name      string
	CommitSHA string
	SemVer    string // "" if Name doesn't parse as semver
}

// lsRemoteTimeout bounds one remote; a hung connection shouldn't stall a scan.
const lsRemoteTimeout = 2 * time.Minute

// List runs `git ls-remote --tags <repoURL>` and returns one Tag per tag.
// Git is never allowed to prompt for credentials: the scanner runs
// unattended, so missing access fails with git's own message instead.
func List(ctx context.Context, repoURL string) ([]Tag, error) {
	ctx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", repoURL)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := gitError(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git ls-remote: %s", msg)
		}
		return nil, fmt.Errorf("git ls-remote: %w", err)
	}
	return parse(string(out)), nil
}

// gitError keeps the lines of git's stderr up to the first "fatal:", which
// carry the cause (e.g. "Permission denied (publickey)"), and drops the
// generic advice git prints after it.
func gitError(stderr string) string {
	var kept []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kept = append(kept, line)
		if strings.HasPrefix(line, "fatal:") {
			break
		}
	}
	return strings.Join(kept, " ")
}

// parse reads ls-remote output, dereferencing annotated tags (the "^{}"
// peeled refs) so CommitSHA is the tagged commit, not the tag object.
func parse(out string) []Tag {
	byName := make(map[string]*Tag)
	var order []string

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sha, ref := fields[0], fields[1]

		const prefix = "refs/tags/"
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		name := strings.TrimPrefix(ref, prefix)

		peeled := strings.HasSuffix(name, "^{}")
		name = strings.TrimSuffix(name, "^{}")

		t, exists := byName[name]
		if !exists {
			t = &Tag{Name: name}
			byName[name] = t
			order = append(order, name)
		}
		// A peeled ref (annotated tag dereferenced to its commit) should win
		// over the tag object's own SHA.
		if peeled || t.CommitSHA == "" {
			t.CommitSHA = sha
		}
	}

	tags := make([]Tag, 0, len(order))
	for _, name := range order {
		t := *byName[name]
		if v, err := semver.NewVersion(t.Name); err == nil {
			t.SemVer = v.String()
		}
		tags = append(tags, t)
	}
	return tags
}
