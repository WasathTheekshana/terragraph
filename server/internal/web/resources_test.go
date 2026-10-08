package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func two(n int) *int { return &n }

// resourceFixture adds to the sample store: the infra repo's dev and prod environments, where only
// prod has a firewall, and the payments project looking up the network the infra repo creates.
func resourceFixture() *fakeStore {
	s := sampleStore()
	vnet := map[string]string{"name": "vnet-hub", "resource_group_name": "rg-net"}
	s.resources = []store.Resource{
		{ProjectID: 3, RepoID: 3, RepoURL: "git@github.com:org/infra.git", ProjectPath: "envs/dev", Address: "azurerm_virtual_network.hub", Mode: "managed",
			Type: "azurerm_virtual_network", Name: "hub", Enabled: "true", File: "main.tf", Line: 5, Attrs: vnet, Tags: map[string]string{"env": "dev", "owner": "net"}},
		{ProjectID: 4, RepoID: 3, RepoURL: "git@github.com:org/infra.git", ProjectPath: "envs/prod", Address: "azurerm_virtual_network.hub", Mode: "managed",
			Type: "azurerm_virtual_network", Name: "hub", Enabled: "true", File: "main.tf", Line: 5, Attrs: map[string]string{"name": "vnet-prod"}, Tags: map[string]string{"env": "prod", "owner": "net", "cost": "42"}},
		{ProjectID: 4, RepoID: 3, RepoURL: "git@github.com:org/infra.git", ProjectPath: "envs/prod", Address: "azurerm_firewall.fw", Mode: "managed",
			Type: "azurerm_firewall", Name: "fw", Enabled: "true", Instances: two(2), Condition: "count = var.fw ? 2 : 0", File: "fw.tf", Line: 1, Attrs: map[string]string{"name": "fw-prod"},
			TagsUnresolved: map[string]string{"*": "local.tags"}},
		{ProjectID: 3, RepoID: 3, RepoURL: "git@github.com:org/infra.git", ProjectPath: "envs/dev", Address: "azurerm_firewall.fw", Mode: "managed",
			Type: "azurerm_firewall", Name: "fw", Enabled: "false", Condition: "count = var.fw ? 2 : 0", File: "fw.tf", Line: 1, Attrs: map[string]string{"name": "fw-dev"}},
		{ProjectID: 1, RepoID: 1, RepoURL: "git@github.com:org/payments.git", ProjectPath: ".", Address: "data.azurerm_virtual_network.hub", Mode: "data",
			Type: "azurerm_virtual_network", Name: "hub", Enabled: "true", File: "data.tf", Line: 2, Attrs: map[string]string{"name": "vnet-prod", "resource_group_name": "rg-net"}},
	}
	s.inventories = map[int64]store.Inventory{
		3: {ProjectID: 3, Scanned: true, Variables: []store.Variable{{Name: "fw", Type: "bool", Value: "false", Flag: true, Source: "default", Controls: []string{"azurerm_firewall.fw"}}}},
		4: {ProjectID: 4, Scanned: true, Variables: []store.Variable{{Name: "fw", Type: "bool", Value: "true", Flag: true, Source: "tfvars", Controls: []string{"azurerm_firewall.fw"}}}},
	}
	return s
}

func TestResourcesPageListsWhatProjectsCreateAndLookUp(t *testing.T) {
	resp, body := get(t, newTestHandler(resourceFixture()), "/resources")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, want := range []string{
		"azurerm_virtual_network.hub", "data.azurerm_virtual_network.hub", "azurerm_firewall.fw", "vnet-hub", "rg-net",
		"looked up", "2 copies", "Off", "count = var.fw ? 2 : 0", "tags set by an expression",
		"Showing 1–5 of 5", "Resource types", `href="/resources?type=azurerm_firewall"`, `href="/resources?tag=env"`,
		`href="/resources?tag=env%3Ddev"`, `href="/projects/3"`, "View resource dependencies",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestResourcesPageFilters(t *testing.T) {
	h := newTestHandler(resourceFixture())
	_, body := get(t, h, "/resources?type=nothing")
	if !strings.Contains(body, "No resources found") || !strings.Contains(body, "Nothing matches this filter") {
		t.Error("an empty result should say so")
	}
	_, body = get(t, h, "/resources?type=azurerm_firewall")
	if !strings.Contains(body, "type azurerm_firewall") || !strings.Contains(body, "Clear all") || !strings.Contains(body, "Showing 1–2 of 2") {
		t.Errorf("a filtered page should show its filter and count: %s", body[:200])
	}
	if !strings.Contains(body, `name="type" value="azurerm_firewall"`) {
		t.Error("searching within a filter keeps it")
	}
	_, body = get(t, h, "/resources?mode=data")
	if !strings.Contains(body, "Showing 1–1 of 1") || !strings.Contains(body, "looked up (data sources)") {
		t.Error("data sources only")
	}
}

func TestResourcesPageRejectsABadFilter(t *testing.T) {
	resp, body := get(t, newTestHandler(resourceFixture()), "/resources?limit=0&mode=both")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "mode must be managed or data") {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestResourcesPageWithNothingRecordedExplainsWhy(t *testing.T) {
	_, body := get(t, newTestHandler(sampleStore()), "/resources")
	if !strings.Contains(body, "No resources found") || !strings.Contains(body, "scan them again") {
		t.Error("an empty inventory should say what to do")
	}
}

func TestResourcesPagePages(t *testing.T) {
	s := sampleStore()
	for i := 0; i < 230; i++ {
		s.resources = append(s.resources, store.Resource{ProjectID: 3, RepoID: 3, RepoURL: "git@github.com:org/infra.git", ProjectPath: "envs/dev",
			Address: "null_resource.n", Mode: "managed", Type: "null_resource", Name: "n", Enabled: "true"})
	}
	_, body := get(t, newTestHandler(s), "/resources")
	if !strings.Contains(body, "Showing 1–100 of 230") || !strings.Contains(body, "Page 1 of 3") || !strings.Contains(body, `href="/resources?page=2"`) {
		t.Error("the first page should link to the second")
	}
}

func TestEnvironmentsPageComparesTheProjectsOfARepo(t *testing.T) {
	resp, body := get(t, newTestHandler(resourceFixture()), "/repos/3/environments")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, want := range []string{
		"envs/dev", "envs/prod", "azurerm_firewall.fw", "fw-prod", "fw-dev", "2 copies", "Feature switches", "Other settings", "Tags",
		"switches azurerm_firewall.fw", "cost", "Only show differences", "Resources that differ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestEnvironmentsPageOnlyDifferences(t *testing.T) {
	h := newTestHandler(resourceFixture())
	_, all := get(t, h, "/repos/3/environments")
	_, diff := get(t, h, "/repos/3/environments?diff=1")
	if !strings.Contains(all, "azurerm_virtual_network.hub") {
		t.Fatal("the network is in the full comparison")
	}
	if strings.Contains(diff, "azurerm_virtual_network.hub") {
		t.Error("the network is created in both environments, so it isn't a difference")
	}
	if !strings.Contains(diff, "azurerm_firewall.fw") || !strings.Contains(diff, "Show everything") {
		t.Error("the firewall differs and should stay")
	}
}

func TestEnvironmentsPageEdgeCases(t *testing.T) {
	h := newTestHandler(resourceFixture())
	_, body := get(t, h, "/repos/1/environments")
	if !strings.Contains(body, "Nothing to compare") {
		t.Error("a repo with one project has no environments to compare")
	}
	_, body = get(t, newTestHandler(sampleStore()), "/repos/3/environments")
	if !strings.Contains(body, "No resources recorded yet") {
		t.Error("projects scanned without resources should say so")
	}
	if resp, _ := get(t, h, "/repos/99/environments"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo = %d", resp.StatusCode)
	}
	if resp, _ := get(t, h, "/repos/x/environments"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
}

func TestResourceGraphPage(t *testing.T) {
	resp, body := get(t, newTestHandler(resourceFixture()), "/resources/graph?level=resource&type=azurerm_virtual_network&tag=env%3Dprod&tag=owner&isolated=true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, want := range []string{
		"Resource dependencies", `action="/resources/graph"`, `data-src="/resources/graph/data?`, `<option value="resource" selected>`,
		`name="type" value="azurerm_virtual_network"`, `name="tag" value="env=prod"`, `name="tag" value="owner"`,
		`name="isolated" value="true" checked`, "created outside the scanned projects", `src="/static/graph.js?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(body, "Local modules") {
		t.Error("local modules belong to the module graph")
	}
	if !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, ">Resources</a>") {
		t.Error("the Resources tab should be the current one")
	}
}

func TestResourceGraphDataLinksTheLookupToItsCreator(t *testing.T) {
	resp, body := get(t, newTestHandler(resourceFixture()), "/resources/graph/data")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status = %d, type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, `"from":"project:1"`) || !strings.Contains(body, `"to":"project:4"`) || !strings.Contains(body, `"label":"azurerm_virtual_network"`) {
		t.Errorf("the payments lookup of vnet-prod should link to the prod project: %s", body)
	}
	for path, want := range map[string]int{
		"/resources/graph/data?project=999":      http.StatusNotFound,
		"/resources/graph/data?level=galaxy":     http.StatusBadRequest,
		"/resources/graph/data?tag==x":           http.StatusBadRequest,
		"/resources/graph/data?module=1":         http.StatusBadRequest,
		"/resources/graph/data?project=1&repo=3": http.StatusBadRequest,
	} {
		if resp, _ := get(t, h(), path); resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func h() http.Handler { return newTestHandler(resourceFixture()) }

func TestResourcePagesDoNotLeakStoreErrors(t *testing.T) {
	s := resourceFixture()
	s.err = http.ErrAbortHandler
	for _, path := range []string{"/resources", "/repos/3/environments"} {
		resp, body := get(t, newTestHandler(s), path)
		if resp.StatusCode != 500 || strings.Contains(body, "abort") {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
	}
	resp, body := get(t, newTestHandler(s), "/resources/graph/data")
	if resp.StatusCode != 500 || strings.Contains(body, "abort") {
		t.Errorf("graph data = %d %s", resp.StatusCode, body)
	}
}

func TestResourcePagesNeedSignIn(t *testing.T) {
	signedOut := newTestHandlerAs(resourceFixture(), nil)
	for _, path := range []string{"/resources", "/resources/graph", "/resources/graph/data", "/repos/3/environments"} {
		if resp, _ := get(t, signedOut, path); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s signed out = %d", path, resp.StatusCode)
		}
	}
}

func TestPagesLinkToTheirResources(t *testing.T) {
	h := newTestHandler(resourceFixture())
	for path, wants := range map[string][]string{
		"/projects/3": {`href="/resources?project=3"`, `href="/resources/graph?project=3"`},
		"/repos/3":    {`href="/repos/3/environments"`, `href="/resources?repo=3"`},
		"/":           {`href="/resources"`},
	} {
		_, body := get(t, h, path)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s has no %s", path, want)
			}
		}
	}
	_, body := get(t, h, "/repos/1")
	if strings.Contains(body, "Compare environments") {
		t.Error("a repo with one project has no environments to compare")
	}
}
