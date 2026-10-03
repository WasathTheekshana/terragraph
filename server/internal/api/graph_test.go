package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/auth"
	"github.com/WasathTheekshana/terragraph/server/internal/graph"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func ptr[T any](v T) *T { return &v }

// graphData has two projects in one repo. Both call the addons module, and one of them has a local
// module inside it that calls the vpc module.
func graphData() *graph.Input {
	const url = "git@github.com:org/infra.git"
	call := func(project int64, parent, name string, module *int64, src string) store.Usage {
		return store.Usage{ProjectID: project, Parent: parent, CallName: name, ModuleID: module, Source: src,
			PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("1.0.0")}
	}
	return &graph.Input{
		Repos: []store.Repo{{ID: 1, RepoURL: url, Projects: 2}},
		Projects: []store.Project{
			{ID: 1, RepoID: 1, RepoURL: url, Path: "envs/dev", LastBranch: "main"},
			{ID: 2, RepoID: 1, RepoURL: url, Path: "envs/prod", LastBranch: "main"},
		},
		Modules: []store.Module{
			{ID: 10, Key: "github.com/org/addons", Kind: "git"},
			{ID: 11, Key: "github.com/org/vpc", Kind: "git"},
		},
		Usages: []store.Usage{
			call(1, "", "addons", ptr(int64(10)), "git::https://github.com/org/addons.git?ref=v1.0.0"),
			call(2, "", "addons", ptr(int64(10)), "git::https://github.com/org/addons.git?ref=v1.0.0"),
			call(1, "", "helpers", nil, "./modules/helpers"),
			call(1, "helpers", "network", ptr(int64(11)), "git::https://github.com/org/vpc.git?ref=v1.0.0"),
		},
	}
}

func graphServer(t *testing.T) string {
	t.Helper()
	return newTestServer(t, &fakeStore{data: graphData()}).URL + "/api/v1/graph"
}

func ids(t *testing.T, body map[string]any, key string) []string {
	t.Helper()
	list, ok := body[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", key, body[key])
	}
	var out []string
	for _, item := range list {
		m := item.(map[string]any)
		if key == "nodes" {
			out = append(out, m["id"].(string))
		} else {
			out = append(out, m["from"].(string)+">"+m["to"].(string))
		}
	}
	return out
}

func contains(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, s := range list {
			if s == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestGraphOfEverything(t *testing.T) {
	resp, body := get(t, graphServer(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	nodes, edges := ids(t, body, "nodes"), ids(t, body, "edges")
	if !contains(nodes, "repo:1", "project:1", "project:2", "module:10", "module:11") {
		t.Errorf("nodes = %v", nodes)
	}
	if !contains(edges, "project:1>module:10", "project:2>module:10", "project:1>module:11") {
		t.Errorf("edges = %v", edges)
	}
	if body["level"] != "project" || body["truncated"] != false {
		t.Errorf("level = %v, truncated = %v", body["level"], body["truncated"])
	}
	stats := body["stats"].(map[string]any)
	if stats["projects"] != float64(2) || stats["modules"] != float64(2) {
		t.Errorf("stats = %v", stats)
	}
}

func TestGraphFocusedOnAProject(t *testing.T) {
	_, body := get(t, graphServer(t)+"?project=2")
	nodes := ids(t, body, "nodes")
	if !contains(nodes, "project:2", "module:10") || contains(nodes, "project:1") || contains(nodes, "module:11") {
		t.Errorf("nodes = %v, want project 2 and the addons module only", nodes)
	}
}

func TestGraphFocusedOnAModuleWithDirectionAndDepth(t *testing.T) {
	_, body := get(t, graphServer(t)+"?module=10&direction=up&depth=1")
	nodes := ids(t, body, "nodes")
	if !contains(nodes, "module:10", "project:1", "project:2") || contains(nodes, "module:11") {
		t.Errorf("nodes = %v", nodes)
	}
}

func TestGraphAtRepoLevel(t *testing.T) {
	_, body := get(t, graphServer(t)+"?level=repo")
	nodes := ids(t, body, "nodes")
	if !contains(nodes, "repo:1", "module:10") || contains(nodes, "project:1") {
		t.Errorf("nodes = %v", nodes)
	}
	if body["level"] != "repo" {
		t.Errorf("level = %v", body["level"])
	}
}

func TestGraphShowsLocalModulesWhenAsked(t *testing.T) {
	_, folded := get(t, graphServer(t))
	if contains(ids(t, folded, "nodes"), "local:1:helpers") {
		t.Error("local modules are folded away by default")
	}
	_, shown := get(t, graphServer(t)+"?locals=true")
	if !contains(ids(t, shown, "nodes"), "local:1:helpers") {
		t.Errorf("nodes = %v", ids(t, shown, "nodes"))
	}
}

func TestGraphMaxNodesFallsBackToRepoLevel(t *testing.T) {
	_, body := get(t, graphServer(t)+"?max_nodes=3")
	if body["level"] != "repo" {
		t.Errorf("level = %v, want repo once the graph is over the limit", body["level"])
	}
}

func TestGraphOfNothingIsEmptyNotNull(t *testing.T) {
	_, body := get(t, newTestServer(t, &fakeStore{data: &graph.Input{}}).URL+"/api/v1/graph")
	if nodes, ok := body["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Errorf("nodes = %v", body["nodes"])
	}
	if edges, ok := body["edges"].([]any); !ok || len(edges) != 0 {
		t.Errorf("edges = %v", body["edges"])
	}
}

func TestGraphAsksAreChecked(t *testing.T) {
	base := graphServer(t)
	notFound := []string{"?project=99", "?repo=99", "?module=99"}
	for _, q := range notFound {
		if resp, body := get(t, base+q); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d %v, want 404", q, resp.StatusCode, body)
		}
	}
	bad := []string{
		"?project=abc", "?project=0", "?project=-1", "?repo=x", "?module=1.5",
		"?direction=sideways", "?level=galaxy", "?depth=-1", "?depth=many", "?locals=maybe",
		"?max_nodes=0", "?max_nodes=99999", "?max_nodes=lots",
		"?project=1&repo=1", "?project=1&module=10",
	}
	for _, q := range bad {
		if resp, body := get(t, base+q); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s = %d %v, want 400", q, resp.StatusCode, body)
		}
	}
}

func TestGraphNeedsReadAccess(t *testing.T) {
	srv := newTestServer(t, &fakeStore{data: graphData()})
	resp, err := http.Get(srv.URL + "/api/v1/graph")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without credentials = %d, want 401", resp.StatusCode)
	}

	ingestOnly := newTestServerAs(t, &fakeStore{data: graphData()}, auth.Principal{CanIngest: true}, nil)
	if resp, _ := get(t, ingestOnly.URL+"/api/v1/graph"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("with an ingest-only credential = %d, want 403", resp.StatusCode)
	}
}

func TestGraphErrorsMentionWhatWasWrong(t *testing.T) {
	_, body := get(t, graphServer(t)+"?project=99")
	if msg, _ := body["error"].(string); !strings.Contains(msg, "project 99") {
		t.Errorf("error = %q", msg)
	}
	_, body = get(t, graphServer(t)+"?direction=sideways")
	if msg, _ := body["error"].(string); !strings.Contains(msg, "direction") {
		t.Errorf("error = %q", msg)
	}
}
