// Package report defines the JSON schema the scanner CLI emits, matching
// docs/design.md section 5.
package report

import "time"

const SchemaVersion = 1

type ScannerType string

const (
	ScannerTypeModuleUsage ScannerType = "module-usage"
	ScannerTypeModuleRepo  ScannerType = "module-repo"
)

type SubjectKind string

const (
	SubjectKindProject    SubjectKind = "project"
	SubjectKindModuleRepo SubjectKind = "module_repo"
)

type Subject struct {
	Kind    SubjectKind `json:"kind"`
	RepoURL string      `json:"repo_url"`
	// Path is the Terraform root's directory inside the repo, slash-separated,
	// "." for the repo root. Empty for module repos.
	Path      string `json:"path,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

type FactType string

const (
	FactTypeModuleCall FactType = "module_call"
	FactTypeVersionTag FactType = "version_tag"

	// An inventory fact opens a project report's resource section, saying where the
	// project keeps its state. Its resource, variable, and output facts follow.
	FactTypeInventory FactType = "inventory"
	FactTypeResource  FactType = "resource"
	FactTypeVariable  FactType = "variable"
	FactTypeOutput    FactType = "output"
)

// ResolutionSource records how a module call's ref was determined.
type ResolutionSource string

const (
	// Set when the ref was cross-checked against modules.json after a
	// terraform init, so ref_resolved is an exact commit.
	ResolutionSourceModulesJSON ResolutionSource = "modules-json"
	// Set when the ref came only from parsing the literal module block.
	ResolutionSourceParse ResolutionSource = "source-parse"
)

// Fact is a single observation. Only the fields relevant to Type are set.
type Fact struct {
	Type FactType `json:"type"`

	// module_call fields
	CallName string `json:"call_name,omitempty"`
	// Parent is the dotted path of module calls this call is made inside,
	// e.g. "addons" for a call in the module called "addons". Empty for calls
	// in the root itself.
	Parent           string           `json:"parent,omitempty"`
	Source           string           `json:"source,omitempty"`
	RefDeclared      string           `json:"ref_declared,omitempty"`
	RefResolved      string           `json:"ref_resolved,omitempty"`
	VersionResolved  string           `json:"version_resolved,omitempty"`
	ResolutionSource ResolutionSource `json:"resolution_source,omitempty"`
	File             string           `json:"file,omitempty"`
	Line             int              `json:"line,omitempty"`

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

type ScanReport struct {
	SchemaVersion int         `json:"schema_version"`
	ScannerType   ScannerType `json:"scanner_type"`
	Subject       Subject     `json:"subject"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Facts         []Fact      `json:"facts"`
}

func New(scannerType ScannerType, subject Subject, facts []Fact) ScanReport {
	return ScanReport{
		SchemaVersion: SchemaVersion,
		ScannerType:   scannerType,
		Subject:       subject,
		GeneratedAt:   time.Now().UTC(),
		Facts:         facts,
	}
}
