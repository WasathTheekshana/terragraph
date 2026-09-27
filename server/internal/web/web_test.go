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
	projects []store.Project
	modules  []store.Module
	usages   []store.Usage
	err      error
}

func (f *fakeStore) ListProjects(context.Context) ([]store.Project, error) { return f.projects, f.err }
func (f *fakeStore) ListModules(context.Context) ([]store.Module, error)   { return f.modules, f.err }

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
	return &fakeStore{
		projects: []store.Project{
			{ID: 1, RepoURL: "git@github.com:org/payments.git", LastBranch: "main", LastCommitSHA: "908ac9f4c548", LastScanAt: &scanned,
				ModuleCalls: 2, OutdatedCalls: 1, MajorBehindCalls: 1},
			{ID: 2, RepoURL: "https://github.com/org/network.git", LastBranch: "main", LastScanAt: &scanned, ModuleCalls: 1},
		},
		modules: []store.Module{
			{ID: 10, Key: "github.com/org/vpc", Kind: "git", Source: "https://github.com/org/vpc.git", LatestTag: ptr("v6.0.0"),
				LatestVersion: ptr("6.0.0"), VersionsScannedAt: &scanned, Consumers: 2, OutdatedConsumers: 1},
			{ID: 11, Key: "registry.terraform.io/x/eks/aws", Kind: "registry", Source: "x/eks/aws"},
		},
		usages: []store.Usage{
			{ProjectID: 1, ProjectRepoURL: "git@github.com:org/payments.git", CallName: "vpc", ModuleID: ptr(int64(10)),
				ModuleKey: ptr("github.com/org/vpc"), PinnedVersion: ptr("5.1.0"), LatestVersion: ptr("6.0.0"), MajorsBehind: ptr(1),
				Outdated: true, File: "main.tf", Line: 1, ResolutionSource: "modules-json"},
			{ProjectID: 1, ProjectRepoURL: "git@github.com:org/payments.git", CallName: "helpers", Source: "./modules/helpers", File: "main.tf", Line: 9},
			{ProjectID: 2, ProjectRepoURL: "https://github.com/org/network.git", CallName: "vpc", ModuleID: ptr(int64(10)),
				ModuleKey: ptr("github.com/org/vpc"), PinnedVersion: ptr("6.0.0"), LatestVersion: ptr("6.0.0"), MajorsBehind: ptr(0), File: "vpc.tf", Line: 4},
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

func newTestHandler(s Store) http.Handler {
	return NewHandler(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		{"/?q=nothing-matches", 200, []string{"No projects match", "nothing-matches"}, nil},
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

func TestEmptyStates(t *testing.T) {
	h := newTestHandler(&fakeStore{})
	for path, want := range map[string]string{"/": "No projects scanned yet", "/modules": "No modules yet"} {
		if resp, body := get(t, h, path); resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("GET %s = %d, want empty state %q", path, resp.StatusCode, want)
		}
	}
}

func TestEscapesUserData(t *testing.T) {
	s := sampleStore()
	s.projects[0].RepoURL = `<script>alert(1)</script>`
	_, body := get(t, newTestHandler(s), "/")
	if strings.Contains(body, "<script>") {
		t.Error("repo URL rendered unescaped")
	}
}

func TestStoreErrorRendersErrorPage(t *testing.T) {
	s := sampleStore()
	s.err = errors.New("db down")
	for _, path := range []string{"/", "/modules", "/projects/1", "/modules/10"} {
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
