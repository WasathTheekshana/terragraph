package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

type fakeStore struct {
	repos    []store.Repo
	projects []store.Project
	modules  []store.Module
	usages   []store.Usage
	runs     []store.Run
	items    []store.RunItem
	err      error
}

func (f *fakeStore) ListRepos(context.Context) ([]store.Repo, error) { return f.repos, f.err }

func (f *fakeStore) GetRepo(_ context.Context, id int64) (store.Repo, error) {
	for _, r := range f.repos {
		if r.ID == id {
			return r, f.err
		}
	}
	return store.Repo{}, store.ErrNotFound
}

func (f *fakeStore) RepoProjects(_ context.Context, id int64) ([]store.Project, error) {
	var out []store.Project
	for _, p := range f.projects {
		if p.RepoID == id {
			out = append(out, p)
		}
	}
	return out, f.err
}

func (f *fakeStore) ModuleDependencies(ctx context.Context, id int64) ([]store.Usage, error) {
	m, err := f.GetModule(ctx, id)
	if err != nil || m.RepoID == nil {
		return nil, err
	}
	var out []store.Usage
	for _, u := range f.usages {
		for _, p := range f.projects {
			if p.ID == u.ProjectID && p.RepoID == *m.RepoID {
				out = append(out, u)
			}
		}
	}
	return out, f.err
}

func (f *fakeStore) ListRuns(context.Context, int) ([]store.Run, error) { return f.runs, f.err }

func (f *fakeStore) GetRun(_ context.Context, id int64) (store.Run, error) {
	for _, r := range f.runs {
		if r.ID == id {
			return r, f.err
		}
	}
	return store.Run{}, store.ErrNotFound
}

func (f *fakeStore) RunItems(context.Context, int64) ([]store.RunItem, error) { return f.items, f.err }

func (f *fakeStore) ListModules(context.Context) ([]store.Module, error) { return f.modules, f.err }

func (f *fakeStore) GetProject(_ context.Context, id int64) (store.Project, error) {
	for _, p := range f.projects {
		if p.ID == id {
			return p, f.err
		}
	}
	return store.Project{}, store.ErrNotFound
}

func (f *fakeStore) GetModule(_ context.Context, id int64) (store.Module, error) {
	for _, m := range f.modules {
		if m.ID == id {
			return m, f.err
		}
	}
	return store.Module{}, store.ErrNotFound
}

func (f *fakeStore) ProjectUsages(_ context.Context, id int64) ([]store.Usage, error) {
	var out []store.Usage
	for _, u := range f.usages {
		if u.ProjectID == id {
			out = append(out, u)
		}
	}
	return out, f.err
}

func (f *fakeStore) ModuleConsumers(_ context.Context, id int64) ([]store.Usage, error) {
	var out []store.Usage
	for _, u := range f.usages {
		if u.ModuleID != nil && *u.ModuleID == id {
			out = append(out, u)
		}
	}
	return out, f.err
}

func sampleStore() *fakeStore {
	scanned := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	const payments, network, infra, vpcRepo = "git@github.com:org/payments.git", "https://github.com/org/network.git",
		"git@github.com:org/infra.git", "https://github.com/org/vpc.git"
	return &fakeStore{
		repos: []store.Repo{
			{ID: 1, RepoURL: payments, Projects: 1, ProjectID: ptr(int64(1)), ProjectPath: ".", Branch: "main", LastScanAt: &scanned,
				ModuleCalls: 3, OutdatedCalls: 1, MajorBehindCalls: 1},
			{ID: 2, RepoURL: network, Projects: 1, ProjectID: ptr(int64(2)), ProjectPath: ".", Branch: "main", LastScanAt: &scanned, ModuleCalls: 1},
			{ID: 3, RepoURL: infra, Projects: 2, Branch: "main", LastScanAt: &scanned, ModuleCalls: 2, OutdatedCalls: 2},
			// The vpc module's own repo: projects use it as a module.
			{ID: 4, RepoURL: vpcRepo, Projects: 1, ProjectID: ptr(int64(5)), ProjectPath: ".", ModuleID: ptr(int64(10)), Branch: "main", LastScanAt: &scanned},
		},
		projects: []store.Project{
			{ID: 1, RepoID: 1, RepoURL: payments, Path: ".", LastBranch: "main", LastCommitSHA: "908ac9f4c548", LastScanAt: &scanned,
				ModuleCalls: 3, OutdatedCalls: 1, MajorBehindCalls: 1},
			{ID: 2, RepoID: 2, RepoURL: network, Path: ".", LastBranch: "main", LastScanAt: &scanned, ModuleCalls: 1},
			{ID: 3, RepoID: 3, RepoURL: infra, Path: "envs/dev", LastBranch: "main", LastScanAt: &scanned, ModuleCalls: 1, OutdatedCalls: 1},
			{ID: 4, RepoID: 3, RepoURL: infra, Path: "envs/prod", LastBranch: "main", LastScanAt: &scanned, ModuleCalls: 1, OutdatedCalls: 1},
			{ID: 5, RepoID: 4, RepoURL: vpcRepo, Path: ".", LastBranch: "main", LastScanAt: &scanned, ModuleCalls: 1},
		},
		modules: []store.Module{
			{ID: 10, Key: "github.com/org/vpc", Kind: "git", Source: vpcRepo, LatestTag: ptr("v6.0.0"),
				LatestVersion: ptr("6.0.0"), VersionsScannedAt: &scanned, Consumers: 2, OutdatedConsumers: 1, RepoID: ptr(int64(4))},
			{ID: 11, Key: "registry.terraform.io/x/eks/aws", Kind: "registry", Source: "x/eks/aws"},
		},
		usages: []store.Usage{
			{ProjectID: 1, ProjectRepoURL: payments, ProjectPath: ".", CallName: "vpc", ModuleID: ptr(int64(10)),
				ModuleKey: ptr("github.com/org/vpc"), ModuleKind: ptr("git"), PinnedVersion: ptr("5.1.0"), LatestVersion: ptr("6.0.0"), MajorsBehind: ptr(1),
				Outdated: true, File: "main.tf", Line: 1, ResolutionSource: "modules-json"},
			{ProjectID: 1, ProjectRepoURL: payments, ProjectPath: ".", CallName: "helpers", Source: "./modules/helpers", File: "main.tf", Line: 9},
			{ProjectID: 1, ProjectRepoURL: payments, ProjectPath: ".", Parent: "helpers", CallName: "subnets", ModuleID: ptr(int64(12)),
				ModuleKey: ptr("github.com/org/subnets"), ModuleKind: ptr("git"), PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("1.0.0"),
				MajorsBehind: ptr(0), File: "modules/helpers/main.tf", Line: 3},
			{ProjectID: 2, ProjectRepoURL: network, ProjectPath: ".", CallName: "vpc", ModuleID: ptr(int64(10)),
				ModuleKey: ptr("github.com/org/vpc"), ModuleKind: ptr("git"), PinnedVersion: ptr("6.0.0"), LatestVersion: ptr("6.0.0"), MajorsBehind: ptr(0), File: "vpc.tf", Line: 4},
			// The vpc module's own code calls a subnets module.
			{ProjectID: 5, ProjectRepoURL: vpcRepo, ProjectPath: ".", CallName: "subnets", ModuleID: ptr(int64(12)),
				ModuleKey: ptr("github.com/org/subnets"), ModuleKind: ptr("git"), PinnedVersion: ptr("0.9.0"), LatestVersion: ptr("1.0.0"),
				MajorsBehind: ptr(1), Outdated: true, File: "main.tf", Line: 2},
		},
	}
}

func get(t *testing.T, h http.Handler, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func newTestHandler(s Store) http.Handler {
	return newHandler(s, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return testNow })
}

func TestPages(t *testing.T) {
	h := newTestHandler(sampleStore())
	tests := []struct {
		path       string
		wantStatus int
		want       []string
		notWant    []string
	}{
		{"/", 200, []string{"<title>Projects · terragraph</title>", "git@github.com:org/payments.git", `href="/projects/1"`, `aria-current="page"`}, nil},
		{"/?q=network", 200, []string{"https://github.com/org/network.git"}, []string{"payments.git"}},
		{"/?q=nothing-matches", 200, []string{"No repositories match", "nothing-matches"}, nil},
		{"/projects/1", 200, []string{"git@github.com:org/payments.git", "908ac9f", "1 major version behind", "Local module", `href="/modules/10"`, "verified"}, nil},
		{"/modules", 200, []string{"github.com/org/vpc", "registry.terraform.io/x/eks/aws", "2 projects"}, nil},
		{"/modules/10", 200, []string{"github.com/org/vpc", "Versions in use", "5.1.0", "6.0.0", `href="/projects/2"`}, nil},
		{"/modules/11", 200, []string{"The latest version is unknown", "No projects use this module"}, nil},
		{"/projects/99", 404, []string{"Page not found"}, nil},
		{"/projects/abc", 404, []string{"Page not found"}, nil},
		{"/modules/0", 404, []string{"Page not found"}, nil},
		{"/nope", 404, []string{"Page not found"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, body := get(t, h, tt.path)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body missing %q", w)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(body, nw) {
					t.Errorf("body unexpectedly contains %q", nw)
				}
			}
		})
	}
}

func runStore() *fakeStore {
	finished := testNow.Add(-time.Hour)
	return &fakeStore{
		runs: []store.Run{
			{ID: 3, Label: "laptop: ~/next-projects", Status: store.RunRunning, CreatedAt: testNow.Add(-time.Minute),
				UpdatedAt: testNow.Add(-5 * time.Second), Total: 4, Pending: 1, Done: 2, Failed: 1},
			{ID: 2, Label: "ci: org/app", Status: store.RunRunning, CreatedAt: testNow.Add(-2 * time.Hour),
				UpdatedAt: testNow.Add(-time.Hour), Total: 2, Pending: 2},
			{ID: 1, Label: "ci: org/vpc", Status: store.RunFinished, CreatedAt: finished, UpdatedAt: finished,
				FinishedAt: &finished, Total: 1, Done: 1},
		},
		items: []store.RunItem{
			{ID: 1, Kind: store.ItemKindModuleRepo, RepoURL: "git@github.com:org/private.git", Status: store.ItemFailed,
				Error: "git ls-remote: Permission denied (publickey)"},
			{ID: 2, Kind: store.ItemKindProject, RepoURL: "git@github.com:org/app.git", Path: "envs/prod", Status: store.ItemPending},
			{ID: 3, Kind: store.ItemKindProject, RepoURL: "git@github.com:org/app.git", Path: ".", Status: store.ItemDone,
				Applied: ptr(true), ProjectID: ptr(int64(7))},
			{ID: 4, Kind: store.ItemKindModuleRepo, RepoURL: "https://github.com/org/vpc.git", Status: store.ItemDone,
				Applied: ptr(true), ModuleID: ptr(int64(10))},
		},
	}
}

func TestRunsPages(t *testing.T) {
	h := newTestHandler(runStore())

	resp, body := get(t, h, "/runs")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /runs = %d", resp.StatusCode)
	}
	for _, want := range []string{`href="/runs/3"`, "laptop: ~/next-projects", "Running", "Stalled", "Finished", "3 of 4", `<progress`} {
		if !strings.Contains(body, want) {
			t.Errorf("/runs missing %q", want)
		}
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("the runs list shouldn't auto-refresh")
	}

	_, body = get(t, h, "/runs/3")
	for _, want := range []string{
		`http-equiv="refresh" content="2"`,
		"Permission denied (publickey)",
		"Waiting", "Failed", "Done", "envs/prod",
		`href="/projects/7"`, `href="/modules/10"`,
		"Module versions",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/runs/3 missing %q", want)
		}
	}
}

func TestRunPageStopsRefreshing(t *testing.T) {
	h := newTestHandler(runStore())
	for id, wantStalled := range map[string]bool{"2": true, "1": false} {
		_, body := get(t, h, "/runs/"+id)
		if strings.Contains(body, `http-equiv="refresh"`) {
			t.Errorf("/runs/%s refreshes though it isn't making progress", id)
		}
		if got := strings.Contains(body, "probably stopped"); got != wantStalled {
			t.Errorf("/runs/%s stalled notice = %v, want %v", id, got, wantStalled)
		}
	}
	if resp, _ := get(t, h, "/runs/99"); resp.StatusCode != 404 {
		t.Errorf("unknown run = %d, want 404", resp.StatusCode)
	}
}

func TestProjectPathsShown(t *testing.T) {
	s := sampleStore()
	s.repos[1].ProjectPath, s.projects[1].Path = "terraform", "terraform"
	h := newTestHandler(s)
	if _, body := get(t, h, "/"); !strings.Contains(body, ">terraform</div>") {
		t.Error("projects list doesn't show a single project's path inside its repo")
	}
	if _, body := get(t, h, "/projects/2"); !strings.Contains(body, "Path terraform") {
		t.Error("project page doesn't show the root path")
	}
}

func TestReposWithSeveralProjects(t *testing.T) {
	h := newTestHandler(sampleStore())

	_, body := get(t, h, "/")
	for _, want := range []string{`href="/repos/3"`, "2 Terraform projects", `href="/projects/1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("projects list missing %q", want)
		}
	}

	resp, body := get(t, h, "/repos/3")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /repos/3 = %d", resp.StatusCode)
	}
	for _, want := range []string{">infra</h1>", "git@github.com:org/infra.git", `href="/projects/3"`, ">envs/dev</a>", `href="/projects/4"`, ">envs/prod</a>"} {
		if !strings.Contains(body, want) {
			t.Errorf("repo page missing %q", want)
		}
	}

	_, body = get(t, h, "/projects/3")
	if !strings.Contains(body, `href="/repos/3"`) || !strings.Contains(body, "← infra") {
		t.Error("a project in a multi-project repo should link back to its repo")
	}
	if resp, _ := get(t, h, "/repos/99"); resp.StatusCode != 404 {
		t.Errorf("unknown repo = %d, want 404", resp.StatusCode)
	}
}

func TestSharedModuleReposListedUnderModules(t *testing.T) {
	h := newTestHandler(sampleStore())

	_, body := get(t, h, "/")
	if strings.Contains(body, ">vpc</a>") || !strings.Contains(body, "1 scanned repository is a shared module") {
		t.Error("a repo used as a module should be left out of Projects, with a note saying where it is")
	}

	_, body = get(t, h, "/modules")
	if !strings.Contains(body, "source scanned") {
		t.Error("modules list should mark modules whose repo was scanned")
	}

	_, body = get(t, h, "/modules/10")
	for _, want := range []string{"What this module uses", `href="/repos/4"`, ">subnets</span>", "1 major version behind"} {
		if !strings.Contains(body, want) {
			t.Errorf("module page missing %q", want)
		}
	}

	_, body = get(t, h, "/projects/5")
	if !strings.Contains(body, "Projects use this repository as a shared module") || !strings.Contains(body, `href="/modules/10"`) {
		t.Error("a shared module repo's project page should point to its module")
	}
}

func TestNestedCallsShownAsTree(t *testing.T) {
	_, body := get(t, newTestHandler(sampleStore()), "/projects/1")
	helpers := strings.Index(body, ">helpers</span>")
	subnets := strings.Index(body, ">subnets</span>")
	if helpers < 0 || subnets < helpers {
		t.Fatal("the nested call should come right after the module that makes it")
	}
	nestedRow := body[helpers:]
	for _, want := range []string{"ml-5", "↳", ">nested</span>", `title="helpers.subnets"`} {
		if !strings.Contains(nestedRow, want) {
			t.Errorf("nested row missing %q", want)
		}
	}
	if !strings.Contains(body, "1 call is made inside other modules") {
		t.Error("missing the nested calls note")
	}

	_, body = get(t, newTestHandler(sampleStore()), "/modules/10")
	if strings.Contains(body, "inside helpers") {
		t.Error("vpc isn't nested anywhere in the sample")
	}
}

func TestUnexpandedRemoteModulesNote(t *testing.T) {
	s := sampleStore()
	h := newTestHandler(s)
	if _, body := get(t, h, "/projects/2"); !strings.Contains(body, "initialized with") {
		t.Error("a remote module parsed from source should explain how to see what it calls")
	}
	if _, body := get(t, h, "/projects/1"); strings.Contains(body, "initialized with") {
		t.Error("no note when remote modules were resolved from modules.json")
	}
}

func TestProjectsShowRepoNames(t *testing.T) {
	h := newTestHandler(sampleStore())
	_, body := get(t, h, "/")
	for _, want := range []string{`title="git@github.com:org/payments.git"`, `>payments</a>`, `>network</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("projects list missing %q", want)
		}
	}
	_, body = get(t, h, "/projects/1")
	if !strings.Contains(body, "<title>payments · terragraph</title>") || !strings.Contains(body, `>payments</h1>`) ||
		!strings.Contains(body, `dark:text-slate-300">git@github.com:org/payments.git</p>`) {
		t.Error("project page should be headed by the repo name, with the full URL shown beneath it")
	}
	if !strings.Contains(body, `title="github.com/org/vpc" class="text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">vpc</a>`) {
		t.Error("module calls should link modules by name, with the key as a tooltip")
	}
}

func TestModulesShowNames(t *testing.T) {
	h := newTestHandler(sampleStore())
	_, body := get(t, h, "/modules")
	for _, want := range []string{`title="github.com/org/vpc"`, `>vpc</a>`, `title="registry.terraform.io/x/eks/aws"`, `>eks/aws</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("modules list missing %q", want)
		}
	}
	_, body = get(t, h, "/modules/10")
	if !strings.Contains(body, "<title>vpc · terragraph</title>") || !strings.Contains(body, `>vpc</h1>`) ||
		!strings.Contains(body, `dark:text-slate-300">github.com/org/vpc</p>`) {
		t.Error("module page should be headed by the module name, with its key shown beneath it")
	}
}

func TestEmptyStates(t *testing.T) {
	h := newTestHandler(&fakeStore{})
	for path, want := range map[string]string{"/": "No projects scanned yet", "/modules": "No modules yet", "/runs": "No scans yet"} {
		if resp, body := get(t, h, path); resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("GET %s = %d, want empty state %q", path, resp.StatusCode, want)
		}
	}
}

func TestEscapesUserData(t *testing.T) {
	s := sampleStore()
	s.repos[0].RepoURL = `<script>alert(1)</script>`
	_, body := get(t, newTestHandler(s), "/")
	if strings.Contains(body, "<script>") {
		t.Error("repo URL rendered unescaped")
	}
}

func TestStoreErrorRendersErrorPage(t *testing.T) {
	s := sampleStore()
	s.err = errors.New("db down")
	for _, path := range []string{"/", "/modules", "/projects/1", "/modules/10", "/repos/3"} {
		resp, body := get(t, newTestHandler(s), path)
		if resp.StatusCode != 500 || !strings.Contains(body, "Something went wrong") || strings.Contains(body, "db down") {
			t.Errorf("GET %s = %d; want a 500 page that doesn't leak the error", path, resp.StatusCode)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	resp, _ := get(t, newTestHandler(sampleStore()), "/")
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestStaticFiles(t *testing.T) {
	h := newTestHandler(&fakeStore{})

	resp, body := get(t, h, string(cssURL))
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/css") || len(body) == 0 {
		t.Fatalf("GET %s = %d %q", cssURL, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned CSS Cache-Control = %q, want immutable", cc)
	}

	resp, _ = get(t, h, "/static/favicon.svg")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("favicon = %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}

	for _, path := range []string{"/static/", "/static/missing.css"} {
		if resp, _ := get(t, h, path); resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestPagesReferenceBuiltCSS(t *testing.T) {
	_, body := get(t, newTestHandler(sampleStore()), "/")
	if !strings.Contains(body, string(cssURL)) {
		t.Errorf("page doesn't link %s", cssURL)
	}
}
