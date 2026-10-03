package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGraphPageLoadsItsScriptAndData(t *testing.T) {
	resp, body := get(t, newTestHandler(sampleStore()), "/graph?project=3&direction=up&depth=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, want := range []string{
		`src="/static/graph.js?v=`,
		`href="/static/graph.css?v=`,
		`data-src="/graph/data?depth=2&amp;direction=up&amp;project=3"`,
		`data-focus="project:3"`,
		`name="project" value="3"`,
		`<option value="up" selected>`,
		"Show everything",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q, want scripts from this server only", csp)
	}
}

func TestGraphPageOfEverythingHasNoFocusControls(t *testing.T) {
	_, body := get(t, newTestHandler(sampleStore()), "/graph")
	if strings.Contains(body, "Show everything") || strings.Contains(body, `name="direction"`) {
		t.Error("an unfocused graph has nothing to narrow")
	}
	if !strings.Contains(body, `data-src="/graph/data"`) {
		t.Error("the page should ask for the whole graph")
	}
}

func TestGraphDataIsTheGraph(t *testing.T) {
	resp, body := get(t, newTestHandler(sampleStore()), "/graph/data")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status = %d, type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var g struct {
		Nodes []struct{ ID string }
		Edges []struct{ From, To string }
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, want := range []string{"project:1", "project:2", "module:10"} {
		if !ids[want] {
			t.Errorf("nodes are missing %s: %v", want, ids)
		}
	}
	if len(g.Edges) == 0 {
		t.Error("no edges")
	}
}

func TestGraphDataFocusAndErrors(t *testing.T) {
	h := newTestHandler(sampleStore())
	if resp, _ := get(t, h, "/graph/data?project=2"); resp.StatusCode != http.StatusOK {
		t.Errorf("focus on a project = %d", resp.StatusCode)
	}
	for path, want := range map[string]int{
		"/graph/data?project=999":        http.StatusNotFound,
		"/graph/data?direction=sideways": http.StatusBadRequest,
		"/graph/data?depth=-1":           http.StatusBadRequest,
	} {
		resp, body := get(t, h, path)
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
		if !strings.Contains(body, `"error"`) {
			t.Errorf("GET %s body = %q, want a JSON error", path, body)
		}
	}
}

func TestGraphDataDoesNotLeakStoreErrors(t *testing.T) {
	s := sampleStore()
	s.err = http.ErrAbortHandler
	resp, body := get(t, newTestHandler(s), "/graph/data")
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(body, "abort") {
		t.Errorf("status = %d, body = %q", resp.StatusCode, body)
	}
}

func TestGraphNeedsSignIn(t *testing.T) {
	h := newTestHandlerAs(sampleStore(), nil)
	for _, path := range []string{"/graph", "/graph/data"} {
		if resp, _ := get(t, h, path); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s signed out = %d, want a redirect to sign in", path, resp.StatusCode)
		}
	}
}

func TestPagesLinkToTheirGraph(t *testing.T) {
	h := newTestHandler(sampleStore())
	for path, want := range map[string]string{
		"/repos/3":    `href="/graph?repo=3"`,
		"/projects/1": `href="/graph?project=1"`,
		"/modules/10": `href="/graph?module=10"`,
		"/":           `href="/graph"`,
	} {
		if _, body := get(t, h, path); !strings.Contains(body, want) {
			t.Errorf("GET %s has no %s", path, want)
		}
	}
}
