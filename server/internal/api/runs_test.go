package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func decodeJSON(resp *http.Response, v any) error {
	return json.NewDecoder(resp.Body).Decode(v)
}

func post(t *testing.T, url, body string, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode != http.StatusNoContent {
		_ = decodeJSON(resp, &out)
	}
	return resp, out
}

func TestCreateRun(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)

	body := `{"label": "laptop: ~/next-projects", "items": [
		{"kind": "project", "repo_url": "git@github.com:org/app.git", "path": "./envs//prod/"},
		{"kind": "project", "repo_url": "git@github.com:org/app.git", "path": ""},
		{"kind": "module_repo", "repo_url": "git@github.com:org/vpc.git", "path": "ignored"}
	]}`
	resp, out := post(t, srv.URL+"/api/v1/runs", body, map[string]string{"Idempotency-Key": "abc"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, out)
	}
	if out["run"] == nil || len(out["items"].([]any)) != 3 {
		t.Errorf("body = %v", out)
	}
	if fs.runKey != "abc" {
		t.Errorf("idempotency key = %q, want it passed to the store", fs.runKey)
	}
	gotPaths := []string{fs.runItems[0].Path, fs.runItems[1].Path, fs.runItems[2].Path}
	if fmt.Sprint(gotPaths) != fmt.Sprint([]string{"envs/prod", ".", ""}) {
		t.Errorf("normalized paths = %q", gotPaths)
	}
}

func TestCreateRunValidation(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"no label", `{"items": [{"kind": "module_repo", "repo_url": "x/y"}]}`, "label"},
		{"no items", `{"label": "l", "items": []}`, "between 1 and"},
		{"bad kind", `{"label": "l", "items": [{"kind": "cost", "repo_url": "x/y"}]}`, "items[0].kind"},
		{"no repo", `{"label": "l", "items": [{"kind": "module_repo"}]}`, "items[0].repo_url"},
		{"escaping path", `{"label": "l", "items": [{"kind": "project", "repo_url": "x/y", "path": "../other"}]}`, "items[0].path"},
		{"duplicate", `{"label": "l", "items": [{"kind": "project", "repo_url": "x/y"}, {"kind": "project", "repo_url": "x/y", "path": "."}]}`, "items[1] duplicates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &fakeStore{})
			resp, out := post(t, srv.URL+"/api/v1/runs", tt.body, nil)
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out["details"]), tt.want) {
				t.Errorf("status = %d, body = %v; want 422 mentioning %q", resp.StatusCode, out, tt.want)
			}
		})
	}
}

func TestRunEndpointsNeedToken(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	for _, path := range []string{"/api/v1/runs", "/api/v1/runs/1/items/1/scan", "/api/v1/runs/1/items/1/fail", "/api/v1/runs/1/finish"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s without a token = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestSubmitRunItem(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)
	scan := strings.Replace(validScan, "%s", "main", 1)

	resp, out := post(t, srv.URL+"/api/v1/runs/1/items/1/scan", scan, nil)
	if resp.StatusCode != http.StatusCreated || out["scan_id"] != float64(9) || !fs.ingested[0].Tracked {
		t.Fatalf("first submission: %d %v", resp.StatusCode, out)
	}
	resp, out = post(t, srv.URL+"/api/v1/runs/1/items/1/scan", scan, nil)
	if resp.StatusCode != http.StatusOK || out["duplicate"] != true {
		t.Errorf("resubmission: %d %v; want 200 marked duplicate", resp.StatusCode, out)
	}
	if resp, _ := post(t, srv.URL+"/api/v1/runs/1/items/1/scan", `{"schema_version": 1}`, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid report = %d, want 422", resp.StatusCode)
	}
	if resp, _ := post(t, srv.URL+"/api/v1/runs/1/items/2/scan", scan, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown item = %d, want 404", resp.StatusCode)
	}
}

func TestRunErrorsMapToStatuses(t *testing.T) {
	scan := strings.Replace(validScan, "%s", "main", 1)
	tests := []struct {
		err  error
		want int
	}{
		{store.ErrRunClosed, http.StatusConflict},
		{fmt.Errorf("%w: other repo", store.ErrItemMismatch), http.StatusUnprocessableEntity},
		{store.ErrNotFound, http.StatusNotFound},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		srv := newTestServer(t, &fakeStore{runErr: tt.err})
		for _, c := range []struct{ path, body string }{
			{"/api/v1/runs/1/items/1/scan", scan},
			{"/api/v1/runs/1/items/1/fail", `{"error": "boom"}`},
			{"/api/v1/runs/1/finish", `{"status": "finished"}`},
		} {
			resp, out := post(t, srv.URL+c.path, c.body, nil)
			if resp.StatusCode != tt.want {
				t.Errorf("%v on %s = %d, want %d (%v)", tt.err, c.path, resp.StatusCode, tt.want, out)
			}
		}
	}
}

func TestFailRunItemTruncates(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)
	resp, _ := post(t, srv.URL+"/api/v1/runs/1/items/1/fail", `{"error": "`+strings.Repeat("é", 3000)+`"}`, nil)
	if resp.StatusCode != http.StatusNoContent || len([]rune(fs.failMsg)) != maxItemErrorLen {
		t.Errorf("status %d, stored %d runes; want 204 and %d runes", resp.StatusCode, len([]rune(fs.failMsg)), maxItemErrorLen)
	}
}

func TestFinishRunValidatesStatus(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	if resp, _ := post(t, srv.URL+"/api/v1/runs/1/finish", `{"status": "running"}`, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status running = %d, want 422", resp.StatusCode)
	}
	if resp, out := post(t, srv.URL+"/api/v1/runs/1/finish", `{"status": "cancelled"}`, nil); resp.StatusCode != http.StatusOK || out["run"] == nil {
		t.Errorf("cancel = %d %v", resp.StatusCode, out)
	}
}

func TestReadRuns(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)

	if resp, out := get(t, srv.URL+"/api/v1/runs"); resp.StatusCode != http.StatusOK || out["runs"] == nil || fs.listLimit != defaultRunList {
		t.Errorf("list = %d %v limit %d", resp.StatusCode, out, fs.listLimit)
	}
	if resp, _ := get(t, srv.URL+"/api/v1/runs?limit=5000"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit too big = %d, want 400", resp.StatusCode)
	}
	if resp, out := get(t, srv.URL+"/api/v1/runs/1"); resp.StatusCode != http.StatusOK || out["run"] == nil || out["items"] == nil {
		t.Errorf("get = %d %v", resp.StatusCode, out)
	}
	if resp, _ := get(t, srv.URL+"/api/v1/runs/2"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", resp.StatusCode)
	}
}
