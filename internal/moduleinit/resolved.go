// Package moduleinit does best-effort resolution of the exact commit a
// module call resolved to, by reading .terraform/modules/modules.json (only
// present after `terraform init`) and inspecting the checked-out module dir.
// Absent that, callers fall back to the literal ref parsed from source.
package moduleinit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type manifestEntry struct {
	Key    string `json:"Key"`
	Source string `json:"Source"`
	Dir    string `json:"Dir"`
}

type manifest struct {
	Modules []manifestEntry `json:"Modules"`
}

// Manifest maps a module call's Key (its call name for top-level calls) to
// the checked-out directory Terraform resolved it to.
type Manifest struct {
	entries map[string]manifestEntry
	rootDir string
}

// Load reads rootDir/.terraform/modules/modules.json. Returns (nil, nil) if
// it doesn't exist, which is normal when the project hasn't been
// initialized in this pipeline run.
func Load(rootDir string) (*Manifest, error) {
	path := filepath.Join(rootDir, ".terraform", "modules", "modules.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	entries := make(map[string]manifestEntry, len(m.Modules))
	for _, e := range m.Modules {
		entries[e.Key] = e
	}
	return &Manifest{entries: entries, rootDir: rootDir}, nil
}

// ResolvedCommit returns the git commit SHA the module call named callName
// resolved to, by running `git rev-parse HEAD` inside its checked-out
// directory. Returns "" if there's no manifest entry, the module isn't its
// own git checkout under .terraform/modules, or git isn't available.
func (m *Manifest) ResolvedCommit(callName string) string {
	if m == nil {
		return ""
	}
	entry, ok := m.entries[callName]
	if !ok || entry.Dir == "" {
		return ""
	}

	dir := filepath.Join(m.rootDir, entry.Dir)
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	// Without this, a local module (or any dir lacking its own .git) would
	// report the enclosing project's commit.
	modulesDir, err := filepath.Abs(filepath.Join(m.rootDir, ".terraform", "modules"))
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(modulesDir, filepath.Clean(top))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}

	sha, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
