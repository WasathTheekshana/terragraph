package graph

import (
	"errors"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const (
	rsNetURL  = "git@github.com:acme/network.git"
	rsAppsURL = "git@github.com:acme/apps.git"
	rsDataURL = "git@github.com:acme/data.git"
)

type fixture struct {
	in ResourceInput
}

func (f *fixture) project(id, repo int64, url, path string) {
	f.in.Projects = append(f.in.Projects, store.Project{ID: id, RepoID: repo, RepoURL: url, Path: path, LastBranch: "main"})
	seen := false
	for _, r := range f.in.Repos {
		if r.ID == repo {
			seen = true
		}
	}
	if !seen {
		f.in.Repos = append(f.in.Repos, store.Repo{ID: repo, RepoURL: url})
	}
}

func (f *fixture) managed(project int64, address, typ, name string, attrs, tags map[string]string) *store.Resource {
	r := store.Resource{ProjectID: project, Address: address, Mode: "managed", Type: typ, Name: name, Enabled: "true",
		Attrs: attrs, Tags: tags, File: "main.tf", Line: 4}
	f.in.Resources = append(f.in.Resources, r)
	return &f.in.Resources[len(f.in.Resources)-1]
}

func (f *fixture) data(project int64, address, typ, name string, attrs map[string]string) {
	f.in.Resources = append(f.in.Resources, store.Resource{ProjectID: project, Address: address, Mode: "data", Type: typ, Name: name,
		Enabled: "true", Attrs: attrs, File: "data.tf", Line: 9})
}

func (f *fixture) state(project int64, key string) {
	if f.in.Inventories == nil {
		f.in.Inventories = map[int64]store.Inventory{}
	}
	f.in.Inventories[project] = store.Inventory{ProjectID: project, Backend: "azurerm", StateKey: key}
}

func (f *fixture) build(t *testing.T, opts ResourceOptions) Graph {
	t.Helper()
	g, err := BuildResources(f.in, opts)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func nodeMap(g Graph) map[string]Node {
	out := map[string]Node{}
	for _, n := range g.Nodes {
		out[n.ID] = n
	}
	return out
}

func edgeOf(t *testing.T, g Graph, from, to string) Edge {
	t.Helper()
	for _, e := range g.Edges {
		if e.From == from && e.To == to {
			return e
		}
	}
	var have []string
	for _, e := range g.Edges {
		have = append(have, e.From+">"+e.To)
	}
	t.Fatalf("no edge %s > %s; have %v", from, to, have)
	return Edge{}
}

// hub creates the shared network, and two environments of one app repo look it up.
func hub() *fixture {
	f := &fixture{}
	f.project(1, 1, rsNetURL, "envs/hub")
	f.project(2, 2, rsAppsURL, "envs/dev")
	f.project(3, 2, rsAppsURL, "envs/prod")
	f.state(1, "azurerm:st/tf/net.tfstate")
	f.managed(1, "azurerm_virtual_network.hub", "azurerm_virtual_network", "hub",
		map[string]string{"name": "vnet-hub", "resource_group_name": "rg-net"}, map[string]string{"env": "shared", "owner": "net"})
	f.data(2, "data.azurerm_virtual_network.hub", "azurerm_virtual_network", "hub",
		map[string]string{"name": "vnet-hub", "resource_group_name": "rg-net"})
	f.data(3, "data.azurerm_virtual_network.hub", "azurerm_virtual_network", "hub",
		map[string]string{"name": "VNET-HUB", "resource_group_name": "rg-net"})
	return f
}

func TestAProjectThatLooksUpWhatAnotherCreatesIsLinkedToIt(t *testing.T) {
	g := hub().build(t, ResourceOptions{})
	nodes := nodeMap(g)
	for _, id := range []string{"project:1", "project:2", "project:3", "repo:1", "repo:2"} {
		if _, ok := nodes[id]; !ok {
			t.Errorf("missing node %s: %v", id, nodes)
		}
	}
	if nodes["project:2"].Group != "repo:2" || nodes["project:3"].Group != "repo:2" {
		t.Error("the two environments belong to their repo")
	}
	e := edgeOf(t, g, "project:2", "project:1")
	if e.Label != "azurerm_virtual_network" || e.Status != StatusCurrent || e.Calls != 1 || len(e.Origins) != 1 || e.Origins[0] != OriginLookup {
		t.Errorf("edge = %+v", e)
	}
	if len(e.Details) != 1 || !strings.Contains(e.Details[0], "data.azurerm_virtual_network.hub") || !strings.Contains(e.Details[0], "data.tf:9") ||
		!strings.Contains(e.Details[0], "azurerm_virtual_network.hub") {
		t.Errorf("details = %v", e.Details)
	}
	edgeOf(t, g, "project:3", "project:1")
	if g.Stats.Links != 2 || g.Stats.External != 0 {
		t.Errorf("stats = %+v", g.Stats)
	}
	if nodes["project:1"].Sub != "1 created, 0 looked up" {
		t.Errorf("sub = %q", nodes["project:1"].Sub)
	}
}

func TestALookupInTheCreatingProjectItselfIsNotALink(t *testing.T) {
	f := hub()
	f.data(1, "data.azurerm_virtual_network.self", "azurerm_virtual_network", "self", map[string]string{"name": "vnet-hub"})
	g := f.build(t, ResourceOptions{})
	for _, e := range g.Edges {
		if e.From == e.To {
			t.Errorf("self edge %+v", e)
		}
	}
}

func TestTheResourceGroupDecidesBetweenSameNamedResources(t *testing.T) {
	f := hub()
	f.managed(1, "azurerm_virtual_network.other", "azurerm_virtual_network", "other",
		map[string]string{"name": "vnet-hub", "resource_group_name": "rg-other"}, nil)
	g := f.build(t, ResourceOptions{Level: "resource"})
	nodes := nodeMap(g)
	if _, ok := nodes["resource:1:azurerm_virtual_network.other"]; ok {
		t.Error("a resource in another resource group is not what the lookup finds")
	}
	if e := edgeOf(t, g, "resource:2:data.azurerm_virtual_network.hub", "resource:1:azurerm_virtual_network.hub"); e.Status != StatusCurrent {
		t.Errorf("edge = %+v", e)
	}
}

func TestSeveralMatchesAreMarkedAmbiguous(t *testing.T) {
	f := hub()
	f.project(4, 3, rsDataURL, "envs/hub2")
	f.managed(4, "azurerm_virtual_network.hub", "azurerm_virtual_network", "hub",
		map[string]string{"name": "vnet-hub", "resource_group_name": "rg-net"}, nil)
	g := f.build(t, ResourceOptions{})
	for _, to := range []string{"project:1", "project:4"} {
		if e := edgeOf(t, g, "project:2", to); e.Status != StatusUnknown {
			t.Errorf("edge to %s = %+v, want it flagged as one of several matches", to, e)
		}
	}
}

func TestAMatchWithoutAResourceGroupIsNotExact(t *testing.T) {
	f := &fixture{}
	f.project(1, 1, rsNetURL, ".")
	f.project(2, 2, rsAppsURL, ".")
	f.managed(1, "azurerm_key_vault.kv", "azurerm_key_vault", "kv", map[string]string{"name": "kv-shared"}, nil)
	f.in.Resources[0].Unresolved = map[string]string{"resource_group_name": "azurerm_resource_group.rg.name"}
	f.data(2, "data.azurerm_key_vault.kv", "azurerm_key_vault", "kv", map[string]string{"name": "kv-shared", "resource_group_name": "rg-sec"})
	e := edgeOf(t, f.build(t, ResourceOptions{}), "project:2", "project:1")
	if e.Status != StatusUnknown || !strings.Contains(e.Details[0], "name only") {
		t.Errorf("edge = %+v", e)
	}
}

func TestLookupsNobodyCreatesGoToTheOutside(t *testing.T) {
	f := hub()
	f.data(2, "data.azurerm_key_vault.shared", "azurerm_key_vault", "shared", map[string]string{"name": "kv-platform", "resource_group_name": "rg-sec"})
	g := f.build(t, ResourceOptions{})
	e := edgeOf(t, g, "project:2", "external")
	if e.Label != "azurerm_key_vault" || !strings.Contains(e.Details[0], "kv-platform") {
		t.Errorf("edge = %+v", e)
	}
	if n := nodeMap(g)["external"]; n.Type != TypeExternal || !n.External {
		t.Errorf("node = %+v", n)
	}
	if g.Stats.External != 1 {
		t.Errorf("stats = %+v", g.Stats)
	}

	r := f.build(t, ResourceOptions{Level: "resource"})
	if n := nodeMap(r)["external:azurerm_key_vault:kv-platform"]; n.Label != "azurerm_key_vault" || n.Sub != "kv-platform" {
		t.Errorf("resource level external = %+v", n)
	}
}

func TestLookupsWithNothingToLookUpByAreIgnored(t *testing.T) {
	f := hub()
	f.data(2, "data.azurerm_client_config.current", "azurerm_client_config", "current", nil)
	g := f.build(t, ResourceOptions{})
	for _, e := range g.Edges {
		if strings.Contains(e.Label, "client_config") {
			t.Errorf("edge %+v", e)
		}
	}
	if g.Stats.Unresolved != 0 {
		t.Errorf("stats = %+v", g.Stats)
	}
}

func TestLookupsThatDependOnComputedValuesAreCountedButNotDrawn(t *testing.T) {
	f := hub()
	f.in.Resources = append(f.in.Resources, store.Resource{ProjectID: 2, Address: "data.azurerm_virtual_network.dyn", Mode: "data",
		Type: "azurerm_virtual_network", Name: "dyn", Enabled: "true", Unresolved: map[string]string{"name": "module.x.name"}})
	g := f.build(t, ResourceOptions{})
	if g.Stats.Unresolved != 1 {
		t.Errorf("stats = %+v", g.Stats)
	}
	if nodeMap(g)["project:2"].Details == nil {
		t.Fatal("no details")
	}
	found := false
	for _, d := range nodeMap(g)["project:2"].Details {
		found = found || d.Label == "Unresolved lookups"
	}
	if !found {
		t.Error("the project should say how many of its lookups couldn't be resolved")
	}

	// A creator whose name can't be read might be the target, so a lookup isn't called external.
	f2 := &fixture{}
	f2.project(1, 1, rsNetURL, ".")
	f2.project(2, 2, rsAppsURL, ".")
	f2.managed(1, "azurerm_virtual_network.v", "azurerm_virtual_network", "v", nil, nil)
	f2.in.Resources[0].Unresolved = map[string]string{"name": "var.name"}
	f2.data(2, "data.azurerm_virtual_network.v", "azurerm_virtual_network", "v", map[string]string{"name": "vnet-x"})
	g2 := f2.build(t, ResourceOptions{})
	if len(g2.Edges) != 0 || g2.Stats.Unresolved != 1 {
		t.Errorf("edges = %v, stats = %+v", g2.Edges, g2.Stats)
	}
}

func TestDisabledResourcesAreNotLookedUpOrFound(t *testing.T) {
	f := hub()
	f.in.Resources[0].Enabled = "false"
	g := f.build(t, ResourceOptions{})
	for _, e := range g.Edges {
		if e.To == "project:1" {
			t.Errorf("a resource switched off isn't there to find: %+v", e)
		}
	}
	f = hub()
	f.in.Resources[1].Enabled = "false"
	if e := f.build(t, ResourceOptions{}); len(e.Edges) != 1 {
		t.Errorf("a lookup switched off reads nothing: %v", e.Edges)
	}
}

func TestRemoteStateLinksToTheProjectThatKeepsIt(t *testing.T) {
	f := &fixture{}
	f.project(1, 1, rsNetURL, ".")
	f.project(2, 2, rsAppsURL, ".")
	f.state(1, "azurerm:st/tf/net.tfstate")
	f.data(2, "data.terraform_remote_state.net", "terraform_remote_state", "net",
		map[string]string{"state_key": "azurerm:st/tf/net.tfstate", "backend": "azurerm"})
	f.data(2, "data.terraform_remote_state.old", "terraform_remote_state", "old",
		map[string]string{"state_key": "azurerm:st/tf/gone.tfstate", "backend": "azurerm"})
	g := f.build(t, ResourceOptions{})
	e := edgeOf(t, g, "project:2", "project:1")
	if e.Label != "terraform state" || e.Origins[0] != OriginState || e.Status != StatusCurrent {
		t.Errorf("edge = %+v", e)
	}
	edgeOf(t, g, "project:2", "external")

	r := f.build(t, ResourceOptions{Level: "resource"})
	if e := edgeOf(t, r, "resource:2:data.terraform_remote_state.net", "state:1"); e.Status != StatusCurrent {
		t.Errorf("edge = %+v", e)
	}
	if n := nodeMap(r)["state:1"]; n.Group != "project:1" || n.Sub != "azurerm:st/tf/net.tfstate" {
		t.Errorf("state node = %+v", n)
	}
}

func TestAwsLookupsByTag(t *testing.T) {
	f := &fixture{}
	f.project(1, 1, rsNetURL, ".")
	f.project(2, 2, rsAppsURL, ".")
	f.managed(1, "aws_vpc.main", "aws_vpc", "main", map[string]string{"cidr_block": "10.0.0.0/16"}, map[string]string{"Name": "shared-vpc"})
	f.data(2, "data.aws_vpc.shared", "aws_vpc", "shared", map[string]string{"tag.Name": "shared-vpc"})
	e := edgeOf(t, f.build(t, ResourceOptions{}), "project:2", "project:1")
	if e.Label != "aws_vpc" {
		t.Errorf("edge = %+v", e)
	}
}

func TestEdgesSayHowManyOfEachTypeTheyStandFor(t *testing.T) {
	f := hub()
	f.managed(1, "azurerm_subnet.a", "azurerm_subnet", "a", map[string]string{"name": "snet-a"}, nil)
	f.managed(1, "azurerm_subnet.b", "azurerm_subnet", "b", map[string]string{"name": "snet-b"}, nil)
	f.data(2, "data.azurerm_subnet.a", "azurerm_subnet", "a", map[string]string{"name": "snet-a"})
	f.data(2, "data.azurerm_subnet.b", "azurerm_subnet", "b", map[string]string{"name": "snet-b"})
	e := edgeOf(t, f.build(t, ResourceOptions{}), "project:2", "project:1")
	if e.Calls != 3 || e.Label != "azurerm_subnet ×2 +1" || len(e.Items) != 2 || len(e.Details) != 3 {
		t.Errorf("edge = %+v", e)
	}
}

func TestRepoLevelMergesTheEnvironments(t *testing.T) {
	g := hub().build(t, ResourceOptions{Level: "repo"})
	nodes := nodeMap(g)
	if _, ok := nodes["project:2"]; ok {
		t.Error("projects are folded into their repo")
	}
	e := edgeOf(t, g, "repo:2", "repo:1")
	if e.Calls != 2 || e.Label != "azurerm_virtual_network ×2" || len(e.Projects) != 2 {
		t.Errorf("edge = %+v", e)
	}
	if nodes["repo:2"].Sub != "0 created, 2 looked up" {
		t.Errorf("sub = %q", nodes["repo:2"].Sub)
	}
}

func TestLookupsBetweenEnvironmentsOfOneRepoAreNotSelfEdgesAtRepoLevel(t *testing.T) {
	f := &fixture{}
	f.project(1, 1, rsAppsURL, "envs/shared")
	f.project(2, 1, rsAppsURL, "envs/prod")
	f.managed(1, "azurerm_resource_group.rg", "azurerm_resource_group", "rg", map[string]string{"name": "rg-x"}, nil)
	f.data(2, "data.azurerm_resource_group.rg", "azurerm_resource_group", "rg", map[string]string{"name": "rg-x"})
	if g := f.build(t, ResourceOptions{Level: "repo"}); len(g.Edges) != 0 {
		t.Errorf("edges = %v", g.Edges)
	}
	if g := f.build(t, ResourceOptions{}); len(g.Edges) != 1 {
		t.Errorf("at project level the two environments are still linked: %v", g.Edges)
	}
}

func TestResourceLevelDrawsEachResourceInsideItsProject(t *testing.T) {
	g := hub().build(t, ResourceOptions{Level: "resource"})
	nodes := nodeMap(g)
	prod := nodes["resource:3:data.azurerm_virtual_network.hub"]
	if prod.Type != TypeResource || prod.Group != "project:3" || prod.Label != "azurerm_virtual_network.hub" || prod.Sub != "VNET-HUB" {
		t.Errorf("consumer = %+v", prod)
	}
	hubNode := nodes["resource:1:azurerm_virtual_network.hub"]
	if hubNode.Group != "project:1" || hubNode.Sub != "vnet-hub" {
		t.Errorf("producer = %+v", hubNode)
	}
	gotTags := false
	for _, d := range hubNode.Details {
		if d.Label == "Tags" && d.Value == "env=shared, owner=net" {
			gotTags = true
		}
	}
	if !gotTags {
		t.Errorf("details = %+v", hubNode.Details)
	}
	if nodes["project:3"].Group != "repo:2" {
		t.Error("projects stay in their repo at the resource level")
	}
	if g.Level != "resource" {
		t.Errorf("level = %s", g.Level)
	}
}

func TestFilteringByTypeAndTag(t *testing.T) {
	f := hub()
	f.managed(1, "azurerm_subnet.a", "azurerm_subnet", "a", map[string]string{"name": "snet-a"}, map[string]string{"env": "dev"})
	f.data(3, "data.azurerm_subnet.a", "azurerm_subnet", "a", map[string]string{"name": "snet-a"})

	g := f.build(t, ResourceOptions{Type: "azurerm_subnet"})
	if len(g.Edges) != 1 || g.Edges[0].From != "project:3" {
		t.Errorf("by type: %v", g.Edges)
	}
	g = f.build(t, ResourceOptions{Tags: map[string]string{"owner": "net"}})
	if len(g.Edges) != 2 {
		t.Errorf("by tag: %v", g.Edges)
	}
	g = f.build(t, ResourceOptions{Tags: map[string]string{"env": "dev"}})
	if len(g.Edges) != 1 || g.Edges[0].Label != "azurerm_subnet" {
		t.Errorf("by tag value: %v", g.Edges)
	}
	g = f.build(t, ResourceOptions{Tags: map[string]string{"owner": "nobody"}})
	if len(g.Nodes) != 0 || g.Nodes == nil || g.Edges == nil {
		t.Errorf("nothing matches: %+v", g)
	}
}

func TestFocusOnAProject(t *testing.T) {
	f := hub()
	f.project(5, 4, rsDataURL, ".")
	f.managed(5, "azurerm_storage_account.s", "azurerm_storage_account", "s", map[string]string{"name": "stdata"}, nil)
	f.data(1, "data.azurerm_storage_account.s", "azurerm_storage_account", "s", map[string]string{"name": "stdata"})

	// Down: what dev depends on is hub, and through it the data repo.
	g := f.build(t, ResourceOptions{Focus: Focus{Project: 2, Direction: "down"}})
	nodes := nodeMap(g)
	for _, id := range []string{"project:2", "project:1", "project:5"} {
		if _, ok := nodes[id]; !ok {
			t.Errorf("down from dev is missing %s: %v", id, nodes)
		}
	}
	if _, ok := nodes["project:3"]; ok {
		t.Error("prod is a sibling, not something dev depends on")
	}
	// Depth.
	g = f.build(t, ResourceOptions{Focus: Focus{Project: 2, Direction: "down", Depth: 1}})
	if _, ok := nodeMap(g)["project:5"]; ok {
		t.Error("depth 1 stops at hub")
	}
	// Up: who depends on hub.
	g = f.build(t, ResourceOptions{Focus: Focus{Project: 1, Direction: "up"}})
	nodes = nodeMap(g)
	if _, ok := nodes["project:2"]; !ok || nodes["project:5"].ID != "" {
		t.Errorf("up from hub = %v", nodes)
	}
	// Default is both directions.
	g = f.build(t, ResourceOptions{Focus: Focus{Project: 1}})
	nodes = nodeMap(g)
	if _, ok := nodes["project:5"]; !ok {
		t.Errorf("both directions from hub = %v", nodes)
	}
	if _, ok := nodes["project:2"]; !ok {
		t.Errorf("both directions from hub = %v", nodes)
	}
}

func TestFocusOnARepoAndAtOtherLevels(t *testing.T) {
	f := hub()
	g := f.build(t, ResourceOptions{Focus: Focus{Repo: 2}})
	nodes := nodeMap(g)
	if _, ok := nodes["project:1"]; !ok || nodes["project:2"].ID == "" || nodes["project:3"].ID == "" {
		t.Errorf("repo focus = %v", nodes)
	}
	g = f.build(t, ResourceOptions{Level: "repo", Focus: Focus{Project: 2}})
	nodes = nodeMap(g)
	if _, ok := nodes["repo:2"]; !ok || nodes["repo:1"].ID == "" {
		t.Errorf("repo level, project focus = %v", nodes)
	}
	g = f.build(t, ResourceOptions{Level: "resource", Focus: Focus{Project: 3}})
	nodes = nodeMap(g)
	if _, ok := nodes["resource:3:data.azurerm_virtual_network.hub"]; !ok || nodes["resource:1:azurerm_virtual_network.hub"].ID == "" {
		t.Errorf("resource level = %v", nodes)
	}
	if _, ok := nodes["resource:2:data.azurerm_virtual_network.hub"]; ok {
		t.Error("dev's lookup isn't prod's dependency")
	}
	if nodes["project:3"].ID == "" || nodes["project:1"].ID == "" || nodes["repo:1"].ID == "" {
		t.Errorf("the groups around kept resources are kept: %v", nodes)
	}
}

func TestFocusErrors(t *testing.T) {
	f := hub()
	for name, o := range map[string]ResourceOptions{
		"no such project": {Focus: Focus{Project: 99}},
		"no such repo":    {Focus: Focus{Repo: 99}},
	} {
		if _, err := BuildResources(f.in, o); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, o := range map[string]ResourceOptions{
		"bad level":      {Level: "galaxy"},
		"bad direction":  {Focus: Focus{Project: 1, Direction: "sideways"}},
		"module":         {Focus: Focus{Module: 1}},
		"two foci":       {Focus: Focus{Project: 1, Repo: 1}},
		"negative depth": {Focus: Focus{Project: 1, Depth: -1}},
	} {
		if _, err := BuildResources(f.in, o); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestProjectsThatConnectToNothingAreOnlyShownWhenAsked(t *testing.T) {
	f := hub()
	f.project(9, 9, rsDataURL, "lonely")
	f.managed(9, "azurerm_resource_group.rg", "azurerm_resource_group", "rg", map[string]string{"name": "rg-lonely"}, nil)
	if _, ok := nodeMap(f.build(t, ResourceOptions{}))["project:9"]; ok {
		t.Error("a project nothing links to is noise by default")
	}
	if _, ok := nodeMap(f.build(t, ResourceOptions{Isolated: true}))["project:9"]; !ok {
		t.Error("it should show when asked")
	}
}

func TestProjectsThatUseEachOthersResourcesFormACycle(t *testing.T) {
	f := &fixture{}
	f.project(1, 1, rsNetURL, ".")
	f.project(2, 2, rsAppsURL, ".")
	f.managed(1, "azurerm_resource_group.a", "azurerm_resource_group", "a", map[string]string{"name": "rg-a"}, nil)
	f.managed(2, "azurerm_key_vault.b", "azurerm_key_vault", "b", map[string]string{"name": "kv-b"}, nil)
	f.data(1, "data.azurerm_key_vault.b", "azurerm_key_vault", "b", map[string]string{"name": "kv-b"})
	f.data(2, "data.azurerm_resource_group.a", "azurerm_resource_group", "a", map[string]string{"name": "rg-a"})
	g := f.build(t, ResourceOptions{})
	if !edgeOf(t, g, "project:1", "project:2").Cycle || !edgeOf(t, g, "project:2", "project:1").Cycle || g.Stats.Cycles != 2 {
		t.Errorf("edges = %+v, stats = %+v", g.Edges, g.Stats)
	}
}

func TestResourceGraphIsDeterministicAndNeverNull(t *testing.T) {
	a, b := hub().build(t, ResourceOptions{}), hub().build(t, ResourceOptions{})
	if len(a.Nodes) != len(b.Nodes) {
		t.Fatal("different sizes")
	}
	for i := range a.Nodes {
		if a.Nodes[i].ID != b.Nodes[i].ID {
			t.Errorf("order differs at %d", i)
		}
	}
	empty, err := BuildResources(ResourceInput{}, ResourceOptions{})
	if err != nil || empty.Nodes == nil || empty.Edges == nil {
		t.Errorf("empty = %+v, %v", empty, err)
	}
}

func TestALargeResourceGraphIsTrimmed(t *testing.T) {
	f := &fixture{}
	f.project(1, 1, rsNetURL, ".")
	f.managed(1, "azurerm_resource_group.rg", "azurerm_resource_group", "rg", map[string]string{"name": "rg-shared"}, nil)
	for i := int64(2); i < 12; i++ {
		f.project(i, i, rsAppsURL+strings.Repeat("x", int(i)), ".")
		f.data(i, "data.azurerm_resource_group.rg", "azurerm_resource_group", "rg", map[string]string{"name": "rg-shared"})
	}
	g := f.build(t, ResourceOptions{MaxNodes: 4})
	if !g.Truncated || g.HiddenNodes == 0 {
		t.Errorf("truncated = %v, hidden = %d", g.Truncated, g.HiddenNodes)
	}
}
