package report

import (
	"strings"
	"testing"
	"time"
)

func validUsage() Report {
	return Report{
		SchemaVersion: SchemaVersion,
		ScannerType:   ScannerTypeModuleUsage,
		Subject:       Subject{Kind: SubjectKindProject, RepoURL: "git@github.com:org/project-a.git", Branch: "main"},
		GeneratedAt:   time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Facts: []Fact{
			{Type: FactTypeModuleCall, CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", ResolutionSource: ResolutionSourceParse},
		},
	}
}

func validRepo() Report {
	return Report{
		SchemaVersion: SchemaVersion,
		ScannerType:   ScannerTypeModuleRepo,
		Subject:       Subject{Kind: SubjectKindModuleRepo, RepoURL: "https://github.com/org/vpc.git"},
		GeneratedAt:   time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Facts:         []Fact{{Type: FactTypeVersionTag, Tag: "v1.0.0", CommitSHA: "abc"}},
	}
}

func TestValidateAcceptsValidReports(t *testing.T) {
	for name, r := range map[string]Report{"usage": validUsage(), "repo": validRepo()} {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Report)
		wantErr string
	}{
		{"schema version", func(r *Report) { r.SchemaVersion = 2 }, "schema_version 2"},
		{"missing generated_at", func(r *Report) { r.GeneratedAt = time.Time{} }, "generated_at is required"},
		{"missing repo url", func(r *Report) { r.Subject.RepoURL = "" }, "--repo-url"},
		{"unknown scanner", func(r *Report) { r.ScannerType = "cost" }, `scanner_type "cost"`},
		{"wrong subject kind", func(r *Report) { r.Subject.Kind = SubjectKindModuleRepo }, "subject.kind"},
		{"wrong fact type", func(r *Report) { r.Facts[0].Type = FactTypeVersionTag }, "facts[0].type"},
		{"missing call name", func(r *Report) { r.Facts[0].CallName = "" }, "facts[0].call_name is required"},
		{"duplicate call name", func(r *Report) { r.Facts = append(r.Facts, r.Facts[0]) }, `"vpc" is duplicated`},
		{"missing source", func(r *Report) { r.Facts[0].Source = "" }, "facts[0].source is required"},
		{"bad resolution source", func(r *Report) { r.Facts[0].ResolutionSource = "lockfile" }, "resolution_source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validUsage()
			tt.mutate(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRepoRejectsDuplicateTags(t *testing.T) {
	r := validRepo()
	r.Facts = append(r.Facts, r.Facts[0])
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), `"v1.0.0" is duplicated`) {
		t.Errorf("Validate() = %v, want duplicate tag error", err)
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	r := validUsage()
	r.SchemaVersion = 0
	r.Subject.RepoURL = ""
	err := r.Validate()
	if err == nil || strings.Count(err.Error(), "\n") < 1 {
		t.Errorf("Validate() = %v, want multiple errors", err)
	}
}
