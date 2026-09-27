package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/WasathTheekshana/terragraph/server/internal/report"
)

// newTestStore returns a migrated store in a fresh schema of the database at
// TERRAGRAPH_TEST_DATABASE_URL, dropped when the test ends.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TERRAGRAPH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TERRAGRAPH_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(b)

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), url)
		if err != nil {
			t.Errorf("dropping schema: %v", err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("dropping schema: %v", err)
		}
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	s := New(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

var t0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func projectScan(repoURL, branch string, at time.Time, calls ...report.Fact) report.Report {
	for i := range calls {
		calls[i].Type = report.FactTypeModuleCall
		if calls[i].ResolutionSource == "" {
			calls[i].ResolutionSource = report.ResolutionSourceParse
		}
	}
	return report.Report{
		SchemaVersion: report.SchemaVersion,
		ScannerType:   report.ScannerTypeModuleUsage,
		Subject:       report.Subject{Kind: report.SubjectKindProject, RepoURL: repoURL, Branch: branch, CommitSHA: "c0ffee"},
		GeneratedAt:   at,
		Facts:         calls,
	}
}

func repoScan(repoURL string, at time.Time, tags ...string) report.Report {
	facts := make([]report.Fact, len(tags))
	for i, tag := range tags {
		facts[i] = report.Fact{Type: report.FactTypeVersionTag, Tag: tag, CommitSHA: "sha-" + tag}
	}
	return report.Report{
		SchemaVersion: report.SchemaVersion,
		ScannerType:   report.ScannerTypeModuleRepo,
		Subject:       report.Subject{Kind: report.SubjectKindModuleRepo, RepoURL: repoURL},
		GeneratedAt:   at,
		Facts:         facts,
	}
}

func mustIngest(t *testing.T, s *Store, r report.Report, tracked bool) IngestResult {
	t.Helper()
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid test report: %v", err)
	}
	res, err := s.Ingest(context.Background(), Scan{Report: r, Tracked: tracked})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestIngestAndQuery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mustIngest(t, s, repoScan("https://github.com/org/tf-module-vpc.git", t0,
		"v1.0.0", "v2.0.0", "v2.1.0", "v3.0.0-rc.1", "latest"), false)
	res := mustIngest(t, s, projectScan("git@github.com:org/project-a.git", "main", t0,
		report.Fact{CallName: "vpc", Source: "git::https://github.com/org/tf-module-vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"},
		report.Fact{CallName: "vpc_edge", Source: "git@github.com:org/tf-module-vpc.git?ref=v2.1.0", RefDeclared: "v2.1.0"},
		report.Fact{CallName: "helpers", Source: "./modules/helpers"},
		report.Fact{CallName: "s3", Source: "terraform-aws-modules/s3-bucket/aws", RefDeclared: "~> 4.0",
			VersionResolved: "4.11.0", ResolutionSource: report.ResolutionSourceModulesJSON},
	), true)
	if !res.Applied {
		t.Fatal("tracked scan was not applied")
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	p := projects[0]
	if p.ModuleCalls != 4 || p.OutdatedCalls != 1 || p.MajorBehindCalls != 1 || p.LastBranch != "main" {
		t.Errorf("project = %+v, want 4 calls, 1 outdated, 1 major behind, branch main", p)
	}

	usages, err := s.ProjectUsages(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		pinned, latest any
		majors         any
		outdated       bool
		hasModule      bool
	}
	wants := map[string]want{
		"vpc":      {"1.0.0", "2.1.0", 1, true, true},
		"vpc_edge": {"2.1.0", "2.1.0", 0, false, true},
		"helpers":  {nil, nil, nil, false, false},
		"s3":       {"4.11.0", nil, nil, false, true},
	}
	if len(usages) != len(wants) {
		t.Fatalf("got %d usages, want %d", len(usages), len(wants))
	}
	for _, u := range usages {
		w, ok := wants[u.CallName]
		if !ok {
			t.Errorf("unexpected usage %q", u.CallName)
			continue
		}
		got := want{deref(u.PinnedVersion), deref(u.LatestVersion), deref(u.MajorsBehind), u.Outdated, u.ModuleID != nil}
		if got != w {
			t.Errorf("%s: got %+v, want %+v", u.CallName, got, w)
		}
	}

	modules, err := s.ListModules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var vpc *Module
	for i := range modules {
		if modules[i].Key == "github.com/org/tf-module-vpc" {
			vpc = &modules[i]
		}
	}
	if len(modules) != 2 || vpc == nil {
		t.Fatalf("modules = %+v, want the vpc repo and the s3 registry module", modules)
	}
	if deref(vpc.LatestTag) != "v2.1.0" || vpc.Consumers != 1 || vpc.OutdatedConsumers != 1 || vpc.Kind != "git" {
		t.Errorf("vpc module = %+v, want latest v2.1.0, 1 consumer, 1 outdated", *vpc)
	}

	gotProject, err := s.GetProject(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(gotProject, p) {
		t.Errorf("GetProject(%d) = %+v, %v; want %+v", p.ID, gotProject, err, p)
	}
	gotModule, err := s.GetModule(ctx, vpc.ID)
	if err != nil || gotModule.Key != vpc.Key || gotModule.Consumers != vpc.Consumers {
		t.Errorf("GetModule(%d) = %+v, %v; want %+v", vpc.ID, gotModule, err, *vpc)
	}

	consumers, err := s.ModuleConsumers(ctx, vpc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 2 || consumers[0].ProjectRepoURL != "git@github.com:org/project-a.git" {
		t.Errorf("consumers = %+v, want both vpc calls from project-a", consumers)
	}
}

func TestIngestAppliesOnlyTrackedAndNewer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const repo = "https://github.com/org/project-b.git"
	vpc := report.Fact{CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"}
	eks := report.Fact{CallName: "eks", Source: "git::https://github.com/org/eks.git?ref=v1.0.0", RefDeclared: "v1.0.0"}

	callNames := func() []string {
		t.Helper()
		projects, err := s.ListProjects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		usages, err := s.ProjectUsages(ctx, projects[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, len(usages))
		for i, u := range usages {
			names[i] = u.CallName
		}
		return names
	}

	steps := []struct {
		name        string
		scan        report.Report
		tracked     bool
		wantApplied bool
		wantCalls   int
	}{
		{"first tracked scan", projectScan(repo, "main", t0, vpc, eks), true, true, 2},
		{"untracked branch", projectScan(repo, "feature/x", t0.Add(time.Hour), vpc), false, false, 2},
		{"older tracked scan", projectScan(repo, "main", t0.Add(-time.Hour), vpc), true, false, 2},
		{"newer tracked scan replaces", projectScan(repo, "main", t0.Add(2*time.Hour), vpc), true, true, 1},
	}
	for _, st := range steps {
		res := mustIngest(t, s, st.scan, st.tracked)
		if res.Applied != st.wantApplied || res.ScanID == 0 {
			t.Errorf("%s: result %+v, want applied=%v", st.name, res, st.wantApplied)
		}
		if got := callNames(); len(got) != st.wantCalls {
			t.Errorf("%s: current calls %v, want %d", st.name, got, st.wantCalls)
		}
	}
}

func TestModuleRepoScanReplacesTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const repo = "https://github.com/org/vpc.git"

	latestTag := func() any {
		t.Helper()
		modules, err := s.ListModules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return deref(modules[0].LatestTag)
	}

	mustIngest(t, s, repoScan(repo, t0, "v1.0.0", "v2.0.0"), false)
	if got := latestTag(); got != "v2.0.0" {
		t.Fatalf("latest = %v, want v2.0.0", got)
	}
	if res := mustIngest(t, s, repoScan(repo, t0.Add(-time.Hour), "v1.0.0"), false); res.Applied {
		t.Error("stale module repo scan was applied")
	}
	// v2.0.0 was deleted upstream.
	mustIngest(t, s, repoScan(repo, t0.Add(time.Hour), "v1.0.0", "v1.1.0"), false)
	if got := latestTag(); got != "v1.1.0" {
		t.Errorf("latest = %v, want v1.1.0", got)
	}
}

func TestProjectModuleSharesModuleRepo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// The project is scanned before the module repo; both must land on one module.
	mustIngest(t, s, projectScan("https://github.com/org/p.git", "main", t0,
		report.Fact{CallName: "vpc", Source: "git::ssh://git@github.com/Org/VPC.git//modules/base?ref=v1.0.0", RefDeclared: "v1.0.0"},
	), true)
	mustIngest(t, s, repoScan("https://github.com/org/vpc.git", t0, "v1.0.0", "v2.0.0"), false)

	modules, err := s.ListModules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 || modules[0].Consumers != 1 || deref(modules[0].LatestTag) != "v2.0.0" {
		t.Errorf("modules = %+v, want one module with 1 consumer and latest v2.0.0", modules)
	}
}

func TestNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.ProjectUsages(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("ProjectUsages(999) error = %v, want ErrNotFound", err)
	}
	if _, err := s.ModuleConsumers(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("ModuleConsumers(999) error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetProject(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProject(999) error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetModule(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetModule(999) error = %v, want ErrNotFound", err)
	}
}

func TestIngestStoresRawReport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := projectScan("https://github.com/org/p.git", "main", t0)
	raw := []byte(`{"schema_version":1,"extra_field":"kept"}`)
	res, err := s.Ingest(ctx, Scan{Report: r, Raw: raw, Tracked: true})
	if err != nil {
		t.Fatal(err)
	}
	var extra string
	if err := s.pool.QueryRow(ctx, `SELECT report->>'extra_field' FROM scans WHERE id = $1`, res.ScanID).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if extra != "kept" {
		t.Errorf("stored report lost fields: extra_field = %q", extra)
	}
}
