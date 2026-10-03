package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/auth"
	"github.com/WasathTheekshana/terragraph/server/internal/graph"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const token = "test-token"

type fakeStore struct {
	// data, when set, is what the list methods return for the graph.
	data *graph.Input

	ingested  []store.Scan
	ingestErr error
	pingErr   error
	usages    map[int64][]store.Usage

	runErr    error
	runKey    string
	runItems  []store.NewRunItem
	failMsg   string
	listLimit int
}

func (f *fakeStore) Ingest(_ context.Context, in store.Scan) (store.IngestResult, error) {
	if f.ingestErr != nil {
		return store.IngestResult{}, f.ingestErr
	}
	f.ingested = append(f.ingested, in)
	return store.IngestResult{ScanID: int64(len(f.ingested)), Applied: in.Tracked}, nil
}

func (f *fakeStore) ListProjects(context.Context) ([]store.Project, error) {
	if f.data != nil {
		return f.data.Projects, nil
	}
	return nil, nil
}

func (f *fakeStore) ListUsages(context.Context) ([]store.Usage, error) {
	if f.data != nil {
		return f.data.Usages, nil
	}
	return nil, nil
}

func (f *fakeStore) ListModules(context.Context) ([]store.Module, error) {
	if f.data != nil {
		return f.data.Modules, nil
	}
	return []store.Module{{ID: 1, Key: "github.com/org/vpc", Kind: "git"}}, nil
}

func (f *fakeStore) ProjectUsages(_ context.Context, id int64) ([]store.Usage, error) {
	u, ok := f.usages[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) ModuleConsumers(ctx context.Context, id int64) ([]store.Usage, error) {
	return f.ProjectUsages(ctx, id)
}

func (f *fakeStore) GetProject(_ context.Context, id int64) (store.Project, error) {
	if _, ok := f.usages[id]; !ok {
		return store.Project{}, store.ErrNotFound
	}
	return store.Project{ID: id, RepoURL: "git@github.com:org/p.git"}, nil
}

func (f *fakeStore) GetModule(_ context.Context, id int64) (store.Module, error) {
	if _, ok := f.usages[id]; !ok {
		return store.Module{}, store.ErrNotFound
	}
	return store.Module{ID: id, Key: "github.com/org/vpc", Kind: "git"}, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) ModuleDependencies(ctx context.Context, id int64) ([]store.Usage, error) {
	return f.ProjectUsages(ctx, id)
}

func (f *fakeStore) ListRepos(context.Context) ([]store.Repo, error) {
	if f.data != nil {
		return f.data.Repos, nil
	}
	return []store.Repo{{ID: 7, RepoURL: "git@github.com:org/p.git", Projects: 2}}, nil
}

func (f *fakeStore) GetRepo(_ context.Context, id int64) (store.Repo, error) {
	if _, ok := f.usages[id]; !ok {
		return store.Repo{}, store.ErrNotFound
	}
	return store.Repo{ID: id, RepoURL: "git@github.com:org/p.git", Projects: 2}, nil
}

func (f *fakeStore) RepoProjects(_ context.Context, id int64) ([]store.Project, error) {
	return []store.Project{{ID: 1, RepoID: id, Path: "envs/dev"}, {ID: 2, RepoID: id, Path: "envs/prod"}}, nil
}

func (f *fakeStore) CreateRun(_ context.Context, label, key string, items []store.NewRunItem) (store.Run, []store.RunItem, error) {
	f.runKey, f.runItems = key, items
	out := make([]store.RunItem, len(items))
	for i, it := range items {
		out[i] = store.RunItem{ID: int64(i + 1), Kind: it.Kind, RepoURL: it.RepoURL, Path: it.Path, Status: store.ItemPending}
	}
	return store.Run{ID: 1, Label: label, Status: store.RunRunning, Total: len(items)}, out, nil
}

func (f *fakeStore) IngestRunItem(_ context.Context, runID, itemID int64, in store.Scan) (store.IngestResult, error) {
	if f.runErr != nil {
		return store.IngestResult{}, f.runErr
	}
	if runID != 1 || itemID != 1 {
		return store.IngestResult{}, store.ErrNotFound
	}
	f.ingested = append(f.ingested, in)
	return store.IngestResult{ScanID: 9, Applied: in.Tracked, Duplicate: len(f.ingested) > 1}, nil
}

func (f *fakeStore) FailRunItem(_ context.Context, runID, itemID int64, msg string) error {
	f.failMsg = msg
	return f.runErr
}

func (f *fakeStore) FinishRun(_ context.Context, runID int64, status string) (store.Run, error) {
	if f.runErr != nil {
		return store.Run{}, f.runErr
	}
	return store.Run{ID: runID, Status: status}, nil
}

func (f *fakeStore) ListRuns(_ context.Context, limit int) ([]store.Run, error) {
	f.listLimit = limit
	return nil, nil
}

func (f *fakeStore) GetRun(_ context.Context, id int64) (store.Run, error) {
	if id != 1 {
		return store.Run{}, store.ErrNotFound
	}
	return store.Run{ID: 1, Status: store.RunRunning}, nil
}

func (f *fakeStore) RunItems(context.Context, int64) ([]store.RunItem, error) {
	return []store.RunItem{{ID: 1, Kind: store.ItemKindProject}}, nil
}

// fakeAuth accepts "Bearer test-token" as principal p and rejects the rest,
// the way the real authenticator answers.
type fakeAuth struct{ p auth.Principal }

func (f fakeAuth) RequireAPI(perm auth.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="terragraph"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid credentials")
			return
		}
		if (perm == auth.Read && !f.p.CanRead) || (perm == auth.Ingest && !f.p.CanIngest) {
			writeError(w, http.StatusForbidden, "these credentials don't allow this")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), f.p)))
	})
}

var fullAccess = auth.Principal{Kind: auth.KindToken, Name: "test", CanRead: true, CanIngest: true}

func newTestServer(t *testing.T, fs *fakeStore) *httptest.Server {
	t.Helper()
	return newTestServerWithUI(t, fs, nil)
}

func newTestServerWithUI(t *testing.T, fs *fakeStore, ui http.Handler) *httptest.Server {
	t.Helper()
	return newTestServerAs(t, fs, fullAccess, ui)
}

func newTestServerAs(t *testing.T, fs *fakeStore, p auth.Principal, ui http.Handler) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{TrackedBranches: []string{"main"}, UI: ui}
	srv := httptest.NewServer(NewHandler(fs, fakeAuth{p}, cfg, log))
	t.Cleanup(srv.Close)
	return srv
}

const validScan = `{
	"schema_version": 1,
	"scanner_type": "module-usage",
	"subject": {"kind": "project", "repo_url": "git@github.com:org/p.git", "branch": "%s"},
	"generated_at": "2026-09-27T12:00:00Z",
	"facts": [{"type": "module_call", "call_name": "vpc", "source": "git::https://github.com/org/vpc.git?ref=v1.0.0",
		"ref_declared": "v1.0.0", "resolution_source": "source-parse"}]
}`

func postScan(t *testing.T, url, auth, contentType, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/api/v1/scans", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return resp, body
}

func get(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return do(t, req)
}

func TestReadsNeedCredentials(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	for _, path := range []string{"/api/v1/projects", "/api/v1/modules", "/api/v1/repos", "/api/v1/runs"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without credentials = %d, want 401", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d; health checks stay open", path, resp.StatusCode)
		}
	}
}

func TestIngestOnlyCredentialsCantRead(t *testing.T) {
	srv := newTestServerAs(t, &fakeStore{}, auth.Principal{CanIngest: true}, nil)
	if resp, _ := get(t, srv.URL+"/api/v1/projects"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("read with an ingest-only credential = %d, want 403", resp.StatusCode)
	}
}

func TestScopedCredentials(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServerAs(t, fs, auth.Principal{CanIngest: true, RepoPatterns: []string{"github.com/org/*"}}, nil)
	inScope := strings.Replace(validScan, "%s", "main", 1)
	outOfScope := strings.Replace(inScope, "git@github.com:org/p.git", "git@github.com:other/p.git", 1)

	if resp, body := postScan(t, srv.URL, "Bearer "+token, "application/json", inScope); resp.StatusCode != http.StatusCreated {
		t.Errorf("in-scope scan = %d %v", resp.StatusCode, body)
	}
	if resp, body := postScan(t, srv.URL, "Bearer "+token, "application/json", outOfScope); resp.StatusCode != http.StatusForbidden {
		t.Errorf("out-of-scope scan = %d %v, want 403", resp.StatusCode, body)
	}

	run := `{"label": "l", "items": [
		{"kind": "project", "repo_url": "git@github.com:org/a.git"},
		{"kind": "project", "repo_url": "git@github.com:other/b.git"},
		{"kind": "module_repo", "repo_url": "https://github.com/terraform-aws-modules/terraform-aws-vpc.git"}]}`
	resp, body := post(t, srv.URL+"/api/v1/runs", run, nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(fmt.Sprint(body["details"]), "other/b.git") || strings.Contains(fmt.Sprint(body["details"]), "org/a.git") {
		t.Errorf("run with an out-of-scope project = %d %v; want 403 naming only that project", resp.StatusCode, body)
	}
	run = strings.Replace(run, `{"kind": "project", "repo_url": "git@github.com:other/b.git"},`, "", 1)
	if resp, body := post(t, srv.URL+"/api/v1/runs", run, nil); resp.StatusCode != http.StatusCreated {
		t.Errorf("run in scope, plus a public module repo = %d %v", resp.StatusCode, body)
	}
}

func TestVerifiedBranchOverridesReport(t *testing.T) {
	for _, tt := range []struct {
		signed      string
		wantTracked bool
	}{{"main", true}, {"feature/x", false}, {"", false}} {
		fs := &fakeStore{}
		srv := newTestServerAs(t, fs, auth.Principal{CanIngest: true, Branch: tt.signed, BranchVerified: true}, nil)
		// The report claims main whatever branch the workflow really ran on.
		resp, _ := postScan(t, srv.URL, "Bearer "+token, "application/json", strings.Replace(validScan, "%s", "main", 1))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status %d", resp.StatusCode)
		}
		got := fs.ingested[0]
		if got.Tracked != tt.wantTracked || got.Report.Subject.Branch != tt.signed {
			t.Errorf("signed branch %q: tracked %v, stored branch %q", tt.signed, got.Tracked, got.Report.Subject.Branch)
		}
	}
}

func TestCreateScan(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)

	resp, body := postScan(t, srv.URL, "Bearer "+token, "application/json; charset=utf-8", strings.Replace(validScan, "%s", "main", 1))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, body)
	}
	if body["scan_id"] != float64(1) || body["applied"] != true {
		t.Errorf("body = %v, want scan_id 1, applied true", body)
	}
	if len(fs.ingested) != 1 || !fs.ingested[0].Tracked || len(fs.ingested[0].Raw) == 0 {
		t.Errorf("ingested = %+v, want one tracked scan with the raw body", fs.ingested)
	}
}

func TestCreateScanUntrackedBranch(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)

	resp, body := postScan(t, srv.URL, "Bearer "+token, "application/json", strings.Replace(validScan, "%s", "feature/x", 1))
	if resp.StatusCode != http.StatusCreated || body["applied"] != false {
		t.Errorf("status = %d, body = %v; want 201 with applied false", resp.StatusCode, body)
	}
}

func TestFoldersOutsideGitAreAlwaysTracked(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)
	scan := strings.Replace(strings.Replace(validScan, "%s", "", 1), "git@github.com:org/p.git", "file://laptop/C:/work/infra", 1)
	resp, body := postScan(t, srv.URL, "Bearer "+token, "application/json", scan)
	if resp.StatusCode != http.StatusCreated || body["applied"] != true {
		t.Errorf("status = %d, body = %v; want a branchless local folder applied", resp.StatusCode, body)
	}
}

func TestCreateScanErrors(t *testing.T) {
	valid := strings.Replace(validScan, "%s", "main", 1)
	tests := []struct {
		name        string
		auth        string
		contentType string
		body        string
		ingestErr   error
		wantStatus  int
		wantError   string
	}{
		{"no token", "", "application/json", valid, nil, http.StatusUnauthorized, "credentials"},
		{"wrong token", "Bearer nope", "application/json", valid, nil, http.StatusUnauthorized, "credentials"},
		{"not bearer", "Basic " + token, "application/json", valid, nil, http.StatusUnauthorized, "credentials"},
		{"wrong content type", "Bearer " + token, "text/plain", valid, nil, http.StatusUnsupportedMediaType, "application/json"},
		{"bad json", "Bearer " + token, "application/json", "{", nil, http.StatusBadRequest, "invalid JSON"},
		{"invalid report", "Bearer " + token, "application/json", `{"schema_version": 1}`, nil, http.StatusUnprocessableEntity, "invalid scan report"},
		{"too large", "Bearer " + token, "application/json", `"` + strings.Repeat("a", maxReportBytes) + `"`, nil, http.StatusRequestEntityTooLarge, "10 MiB"},
		{"store failure", "Bearer " + token, "application/json", valid, errors.New("db down"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &fakeStore{ingestErr: tt.ingestErr})
			resp, body := postScan(t, srv.URL, tt.auth, tt.contentType, tt.body)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %v)", resp.StatusCode, tt.wantStatus, body)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, tt.wantError) {
				t.Errorf("error = %q, want it to contain %q", msg, tt.wantError)
			}
			if tt.wantStatus == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate header")
			}
		})
	}
}

func TestInvalidReportListsDetails(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	_, body := postScan(t, srv.URL, "Bearer "+token, "application/json", `{"schema_version": 2}`)
	details, _ := body["details"].([]any)
	if len(details) < 2 {
		t.Errorf("details = %v, want every validation problem listed", body["details"])
	}
}

func TestListEndpoints(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})

	resp, body := get(t, srv.URL+"/api/v1/projects")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("projects status = %d", resp.StatusCode)
	}
	if projects, ok := body["projects"].([]any); !ok || len(projects) != 0 {
		t.Errorf("projects = %#v, want an empty list, not null", body["projects"])
	}

	resp, body = get(t, srv.URL+"/api/v1/modules")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("modules status = %d", resp.StatusCode)
	}
	if modules, _ := body["modules"].([]any); len(modules) != 1 {
		t.Errorf("modules = %v, want 1", body["modules"])
	}
}

func TestUsageEndpoints(t *testing.T) {
	fs := &fakeStore{usages: map[int64][]store.Usage{7: {{ProjectID: 7, CallName: "vpc"}}}}
	srv := newTestServer(t, fs)

	tests := []struct {
		path       string
		wantStatus int
		wantKey    string
	}{
		{"/api/v1/projects/7", http.StatusOK, "project"},
		{"/api/v1/modules/7", http.StatusOK, "module"},
		{"/api/v1/projects/7/usages", http.StatusOK, "usages"},
		{"/api/v1/modules/7/consumers", http.StatusOK, "usages"},
		{"/api/v1/modules/7/dependencies", http.StatusOK, "usages"},
		{"/api/v1/repos", http.StatusOK, "repos"},
		{"/api/v1/repos/7", http.StatusOK, "projects"},
		{"/api/v1/repos/8", http.StatusNotFound, ""},
		{"/api/v1/repos/x", http.StatusBadRequest, ""},
		{"/api/v1/projects/8", http.StatusNotFound, ""},
		{"/api/v1/modules/8", http.StatusNotFound, ""},
		{"/api/v1/projects/8/usages", http.StatusNotFound, ""},
		{"/api/v1/modules/8/consumers", http.StatusNotFound, ""},
		{"/api/v1/projects/abc/usages", http.StatusBadRequest, ""},
		{"/api/v1/projects/0", http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		resp, body := get(t, srv.URL+tt.path)
		if resp.StatusCode != tt.wantStatus {
			t.Errorf("GET %s status = %d, want %d (body %v)", tt.path, resp.StatusCode, tt.wantStatus, body)
		}
		if tt.wantKey != "" && body[tt.wantKey] == nil {
			t.Errorf("GET %s body = %v, want a %q field", tt.path, body, tt.wantKey)
		}
	}
}

var testUI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, "ui page")
})

func TestUnknownAPIPathIsJSON(t *testing.T) {
	for name, ui := range map[string]http.Handler{"api only": nil, "with ui": testUI} {
		t.Run(name, func(t *testing.T) {
			srv := newTestServerWithUI(t, &fakeStore{}, ui)
			resp, body := get(t, srv.URL+"/api/v1/nope")
			if resp.StatusCode != http.StatusNotFound || body["error"] != "not found" {
				t.Errorf("status = %d, body = %v; want a JSON 404", resp.StatusCode, body)
			}
		})
	}
}

func TestUIServesNonAPIPaths(t *testing.T) {
	srv := newTestServerWithUI(t, &fakeStore{}, testUI)
	for _, path := range []string{"/", "/projects/3", "/modules"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ui page" {
			t.Errorf("GET %s = %d %q, want the UI handler", path, resp.StatusCode, body)
		}
	}
	if resp, body := get(t, srv.URL+"/api/v1/projects"); resp.StatusCode != http.StatusOK || body["projects"] == nil {
		t.Errorf("API route shadowed by UI: %d %v", resp.StatusCode, body)
	}
}

func TestNoUIWithoutConfig(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET / status = %d, want 404 when no UI is configured", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)

	if resp, _ := get(t, srv.URL+"/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/readyz"); resp.StatusCode != http.StatusOK {
		t.Errorf("readyz = %d", resp.StatusCode)
	}
	fs.pingErr = errors.New("db down")
	if resp, _ := get(t, srv.URL+"/readyz"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz with db down = %d, want 503", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz with db down = %d, want 200 (liveness must not depend on the db)", resp.StatusCode)
	}
}
