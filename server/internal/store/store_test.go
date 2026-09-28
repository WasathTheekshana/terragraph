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
	return newTestStoreAt(t, 0)
}

// newTestStoreAt is newTestStore migrated only up to version (0 for all).
func newTestStoreAt(t *testing.T, version int64) *Store {
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
	if err := s.migrateTo(ctx, version); err != nil {
		t.Fatal(err)
	}
	return s
}

// An existing install upgrading to multi-root projects must keep its data.
func TestMigrationToMultiRootKeepsProjects(t *testing.T) {
	s := newTestStoreAt(t, 1)
	ctx := context.Background()
	old := projectScan("https://github.com/org/app.git", "main", t0,
		report.Fact{CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"})
	var id int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO projects (repo_key, repo_url) VALUES ('github.com/org/app', $1) RETURNING id`,
		old.Subject.RepoURL).Scan(&id); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetProject(ctx, id)
	if err != nil || p.Path != "." {
		t.Fatalf("existing project after upgrade = %+v, %v; want it kept at the repo root", p, err)
	}

	// A scan of the repo root lands on the existing project; another root is new.
	mustIngest(t, s, old, true)
	sub := old
	sub.Subject.Path = "envs/prod"
	mustIngest(t, s, sub, true)
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 2 || projects[0].ID != id || projects[0].ModuleCalls != 1 {
		t.Errorf("projects after upgrade = %+v, %v", projects, err)
	}
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
		if wantKind := map[string]any{"vpc": "git", "vpc_edge": "git", "s3": "registry", "helpers": nil}[u.CallName]; deref(u.ModuleKind) != wantKind {
			t.Errorf("%s: module kind = %v, want %v", u.CallName, deref(u.ModuleKind), wantKind)
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

func TestRootsInOneRepoAreSeparateProjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const repo = "https://github.com/org/infra.git"
	vpc := report.Fact{CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"}

	for _, path := range []string{"envs/dev", "envs/prod", ""} {
		r := projectScan(repo, "main", t0, vpc)
		r.Subject.Path = path
		mustIngest(t, s, r, true)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range projects {
		paths = append(paths, p.Path)
	}
	if !reflect.DeepEqual(paths, []string{".", "envs/dev", "envs/prod"}) {
		t.Errorf("project paths = %v, want one project per root", paths)
	}
}

func TestReposGroupProjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	vpc := report.Fact{CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"}
	for _, sc := range []struct{ repo, path, branch string }{
		{"git@github.com:org/infra.git", "envs/dev", "main"},
		{"git@github.com:org/infra.git", "envs/prod", "main"},
		{"https://github.com/org/api.git", "", "main"},
		{"https://github.com/org/mixed.git", "a", "main"},
		{"https://github.com/org/mixed.git", "b", "master"},
	} {
		r := projectScan(sc.repo, sc.branch, t0, vpc)
		r.Subject.Path = sc.path
		mustIngest(t, s, r, true)
	}

	repos, err := s.ListRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]Repo{}
	for _, r := range repos {
		byURL[r.RepoURL] = r
	}
	infra, api, mixed := byURL["git@github.com:org/infra.git"], byURL["https://github.com/org/api.git"], byURL["https://github.com/org/mixed.git"]
	if len(repos) != 3 || infra.Projects != 2 || infra.ProjectID != nil || infra.ProjectPath != "" || infra.ModuleCalls != 2 || infra.Branch != "main" {
		t.Errorf("infra = %+v (of %d repos)", infra, len(repos))
	}
	if api.Projects != 1 || api.ProjectID == nil || api.ProjectPath != "." || api.ModuleID != nil {
		t.Errorf("api = %+v; a single-project repo should link straight to its project", api)
	}
	if mixed.Branch != "" || !mixed.SeveralBranches || infra.SeveralBranches {
		t.Errorf("mixed = %q/%v, infra several = %v; want several only when projects differ", mixed.Branch, mixed.SeveralBranches, infra.SeveralBranches)
	}

	projects, err := s.RepoProjects(ctx, infra.ID)
	if err != nil || len(projects) != 2 || projects[0].Path != "envs/dev" || projects[0].RepoID != infra.ID {
		t.Errorf("RepoProjects = %+v, %v", projects, err)
	}
	if _, err := s.RepoProjects(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("RepoProjects(999) = %v, want ErrNotFound", err)
	}
	if got, err := s.GetRepo(ctx, api.ID); err != nil || got.ID != api.ID {
		t.Errorf("GetRepo = %+v, %v", got, err)
	}
}

func TestSharedModuleRepo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A project uses the vpc module, and the vpc module's own repo was scanned
	// too: it calls a subnets module.
	mustIngest(t, s, projectScan("https://github.com/org/app.git", "main", t0,
		report.Fact{CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"}), true)
	mustIngest(t, s, projectScan("git@github.com:org/vpc.git", "main", t0,
		report.Fact{CallName: "subnets", Source: "git::https://github.com/org/subnets.git?ref=v2.0.0", RefDeclared: "v2.0.0"}), true)
	mustIngest(t, s, repoScan("https://github.com/org/subnets.git", t0, "v2.0.0", "v3.0.0"), false)

	modules, err := s.ListModules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var vpc, subnets Module
	for _, m := range modules {
		switch m.Key {
		case "github.com/org/vpc":
			vpc = m
		case "github.com/org/subnets":
			subnets = m
		}
	}
	if vpc.RepoID == nil || subnets.RepoID != nil {
		t.Fatalf("vpc = %+v, subnets = %+v; only the scanned module repo should link to its repo", vpc, subnets)
	}

	repos, err := s.ListRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		isModule := r.Key == "github.com/org/vpc"
		if (r.ModuleID != nil) != isModule || (isModule && *r.ModuleID != vpc.ID) {
			t.Errorf("repo %s module = %v, want set only for the vpc repo", r.Key, r.ModuleID)
		}
	}

	deps, err := s.ModuleDependencies(ctx, vpc.ID)
	if err != nil || len(deps) != 1 || deps[0].CallName != "subnets" || deref(deps[0].MajorsBehind) != 1 {
		t.Errorf("vpc dependencies = %+v, %v; want subnets, one major behind", deps, err)
	}
	if deps, err := s.ModuleDependencies(ctx, subnets.ID); err != nil || len(deps) != 0 {
		t.Errorf("unscanned module dependencies = %+v, %v; want none", deps, err)
	}
}

func TestNestedUsages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	res := mustIngest(t, s, projectScan("https://github.com/org/app.git", "main", t0,
		report.Fact{CallName: "addons", Source: "./modules/addons"},
		report.Fact{CallName: "vpc", Parent: "addons", Source: "git::https://github.com/org/vpc.git?ref=v1.0.0", RefDeclared: "v1.0.0"},
		report.Fact{CallName: "vpc", Source: "git::https://github.com/org/vpc.git?ref=v2.0.0", RefDeclared: "v2.0.0"},
	), true)
	if !res.Applied {
		t.Fatal("not applied")
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	usages, err := s.ProjectUsages(ctx, projects[0].ID)
	if err != nil || len(usages) != 3 {
		t.Fatalf("usages = %+v, %v", usages, err)
	}
	var nested *Usage
	for i := range usages {
		if usages[i].Parent == "addons" {
			nested = &usages[i]
		}
	}
	if nested == nil || nested.CallName != "vpc" || deref(nested.PinnedVersion) != "1.0.0" {
		t.Errorf("nested usage = %+v", nested)
	}

	modules, _ := s.ListModules(ctx)
	consumers, err := s.ModuleConsumers(ctx, modules[0].ID)
	if err != nil || len(consumers) != 2 || modules[0].Consumers != 1 {
		t.Errorf("consumers = %+v, %v; module %+v; want both calls from one project", consumers, err, modules[0])
	}
}

func newRun(t *testing.T, s *Store, items ...NewRunItem) (Run, []RunItem) {
	t.Helper()
	run, got, err := s.CreateRun(context.Background(), "test run", "", items)
	if err != nil {
		t.Fatal(err)
	}
	return run, got
}

func TestRunLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const projRepo, modRepo = "https://github.com/org/app.git", "https://github.com/org/vpc.git"

	run, items := newRun(t, s,
		NewRunItem{Kind: ItemKindProject, RepoURL: projRepo, Path: "envs/prod"},
		NewRunItem{Kind: ItemKindModuleRepo, RepoURL: modRepo},
		NewRunItem{Kind: ItemKindModuleRepo, RepoURL: "git@github.com:org/private.git"},
	)
	if run.Status != RunRunning || run.Total != 3 || run.Pending != 3 {
		t.Fatalf("new run = %+v", run)
	}
	byRepo := map[string]RunItem{}
	for _, it := range items {
		byRepo[it.RepoURL] = it
	}

	p := projectScan(projRepo, "main", t0, report.Fact{CallName: "vpc", Source: "git::" + modRepo + "?ref=v1.0.0", RefDeclared: "v1.0.0"})
	p.Subject.Path = "envs/prod"
	res, err := s.IngestRunItem(ctx, run.ID, byRepo[projRepo].ID, Scan{Report: p, Tracked: true})
	if err != nil || !res.Applied || res.Duplicate {
		t.Fatalf("project item: %+v, %v", res, err)
	}
	if _, err := s.IngestRunItem(ctx, run.ID, byRepo[modRepo].ID, Scan{Report: repoScan(modRepo, t0, "v1.0.0", "v2.0.0")}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailRunItem(ctx, run.ID, byRepo["git@github.com:org/private.git"].ID, "permission denied"); err != nil {
		t.Fatal(err)
	}

	run, err = s.FinishRun(ctx, run.ID, RunFinished)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunFinished || run.FinishedAt == nil || run.Done != 2 || run.Failed != 1 || run.Pending != 0 {
		t.Errorf("finished run = %+v", run)
	}

	items, err = s.RunItems(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != ItemFailed || items[0].Error != "permission denied" {
		t.Errorf("failed items should list first, got %+v", items[0])
	}
	for _, it := range items[1:] {
		if it.Status != ItemDone || (it.ProjectID == nil && it.ModuleID == nil) {
			t.Errorf("done item without a link to what it scanned: %+v", it)
		}
	}

	runs, err := s.ListRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Errorf("ListRuns = %+v, %v", runs, err)
	}
}

func TestRunItemIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const repo = "https://github.com/org/vpc.git"
	run, items := newRun(t, s, NewRunItem{Kind: ItemKindModuleRepo, RepoURL: repo})

	first, err := s.IngestRunItem(ctx, run.ID, items[0].ID, Scan{Report: repoScan(repo, t0, "v1.0.0")})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.IngestRunItem(ctx, run.ID, items[0].ID, Scan{Report: repoScan(repo, t0, "v1.0.0")})
	if err != nil || !again.Duplicate || again.ScanID != first.ScanID {
		t.Errorf("resubmission = %+v, %v; want the first result marked duplicate", again, err)
	}
	var scans int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM scans`).Scan(&scans); err != nil || scans != 1 {
		t.Errorf("scans recorded = %d, want 1", scans)
	}
	if err := s.FailRunItem(ctx, run.ID, items[0].ID, "late failure"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunItems(ctx, run.ID); got[0].Status != ItemDone {
		t.Error("a late failure overwrote a done item")
	}
}

func TestRunItemRejectsMismatchedReports(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	run, items := newRun(t, s, NewRunItem{Kind: ItemKindProject, RepoURL: "https://github.com/org/app.git", Path: "envs/prod"})
	id := items[0].ID

	wrongPath := projectScan("git@github.com:org/app.git", "main", t0)
	wrongPath.Subject.Path = "envs/dev"
	for name, r := range map[string]report.Report{
		"other repo": projectScan("https://github.com/org/other.git", "main", t0),
		"other path": wrongPath,
		"wrong kind": repoScan("https://github.com/org/app.git", t0, "v1.0.0"),
		"repo root":  projectScan("https://github.com/org/app.git", "main", t0),
	} {
		if _, err := s.IngestRunItem(ctx, run.ID, id, Scan{Report: r}); !errors.Is(err, ErrItemMismatch) {
			t.Errorf("%s: error = %v, want ErrItemMismatch", name, err)
		}
	}
	if _, err := s.IngestRunItem(ctx, run.ID+1, id, Scan{Report: wrongPath}); !errors.Is(err, ErrNotFound) {
		t.Errorf("item from another run: error = %v, want ErrNotFound", err)
	}
}

func TestClosedRunRejectsChanges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const repo = "https://github.com/org/vpc.git"
	run, items := newRun(t, s, NewRunItem{Kind: ItemKindModuleRepo, RepoURL: repo})

	if _, err := s.FinishRun(ctx, run.ID, RunCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, run.ID, RunCancelled); err != nil {
		t.Errorf("finishing again with the same status should be a no-op, got %v", err)
	}
	if _, err := s.FinishRun(ctx, run.ID, RunFinished); !errors.Is(err, ErrRunClosed) {
		t.Errorf("changing a closed run's status: %v, want ErrRunClosed", err)
	}
	if _, err := s.IngestRunItem(ctx, run.ID, items[0].ID, Scan{Report: repoScan(repo, t0, "v1.0.0")}); !errors.Is(err, ErrRunClosed) {
		t.Errorf("ingest into a closed run: %v, want ErrRunClosed", err)
	}
	if err := s.FailRunItem(ctx, run.ID, items[0].ID, "x"); !errors.Is(err, ErrRunClosed) {
		t.Errorf("fail in a closed run: %v, want ErrRunClosed", err)
	}
}

func TestCreateRunIdempotencyKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	items := []NewRunItem{{Kind: ItemKindModuleRepo, RepoURL: "https://github.com/org/vpc.git"}}

	first, firstItems, err := s.CreateRun(ctx, "run", "key-1", items)
	if err != nil {
		t.Fatal(err)
	}
	again, againItems, err := s.CreateRun(ctx, "run", "key-1", items)
	if err != nil || again.ID != first.ID || againItems[0].ID != firstItems[0].ID {
		t.Errorf("retry with the same key created a new run: %+v, %v", again, err)
	}
	other, _, err := s.CreateRun(ctx, "run", "", items)
	if err != nil || other.ID == first.ID {
		t.Errorf("run without a key should be new: %+v, %v", other, err)
	}
	if _, _, err := s.CreateRun(ctx, "run", "", items); err != nil {
		t.Errorf("runs without keys must not collide: %v", err)
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
