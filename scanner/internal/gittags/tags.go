// Package gittags enumerates version tags on a remote git repo without
// cloning it, for scanning module repos (design.md §5, scanner_type
// "module-repo").
package gittags

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Tag is one tag found on the remote, with its semver parse if the tag name
// is valid semver (many real-world tags aren't, e.g. "latest" or "prod").
type Tag struct {
	Name      string
	CommitSHA string
	SemVer    string // "" if Name doesn't parse as semver
}

// List runs `git ls-remote --tags <repoURL>` and returns one Tag per ref,
// dereferencing annotated tags (the "^{}" peeled refs) so CommitSHA always
// points at the underlying commit rather than the tag object.
func List(repoURL string) ([]Tag, error) {
	out, err := exec.Command("git", "ls-remote", "--tags", repoURL).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote --tags %s: %w", repoURL, err)
	}

	byName := make(map[string]*Tag)
	var order []string

	for _, line := range strings.Split(string(out), "\n") {
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
	return tags, nil
}
