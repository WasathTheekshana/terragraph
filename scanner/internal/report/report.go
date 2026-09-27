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
	Kind      SubjectKind `json:"kind"`
	RepoURL   string      `json:"repo_url"`
	CommitSHA string      `json:"commit_sha,omitempty"`
	Branch    string      `json:"branch,omitempty"`
}

type FactType string

const (
	FactTypeModuleCall FactType = "module_call"
	FactTypeVersionTag FactType = "version_tag"
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
	CallName         string           `json:"call_name,omitempty"`
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
