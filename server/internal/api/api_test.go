package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const token = "test-token"

type fakeStore struct {
	ingested  []store.Scan
	ingestErr error
	pingErr   error
	usages    map[int64][]store.Usage
}

func (f *fakeStore) Ingest(_ context.Context, in store.Scan) (store.IngestResult, error) {
	if f.ingestErr != nil {
		return store.IngestResult{}, f.ingestErr
	}
	f.ingested = append(f.ingested, in)
	return store.IngestResult{ScanID: int64(len(f.ingested)), Applied: in.Tracked}, nil
}

func (f *fakeStore) ListProjects(context.Context) ([]store.Project, error) { return nil, nil }

func (f *fakeStore) ListModules(context.Context) ([]store.Module, error) {
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

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func newTestServer(t *testing.T, fs *fakeStore) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewHandler(fs, Config{IngestToken: token, TrackedBranches: []string{"main"}}, log))
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
	return do(t, req)
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
		{"no token", "", "application/json", valid, nil, http.StatusUnauthorized, "bearer token"},
		{"wrong token", "Bearer nope", "application/json", valid, nil, http.StatusUnauthorized, "bearer token"},
		{"not bearer", "Basic " + token, "application/json", valid, nil, http.StatusUnauthorized, "bearer token"},
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
	}{
		{"/api/v1/projects/7/usages", http.StatusOK},
		{"/api/v1/modules/7/consumers", http.StatusOK},
		{"/api/v1/projects/8/usages", http.StatusNotFound},
		{"/api/v1/modules/8/consumers", http.StatusNotFound},
		{"/api/v1/projects/abc/usages", http.StatusBadRequest},
		{"/api/v1/projects/0/usages", http.StatusBadRequest},
	}
	for _, tt := range tests {
		resp, body := get(t, srv.URL+tt.path)
		if resp.StatusCode != tt.wantStatus {
			t.Errorf("GET %s status = %d, want %d (body %v)", tt.path, resp.StatusCode, tt.wantStatus, body)
		}
		if tt.wantStatus == http.StatusOK {
			if usages, _ := body["usages"].([]any); len(usages) != 1 {
				t.Errorf("GET %s usages = %v, want 1", tt.path, body["usages"])
			}
		}
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
