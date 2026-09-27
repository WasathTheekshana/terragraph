// Package moduleinit does best-effort resolution of what a module call
// resolved to, by reading .terraform/modules/modules.json (only present after
// `terraform init` or `terraform get`) and inspecting the checked-out module
// dir. Absent that, callers fall back to the literal ref parsed from source.
package moduleinit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type manifestEntry struct {
	Key     string `json:"Key"`
	Source  string `json:"Source"`
	Version string `json:"Version"`
	Dir     string `json:"Dir"`
}

// Resolution is what Terraform actually installed for a module call.
type Resolution struct {
	// Commit is the checked-out git commit, when the module was installed
	// as its own git clone.
	Commit string
	// Version is the exact version Terraform selected for a registry module.
	Version string
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

// Resolve reports what the module call named callName was installed as. The
// second result is false when there's nothing beyond the source to go on.
func (m *Manifest) Resolve(callName string) (Resolution, bool) {
	if m == nil {
		return Resolution{}, false
	}
	entry, ok := m.entries[callName]
	if !ok {
		return Resolution{}, false
	}

	r := Resolution{Version: entry.Version}
	if entry.Dir != "" {
		r.Commit = m.commit(filepath.Join(m.rootDir, entry.Dir))
	}
	return r, r.Commit != "" || r.Version != ""
}

// commit returns HEAD of dir, or "" unless dir is inside its own git clone
// under .terraform/modules.
func (m *Manifest) commit(dir string) string {
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
