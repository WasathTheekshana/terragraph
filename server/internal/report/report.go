// Package report defines the scan report the scanner CLI submits (see
// docs/design.md, section 5) and validates it.
package report

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
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
	FactTypeInventory  = "inventory"
	FactTypeResource   = "resource"
	FactTypeVariable   = "variable"
	FactTypeOutput     = "output"

	ModeManaged = "managed"
	ModeData    = "data"

	EnabledTrue    = "true"
	EnabledFalse   = "false"
	EnabledUnknown = "unknown"

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
	Kind    string `json:"kind"`
	RepoURL string `json:"repo_url"`
	// Path is the Terraform root's directory inside the repo, slash-separated.
	// Empty means the repo root.
	Path      string `json:"path,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

// ProjectPath is the subject's path in canonical form: "." for the repo root.
func (s Subject) ProjectPath() string {
	if s.Path == "" {
		return "."
	}
	return path.Clean(s.Path)
}

// ValidPath reports whether p is empty or a relative, slash-separated path that
// stays inside the repo.
func ValidPath(p string) bool {
	if p == "" {
		return true
	}
	c := path.Clean(p)
	return !strings.Contains(p, `\`) && !path.IsAbs(c) && c != ".." && !strings.HasPrefix(c, "../")
}

type Fact struct {
	Type string `json:"type"`

	// module_call fields
	CallName string `json:"call_name,omitempty"`
	// Parent is the dotted path of module calls this call is nested in, e.g.
	// "addons" for a call inside the module called "addons". Empty for calls
	// in the root itself.
	Parent           string `json:"parent,omitempty"`
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

	// inventory fields
	Backend  string `json:"backend,omitempty"`
	StateKey string `json:"state_key,omitempty"`

	// resource fields. Parent, File, and Line are shared with module calls.
	Address        string            `json:"address,omitempty"`
	Mode           string            `json:"mode,omitempty"`
	ResourceType   string            `json:"resource_type,omitempty"`
	Enabled        string            `json:"enabled,omitempty"`
	Instances      *int              `json:"instances,omitempty"`
	Condition      string            `json:"condition,omitempty"`
	Attrs          map[string]string `json:"attrs,omitempty"`
	Unresolved     map[string]string `json:"unresolved,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	TagsUnresolved map[string]string `json:"tags_unresolved,omitempty"`

	// variable and output fields; resources use Name too.
	Name        string   `json:"name,omitempty"`
	VarType     string   `json:"var_type,omitempty"`
	Value       string   `json:"value,omitempty"`
	ValueSource string   `json:"value_source,omitempty"`
	Sensitive   bool     `json:"sensitive,omitempty"`
	Flag        bool     `json:"flag,omitempty"`
	Controls    []string `json:"controls,omitempty"`
	Expr        string   `json:"expr,omitempty"`
}

// maxMapEntries bounds the attributes and tags of one resource.
const maxMapEntries = 200

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

	if !ValidPath(r.Subject.Path) {
		add("subject.path %q must be a relative, slash-separated path inside the repo", r.Subject.Path)
	}

	switch r.ScannerType {
	case ScannerTypeModuleUsage:
		if r.Subject.Kind != SubjectKindProject {
			add("subject.kind must be %q for %s scans", SubjectKindProject, r.ScannerType)
		}
		seen := make(map[string]bool)
		inventories := 0
		for i, f := range r.Facts {
			switch f.Type {
			case FactTypeModuleCall:
			case FactTypeInventory:
				inventories++
				if inventories > 1 {
					add("facts[%d]: a report has at most one %s fact", i, FactTypeInventory)
				}
				continue
			case FactTypeResource, FactTypeVariable, FactTypeOutput:
				if inventories == 0 {
					add("facts[%d]: a %s fact needs an %s fact before it", i, f.Type, FactTypeInventory)
				}
				validateInventoryFact(f, i, seen, add)
				continue
			default:
				add("facts[%d].type %q is not recognized", i, f.Type)
				continue
			}
			address := f.CallName
			if f.Parent != "" {
				address = f.Parent + "." + f.CallName
				if slices.Contains(strings.Split(f.Parent, "."), "") {
					add("facts[%d].parent %q must be dot-separated call names", i, f.Parent)
				}
			}
			if f.CallName == "" {
				add("facts[%d].call_name is required", i)
			} else if seen[address] {
				add("facts[%d] module call %q is duplicated", i, address)
			}
			seen[address] = true
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

// validateInventoryFact checks a resource, variable, or output fact. Their identities
// share the seen set with module calls, so each is prefixed with its kind.
func validateInventoryFact(f Fact, i int, seen map[string]bool, add func(string, ...any)) {
	if f.Parent != "" && slices.Contains(strings.Split(f.Parent, "."), "") {
		add("facts[%d].parent %q must be dot-separated call names", i, f.Parent)
	}
	switch f.Type {
	case FactTypeResource:
		if f.Address == "" || f.ResourceType == "" || f.Name == "" {
			add("facts[%d]: a resource needs address, resource_type, and name", i)
		}
		if f.Mode != ModeManaged && f.Mode != ModeData {
			add("facts[%d].mode %q must be %q or %q", i, f.Mode, ModeManaged, ModeData)
		}
		if f.Enabled != EnabledTrue && f.Enabled != EnabledFalse && f.Enabled != EnabledUnknown {
			add("facts[%d].enabled %q is not recognized", i, f.Enabled)
		}
		if f.Instances != nil && *f.Instances < 0 {
			add("facts[%d].instances must not be negative", i)
		}
		if len(f.Attrs) > maxMapEntries || len(f.Unresolved) > maxMapEntries || len(f.Tags) > maxMapEntries || len(f.TagsUnresolved) > maxMapEntries {
			add("facts[%d]: attrs and tags are limited to %d entries each", i, maxMapEntries)
		}
		if key := "resource:" + f.Address; f.Address != "" && seen[key] {
			add("facts[%d] resource %q is duplicated", i, f.Address)
		} else {
			seen[key] = true
		}
	case FactTypeVariable, FactTypeOutput:
		if f.Name == "" {
			add("facts[%d].name is required", i)
		}
		if key := f.Type + ":" + f.Parent + "/" + f.Name; seen[key] {
			add("facts[%d] %s %q is duplicated", i, f.Type, f.Name)
		} else {
			seen[key] = true
		}
	}
}
