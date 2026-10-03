package graph

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const (
	appsURL = "git@github.com:org/apps.git"
	addons  = "git::https://github.com/org/addons.git?ref=v2.0.0"
)

// Two projects call the same addons module. Initialised, each shows a different module inside it.
// The addons repo was scanned itself and declares a dependency on the logging module.
func shared() Input {
	const addonsURL = "git@github.com:org/addons.git"
	return Input{
		Repos: []store.Repo{repoOf(1, infraURL, 0), repoOf(2, appsURL, 0), repoOf(3, addonsURL, 10)},
		Projects: []store.Project{
			projectOf(1, 1, infraURL, "."),
			projectOf(2, 2, appsURL, "."),
			projectOf(3, 3, addonsURL, "."),
		},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/addons", "git", 3),
			moduleOf(11, "github.com/org/tf-vpc", "git", 0),
			moduleOf(12, "github.com/org/queues", "git", 0),
			moduleOf(13, "github.com/org/dns", "git", 0),
			moduleOf(14, "github.com/org/logging", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "addons", 10, addons, "2.0.0"),
			call(1, "addons", "network", 11, vpcSrc, "5.1.0"),
			call(2, "", "addons", 10, addons, "2.0.0"),
			call(2, "addons", "queue", 12, "git::https://github.com/org/queues.git", "1.0.0"),
			call(2, "", "dns", 13, "git::https://github.com/org/dns.git", "1.0.0"),
			call(3, "", "log", 14, "git::https://github.com/org/logging.git", "1.0.0"),
		},
	}
}

func ids(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		out = append(out, n.ID)
	}
	return out
}

func TestFocusOnAProjectShowsWhatItDependsOn(t *testing.T) {
	g := build(t, shared(), Options{Focus: Focus{Project: 1}})

	want := []string{"module:10", "module:11", "module:14", "project:1", "repo:1"}
	if !slices.Equal(ids(g), want) {
		t.Errorf("nodes = %v, want %v", ids(g), want)
	}
	mustEdge(t, g, "project:1", "module:10")
	mustEdge(t, g, "module:10", "module:11")
	// The addons module's own declared dependency is true for everyone, so it comes along.
	mustEdge(t, g, "module:10", "module:14")
	if _, ok := edge(g, "module:10", "module:12"); ok {
		t.Error("project 2's nested module must not appear when looking at project 1")
	}
}

func TestFocusKeepsOtherProjectsNestedCallsOut(t *testing.T) {
	g := build(t, shared(), Options{Focus: Focus{Project: 2}})
	mustEdge(t, g, "module:10", "module:12")
	mustNode(t, g, "module:13")
	if _, ok := edge(g, "module:10", "module:11"); ok {
		t.Error("project 1's nested module must not appear when looking at project 2")
	}
	if _, ok := node(g, "project:1"); ok {
		t.Error("other projects aren't part of a project's dependencies")
	}
}

func TestFocusOnAModuleShowsConsumersAndDependencies(t *testing.T) {
	g := build(t, shared(), Options{Focus: Focus{Module: 10}})

	for _, id := range []string{"project:1", "project:2", "module:10", "module:11", "module:12", "module:14"} {
		mustNode(t, g, id)
	}
	if _, ok := node(g, "module:13"); ok {
		t.Error("dns is unrelated to the addons module")
	}
	mustEdge(t, g, "project:1", "module:10")
	mustEdge(t, g, "project:2", "module:10")
}

func TestFocusDirection(t *testing.T) {
	up := build(t, shared(), Options{Focus: Focus{Module: 11, Direction: "up"}})
	if want := []string{"module:10", "module:11", "project:1", "repo:1"}; !slices.Equal(ids(up), want) {
		t.Errorf("up = %v, want %v", ids(up), want)
	}

	down := build(t, shared(), Options{Focus: Focus{Module: 10, Direction: "down"}})
	if _, ok := node(down, "project:1"); ok {
		t.Error("looking down from a module shouldn't include its consumers")
	}
	mustNode(t, down, "module:14")

	alone := build(t, shared(), Options{Focus: Focus{Project: 1, Direction: "up"}})
	if !slices.Equal(ids(alone), []string{"project:1", "repo:1"}) {
		t.Errorf("nothing depends on a project, got %v", ids(alone))
	}
}

func TestFocusDepth(t *testing.T) {
	one := build(t, shared(), Options{Focus: Focus{Module: 11, Direction: "up", Depth: 1}})
	if want := []string{"module:10", "module:11"}; !slices.Equal(ids(one), want) {
		t.Errorf("depth 1 = %v, want %v", ids(one), want)
	}
	two := build(t, shared(), Options{Focus: Focus{Module: 11, Direction: "up", Depth: 2}})
	mustNode(t, two, "project:1")

	shallow := build(t, shared(), Options{Focus: Focus{Project: 1, Depth: 1}})
	if _, ok := node(shallow, "module:11"); ok {
		t.Error("depth 1 from a project stops at the modules it calls")
	}
	mustNode(t, shallow, "module:10")
}

func TestFocusOnARepoCoversAllItsProjects(t *testing.T) {
	in := twoEnvironments()
	in.Modules = append(in.Modules, moduleOf(11, "github.com/org/other", "git", 0))
	in.Usages = append(in.Usages, call(2, "", "o", 11, "git::https://github.com/org/other.git", "1.0.0"))
	g := build(t, in, Options{Focus: Focus{Repo: 1}})

	for _, id := range []string{"project:1", "project:2", "module:10", "module:11"} {
		mustNode(t, g, id)
	}
}

func TestFocusOnAModulesRepoFocusesTheModule(t *testing.T) {
	g := build(t, shared(), Options{Focus: Focus{Repo: 3}})
	mustNode(t, g, "module:10")
	mustEdge(t, g, "module:10", "module:14")
}

func TestFocusOnAProjectInsideAModuleFocusesTheModule(t *testing.T) {
	g := build(t, shared(), Options{Focus: Focus{Project: 3}})
	mustNode(t, g, "module:10")
	mustNode(t, g, "module:14")
}

func TestFocusOnUnknownThingsIsNotFound(t *testing.T) {
	for _, f := range []Focus{{Project: 99}, {Repo: 99}, {Module: 99}} {
		if _, err := Build(shared(), Options{Focus: f}); !errors.Is(err, ErrNotFound) {
			t.Errorf("Build(focus %+v) = %v, want ErrNotFound", f, err)
		}
	}
}

func TestFocusOnAMonorepoModuleCoversEverySubdirectory(t *testing.T) {
	in := Input{
		Repos:    []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{projectOf(1, 1, infraURL, ".")},
		Modules:  []store.Module{moduleOf(10, "github.com/org/mods", "git", 0)},
		Usages: []store.Usage{
			call(1, "", "a", 10, "git::https://github.com/org/mods.git//vpc?ref=v1", "1.0.0"),
			call(1, "", "b", 10, "git::https://github.com/org/mods.git//eks?ref=v1", "1.0.0"),
		},
	}
	g := build(t, in, Options{Focus: Focus{Module: 10}})
	mustNode(t, g, "module:10/vpc")
	mustNode(t, g, "module:10/eks")
	mustNode(t, g, "module_repo:10")
}

func TestFocusedGraphsDropGroupsWithNoMembers(t *testing.T) {
	g := build(t, shared(), Options{Focus: Focus{Project: 2}})
	if _, ok := node(g, "repo:1"); ok {
		t.Error("repo:1 has no project in this view, so its group shouldn't be drawn")
	}
	mustNode(t, g, "repo:2")
}

func TestCollapsingToReposMergesProjectsIntoOneNode(t *testing.T) {
	in := twoEnvironments()
	in.Projects[0].ModuleCalls, in.Projects[1].ModuleCalls = 1, 1
	in.Projects[1].LastBranch = "release"
	in.Usages = []store.Usage{
		call(1, "", "vpc", 10, vpcSrc, "5.1.0"),
		behind(call(2, "", "vpc", 10, "git::https://github.com/org/tf-vpc.git?ref=v3.0.0", "3.0.0"), 2),
	}
	g := build(t, in, Options{Level: "repo"})

	if g.Level != "repo" {
		t.Errorf("level = %q", g.Level)
	}
	repo := mustNode(t, g, "repo:1")
	if repo.Type != TypeRepo || repo.Projects != 2 || repo.ModuleCalls != 2 || repo.Branch != "" || repo.Group != "" {
		t.Errorf("repo = %+v", repo)
	}
	for _, id := range []string{"project:1", "project:2"} {
		if _, ok := node(g, id); ok {
			t.Errorf("%s should be folded into its repo", id)
		}
	}
	e := mustEdge(t, g, "repo:1", "module:10")
	if e.Calls != 2 || e.Status != StatusMajorBehind || !slices.Equal(e.Versions, []string{"5.1.0", "3.0.0"}) || !slices.Equal(e.Projects, []int64{1, 2}) {
		t.Errorf("edge = %+v", e)
	}
	if len(g.Edges) != 1 {
		t.Errorf("edges = %v", edgeNames(g))
	}
}

func TestCollapsingKeepsTheBranchWhenAllProjectsAgree(t *testing.T) {
	g := build(t, twoEnvironments(), Options{Level: "repo"})
	if b := mustNode(t, g, "repo:1").Branch; b != "main" {
		t.Errorf("branch = %q", b)
	}
}

func TestCollapsingAFocusedGraph(t *testing.T) {
	g := build(t, shared(), Options{Level: "repo", Focus: Focus{Project: 1}})
	mustEdge(t, g, "repo:1", "module:10")
	mustEdge(t, g, "module:10", "module:11")
	if _, ok := node(g, "repo:2"); ok {
		t.Error("repo:2 isn't part of project 1's dependencies")
	}
}

// many builds n repos, each with one project calling the same module.
func many(n int) Input {
	in := Input{Modules: []store.Module{moduleOf(1000, "github.com/org/shared", "git", 0)}}
	for i := 1; i <= n; i++ {
		url := fmt.Sprintf("git@github.com:org/app%02d.git", i)
		in.Repos = append(in.Repos, repoOf(int64(i), url, 0))
		in.Projects = append(in.Projects, projectOf(int64(i), int64(i), url, "."))
		in.Usages = append(in.Usages, call(int64(i), "", "shared", 1000, "git::https://github.com/org/shared.git", "1.0.0"))
	}
	return in
}

func TestALargeGraphFallsBackToRepoLevel(t *testing.T) {
	in := many(6)
	// Each repo holds a second project, so repo level has half as many nodes.
	for i := 1; i <= 6; i++ {
		url := fmt.Sprintf("git@github.com:org/app%02d.git", i)
		in.Projects = append(in.Projects, projectOf(int64(100+i), int64(i), url, "second"))
	}
	g := build(t, in, Options{MaxNodes: 8})

	if g.Level != "repo" || g.Truncated {
		t.Errorf("level = %q, truncated = %v; want repo level with nothing left out", g.Level, g.Truncated)
	}
	if g.Stats.Repos != 6 || g.Stats.Projects != 0 {
		t.Errorf("stats = %+v", g.Stats)
	}
}

func TestAGraphStillTooLargeKeepsTheBestConnectedNodes(t *testing.T) {
	g := build(t, many(8), Options{MaxNodes: 4})

	if !g.Truncated || g.HiddenNodes != 5 {
		t.Errorf("truncated = %v, hidden = %d; want 5 left out of 9", g.Truncated, g.HiddenNodes)
	}
	mustNode(t, g, "module:1000")
	for _, e := range g.Edges {
		if _, ok := node(g, e.From); !ok {
			t.Errorf("edge %s -> %s points at a node that was left out", e.From, e.To)
		}
		if _, ok := node(g, e.To); !ok {
			t.Errorf("edge %s -> %s points at a node that was left out", e.From, e.To)
		}
	}
	if len(g.Edges) != 3 {
		t.Errorf("edges = %d, want the 3 that join the kept nodes", len(g.Edges))
	}
}

func TestFocusIsNeverTruncated(t *testing.T) {
	g := build(t, many(8), Options{MaxNodes: 2, Focus: Focus{Module: 1000}})
	if g.Truncated || len(g.Nodes) < 9 {
		t.Errorf("a focused graph shows everything it connects to: %d nodes, truncated %v", len(g.Nodes), g.Truncated)
	}
}

func TestTruncationFlagsWorkRightWhenNothingIsLeftOut(t *testing.T) {
	g := build(t, many(3), Options{MaxNodes: 100})
	if g.Truncated || g.HiddenNodes != 0 || g.Level != "project" {
		t.Errorf("graph = level %q, truncated %v, hidden %d", g.Level, g.Truncated, g.HiddenNodes)
	}
}
