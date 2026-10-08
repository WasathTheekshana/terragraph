package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/graph"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func (f *fakeStore) ListResources(_ context.Context, fl store.ResourceFilter) ([]store.Resource, error) {
	if f.resErr != nil {
		return nil, f.resErr
	}
	var out []store.Resource
	for _, r := range f.res {
		if fl.ProjectID != 0 && r.ProjectID != fl.ProjectID || fl.RepoID != 0 && r.RepoID != fl.RepoID ||
			fl.Type != "" && r.Type != fl.Type || fl.Mode != "" && r.Mode != fl.Mode {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) CountResources(ctx context.Context, fl store.ResourceFilter) (int, error) {
	fl.Limit = 0
	r, err := f.ListResources(ctx, fl)
	return len(r), err
}

func (f *fakeStore) ResourceFacets(_ context.Context, _ store.ResourceFilter) (store.Facets, error) {
	return store.Facets{Types: []store.Count{{Value: "azurerm_virtual_network", Count: 2}}, TagKeys: []store.Count{}}, f.resErr
}

func (f *fakeStore) ListInventories(context.Context) (map[int64]store.Inventory, error) {
	return f.inv, f.resErr
}

func (f *fakeStore) ProjectInventory(_ context.Context, id int64) (store.Inventory, error) {
	for _, p := range f.data.Projects {
		if p.ID == id {
			if inv, ok := f.inv[id]; ok {
				return inv, nil
			}
			return store.Inventory{ProjectID: id, Variables: []store.Variable{}, Outputs: []store.Output{}}, nil
		}
	}
	return store.Inventory{}, store.ErrNotFound
}

// resourceStore has a hub project creating a network that two environments of an app repo look up.
func resourceStore() *fakeStore {
	const hub, apps = "git@github.com:org/network.git", "git@github.com:org/apps.git"
	vnet := map[string]string{"name": "vnet-hub", "resource_group_name": "rg-net"}
	return &fakeStore{
		data: &graph.Input{
			Repos: []store.Repo{{ID: 1, RepoURL: hub}, {ID: 2, RepoURL: apps}},
			Projects: []store.Project{
				{ID: 1, RepoID: 1, RepoURL: hub, Path: ".", LastBranch: "main"},
				{ID: 2, RepoID: 2, RepoURL: apps, Path: "envs/dev", LastBranch: "main"},
				{ID: 3, RepoID: 2, RepoURL: apps, Path: "envs/prod", LastBranch: "main"},
			},
		},
		res: []store.Resource{
			{ProjectID: 1, RepoID: 1, Address: "azurerm_virtual_network.hub", Mode: "managed", Type: "azurerm_virtual_network", Name: "hub", Enabled: "true",
				Attrs: vnet, Tags: map[string]string{"env": "shared"}},
			{ProjectID: 2, RepoID: 2, Address: "data.azurerm_virtual_network.hub", Mode: "data", Type: "azurerm_virtual_network", Name: "hub", Enabled: "true", Attrs: vnet},
			{ProjectID: 3, RepoID: 2, Address: "data.azurerm_virtual_network.hub", Mode: "data", Type: "azurerm_virtual_network", Name: "hub", Enabled: "true", Attrs: vnet},
			{ProjectID: 3, RepoID: 2, Address: "azurerm_firewall.fw", Mode: "managed", Type: "azurerm_firewall", Name: "fw", Enabled: "true", Attrs: map[string]string{"name": "fw-prod"}},
		},
		inv: map[int64]store.Inventory{
			2: {ProjectID: 2, Scanned: true, Variables: []store.Variable{{Name: "enable_firewall", Value: "false", Flag: true}}},
			3: {ProjectID: 3, Scanned: true, Variables: []store.Variable{{Name: "enable_firewall", Value: "true", Flag: true}}},
		},
	}
}

func resourceURL(t *testing.T, path string, query url.Values) string {
	t.Helper()
	srv := newTestServer(t, resourceStore())
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return srv.URL + path
}

func TestListResources(t *testing.T) {
	resp, body := get(t, resourceURL(t, "/api/v1/resources", nil))
	if resp.StatusCode != http.StatusOK || body["total"] != float64(4) || len(body["resources"].([]any)) != 4 {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, body)
	}
	if body["limit"] != float64(store.DefaultResourcePage) || body["offset"] != float64(0) {
		t.Errorf("paging = %v, %v", body["limit"], body["offset"])
	}
	_, body = get(t, resourceURL(t, "/api/v1/resources", url.Values{"type": {"azurerm_firewall"}}))
	if body["total"] != float64(1) {
		t.Errorf("by type = %v", body)
	}
	_, body = get(t, resourceURL(t, "/api/v1/resources", url.Values{"mode": {"data"}, "repo": {"2"}}))
	if body["total"] != float64(2) {
		t.Errorf("data sources of a repo = %v", body)
	}
}

func TestEmptyResourceListIsNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{data: &graph.Input{}})
	_, body := get(t, srv.URL+"/api/v1/resources")
	if list, ok := body["resources"].([]any); !ok || len(list) != 0 {
		t.Errorf("resources = %v", body["resources"])
	}
}

func TestResourceFiltersAreChecked(t *testing.T) {
	for _, q := range []string{
		"?project=abc", "?project=0", "?repo=-1", "?mode=both", "?enabled=maybe", "?tag==x", "?limit=0", "?limit=99999", "?limit=x", "?offset=-1",
	} {
		resp, body := get(t, resourceURL(t, "/api/v1/resources", nil)+q)
		if resp.StatusCode != http.StatusBadRequest || body["error"] == nil {
			t.Errorf("GET %s = %d %v, want 400", q, resp.StatusCode, body)
		}
	}
}

func TestResourceFacets(t *testing.T) {
	resp, body := get(t, resourceURL(t, "/api/v1/resources/facets", nil))
	if resp.StatusCode != http.StatusOK || len(body["types"].([]any)) != 1 || body["tag_keys"] == nil {
		t.Errorf("status = %d, body = %v", resp.StatusCode, body)
	}
	if resp, _ := get(t, resourceURL(t, "/api/v1/resources/facets", nil)+"?mode=x"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad filter = %d", resp.StatusCode)
	}
}

func TestResourceGraphFindsTheCreatorOfWhatIsLookedUp(t *testing.T) {
	resp, body := get(t, resourceURL(t, "/api/v1/resources/graph", nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	if edges := ids(t, body, "edges"); !contains(edges, "project:2>project:1", "project:3>project:1") {
		t.Errorf("edges = %v", edges)
	}
	if body["level"] != "project" {
		t.Errorf("level = %v", body["level"])
	}

	_, body = get(t, resourceURL(t, "/api/v1/resources/graph", url.Values{"level": {"resource"}, "project": {"3"}}))
	if nodes := ids(t, body, "nodes"); !contains(nodes, "resource:3:data.azurerm_virtual_network.hub", "resource:1:azurerm_virtual_network.hub") {
		t.Errorf("nodes = %v", nodes)
	}
	_, body = get(t, resourceURL(t, "/api/v1/resources/graph", url.Values{"level": {"repo"}}))
	if edges := ids(t, body, "edges"); !contains(edges, "repo:2>repo:1") {
		t.Errorf("repo edges = %v", edges)
	}
	_, body = get(t, resourceURL(t, "/api/v1/resources/graph", url.Values{"type": {"azurerm_firewall"}}))
	if edges := ids(t, body, "edges"); len(edges) != 0 {
		t.Errorf("edges for a type nothing looks up = %v", edges)
	}
}

func TestResourceGraphErrors(t *testing.T) {
	for q, want := range map[string]int{
		"?project=99":                   http.StatusNotFound,
		"?repo=99":                      http.StatusNotFound,
		"?level=galaxy":                 http.StatusBadRequest,
		"?module=1":                     http.StatusBadRequest,
		"?direction=sideways&project=1": http.StatusBadRequest,
		"?tag==x":                       http.StatusBadRequest,
		"?isolated=maybe":               http.StatusBadRequest,
		"?max_nodes=0":                  http.StatusBadRequest,
	} {
		if resp, body := get(t, resourceURL(t, "/api/v1/resources/graph", nil)+q); resp.StatusCode != want {
			t.Errorf("GET %s = %d %v, want %d", q, resp.StatusCode, body, want)
		}
	}
}

func TestProjectInventory(t *testing.T) {
	srv := newTestServer(t, resourceStore())
	resp, body := get(t, srv.URL+"/api/v1/projects/3/inventory")
	inv, _ := body["inventory"].(map[string]any)
	if resp.StatusCode != http.StatusOK || inv["scanned"] != true || len(inv["variables"].([]any)) != 1 {
		t.Errorf("status = %d, body = %v", resp.StatusCode, body)
	}
	_, body = get(t, srv.URL+"/api/v1/projects/1/inventory")
	if inv, _ := body["inventory"].(map[string]any); inv["scanned"] != false || inv["variables"] == nil {
		t.Errorf("an unscanned project = %v", body)
	}
	if resp, _ := get(t, srv.URL+"/api/v1/projects/99/inventory"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/api/v1/projects/x/inventory"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
}

func TestRepoEnvironments(t *testing.T) {
	srv := newTestServer(t, resourceStore())
	resp, body := get(t, srv.URL+"/api/v1/repos/2/environments")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	envs := body["environments"].([]any)
	if len(envs) != 2 || envs[0].(map[string]any)["path"] != "envs/dev" {
		t.Errorf("environments = %v", envs)
	}
	var fw map[string]any
	for _, r := range body["resources"].([]any) {
		if row := r.(map[string]any); row["address"] == "azurerm_firewall.fw" {
			fw = row
		}
	}
	cells := fw["cells"].([]any)
	if fw["differs"] != true || cells[0].(map[string]any)["state"] != "absent" || cells[1].(map[string]any)["state"] != "enabled" {
		t.Errorf("firewall row = %v", fw)
	}
	flags := body["flags"].([]any)
	if len(flags) != 1 || flags[0].(map[string]any)["differs"] != true {
		t.Errorf("flags = %v", flags)
	}
	if resp, _ := get(t, srv.URL+"/api/v1/repos/99/environments"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo = %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/api/v1/repos/x/environments"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
}

func TestResourceEndpointsNeedReadAccess(t *testing.T) {
	srv := newTestServer(t, resourceStore())
	for _, path := range []string{"/api/v1/resources", "/api/v1/resources/facets", "/api/v1/resources/graph", "/api/v1/projects/1/inventory", "/api/v1/repos/2/environments"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without credentials = %d", path, resp.StatusCode)
		}
	}
}

func TestResourceStoreErrorsAreNotLeaked(t *testing.T) {
	s := resourceStore()
	s.resErr = context.DeadlineExceeded
	srv := newTestServer(t, s)
	for _, path := range []string{"/api/v1/resources", "/api/v1/resources/facets", "/api/v1/resources/graph", "/api/v1/repos/2/environments"} {
		resp, body := get(t, srv.URL+path)
		if resp.StatusCode != http.StatusInternalServerError || strings.Contains(strings.ToLower(body["error"].(string)), "deadline") {
			t.Errorf("GET %s = %d %v", path, resp.StatusCode, body)
		}
	}
}
