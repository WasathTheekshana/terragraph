// Package hclscan extracts `module` block declarations from a Terraform
// root directory without requiring `terraform init`.
package hclscan

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-config-inspect/tfconfig"
)

// ModuleCall is one `module "x" { ... }` block found in the source.
type ModuleCall struct {
	CallName string
	Source   string
	// RefDeclared is whatever we could determine pins the version: the
	// `?ref=` query param for git/http sources, or the `version` attribute
	// for registry sources. Empty if unpinned.
	RefDeclared string
	File        string
	Line        int
}

// refPattern pulls a `ref=` query param out of git-style module sources,
// which are often not valid net/url targets (e.g. `git@host:org/repo.git//sub?ref=v1`).
var refPattern = regexp.MustCompile(`[?&]ref=([^&\s]+)`)

// Scan reads every .tf file directly under dir (non-recursive, matching how
// Terraform itself scopes a single module/root) and returns its module calls.
func Scan(dir string) ([]ModuleCall, error) {
	module, diags := tfconfig.LoadModule(dir)
	if diags.HasErrors() {
		return nil, diagError(dir, diags)
	}

	calls := make([]ModuleCall, 0, len(module.ModuleCalls))
	for name, mc := range module.ModuleCalls {
		ref := mc.Version
		if m := refPattern.FindStringSubmatch(mc.Source); m != nil {
			ref = m[1]
		}

		calls = append(calls, ModuleCall{
			CallName:    name,
			Source:      mc.Source,
			RefDeclared: ref,
			File:        relPath(dir, mc.Pos.Filename),
			Line:        mc.Pos.Line,
		})
	}

	sort.Slice(calls, func(i, j int) bool {
		if calls[i].File != calls[j].File {
			return calls[i].File < calls[j].File
		}
		return calls[i].Line < calls[j].Line
	})
	return calls, nil
}

// diagError describes the parse errors as "file:line: summary", with files
// relative to dir; the caller already knows which directory it scanned.
func diagError(dir string, diags tfconfig.Diagnostics) error {
	var msgs []string
	for _, d := range diags {
		if d.Severity != tfconfig.DiagError {
			continue
		}
		msg := d.Summary
		if d.Pos != nil && d.Pos.Filename != "" {
			msg = fmt.Sprintf("%s:%d: %s", relPath(dir, d.Pos.Filename), d.Pos.Line, d.Summary)
		}
		msgs = append(msgs, msg)
	}
	return errors.New(strings.Join(msgs, "; "))
}

// relPath keeps reported paths identical across runners and OSes.
func relPath(dir, file string) string {
	if file == "" {
		return ""
	}
	if rel, err := filepath.Rel(dir, file); err == nil {
		file = rel
	}
	return filepath.ToSlash(file)
}
