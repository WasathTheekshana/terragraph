// Package report defines the scan report the scanner CLI submits (see
// docs/design.md, section 5) and validates it.
package report

import (
	"errors"
	"fmt"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/source"
)

const SchemaVersion = 1

const (
	ScannerTypeModuleUsage = "module-usage"
	ScannerTypeModuleRepo  = "module-repo"

	SubjectKindProject    = "project"
	SubjectKindModuleRepo = "module_repo"

	FactTypeModuleCall = "module_call"
	FactTypeVersionTag = "version_tag"

	ResolutionSourceModulesJSON = "modules-json"
	ResolutionSourceParse       = "source-parse"
)

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	ScannerType   string    `json:"scanner_type"`
	Subject       Subject   `json:"subject"`
	GeneratedAt   time.Time `json:"generated_at"`
	Facts         []Fact    `json:"facts"`
}

type Subject struct {
	Kind      string `json:"kind"`
	RepoURL   string `json:"repo_url"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

type Fact struct {
	Type string `json:"type"`

	// module_call fields
	CallName         string `json:"call_name,omitempty"`
	Source           string `json:"source,omitempty"`
	RefDeclared      string `json:"ref_declared,omitempty"`
	RefResolved      string `json:"ref_resolved,omitempty"`
	VersionResolved  string `json:"version_resolved,omitempty"`
	ResolutionSource string `json:"resolution_source,omitempty"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line,omitempty"`

	// version_tag fields
	Tag       string `json:"tag,omitempty"`
	SemVer    string `json:"semver,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
}

// Validate reports every problem with r, so a pipeline author can fix them
// in one go.
func (r Report) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if r.SchemaVersion != SchemaVersion {
		add("schema_version %d is not supported (want %d)", r.SchemaVersion, SchemaVersion)
	}
	if r.GeneratedAt.IsZero() {
		add("generated_at is required")
	}
	if r.Subject.RepoURL == "" {
		add("subject.repo_url is required (pass --repo-url to the scanner if the checkout has no origin remote)")
	} else if source.RepoKey(r.Subject.RepoURL) == "" {
		add("subject.repo_url %q is not a recognizable repo URL", r.Subject.RepoURL)
	}

	switch r.ScannerType {
	case ScannerTypeModuleUsage:
		if r.Subject.Kind != SubjectKindProject {
			add("subject.kind must be %q for %s scans", SubjectKindProject, r.ScannerType)
		}
		seen := make(map[string]bool)
		for i, f := range r.Facts {
			if f.Type != FactTypeModuleCall {
				add("facts[%d].type must be %q", i, FactTypeModuleCall)
				continue
			}
			if f.CallName == "" {
				add("facts[%d].call_name is required", i)
			} else if seen[f.CallName] {
				add("facts[%d].call_name %q is duplicated", i, f.CallName)
			}
			seen[f.CallName] = true
			if f.Source == "" {
				add("facts[%d].source is required", i)
			}
			if f.ResolutionSource != ResolutionSourceModulesJSON && f.ResolutionSource != ResolutionSourceParse {
				add("facts[%d].resolution_source %q is not recognized", i, f.ResolutionSource)
			}
		}
	case ScannerTypeModuleRepo:
		if r.Subject.Kind != SubjectKindModuleRepo {
			add("subject.kind must be %q for %s scans", SubjectKindModuleRepo, r.ScannerType)
		}
		seen := make(map[string]bool)
		for i, f := range r.Facts {
			if f.Type != FactTypeVersionTag {
				add("facts[%d].type must be %q", i, FactTypeVersionTag)
				continue
			}
			if f.Tag == "" {
				add("facts[%d].tag is required", i)
			} else if seen[f.Tag] {
				add("facts[%d].tag %q is duplicated", i, f.Tag)
			}
			seen[f.Tag] = true
			if f.CommitSHA == "" {
				add("facts[%d].commit_sha is required", i)
			}
		}
	default:
		add("scanner_type %q is not supported", r.ScannerType)
	}

	return errors.Join(errs...)
}
