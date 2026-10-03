package graph

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func ptr[T any](v T) *T { return &v }

func repoOf(id int64, url string, module int64) store.Repo {
	r := store.Repo{ID: id, RepoURL: url}
	if module != 0 {
		r.ModuleID = ptr(module)
	}
	return r
}

func projectOf(id, repo int64, url, path string) store.Project {
	return store.Project{ID: id, RepoID: repo, RepoURL: url, Path: path, LastBranch: "main"}
}

func moduleOf(id int64, key, kind string, repo int64) store.Module {
	m := store.Module{ID: id, Key: key, Kind: kind, LatestVersion: ptr("9.0.0")}
	if repo != 0 {
		m.RepoID = ptr(repo)
	}
	return m
}

// call is a module call. module 0 makes it a local module. A pinned version makes it current.
func call(project int64, parent, name string, module int64, src, pinned string) store.Usage {
	u := store.Usage{ProjectID: project, Parent: parent, CallName: name, Source: src}
	if module != 0 {
		u.ModuleID = ptr(module)
	}
	if pinned != "" {
		u.PinnedVersion = ptr(pinned)
		u.LatestVersion = ptr(pinned)
	}
	return u
}

func outdated(u store.Usage) store.Usage {
	u.Outdated = true
	u.LatestVersion = ptr("9.0.0")
	u.MajorsBehind = ptr(0)
	return u
}

func behind(u store.Usage, majors int) store.Usage {
	u = outdated(u)
	u.MajorsBehind = ptr(majors)
	return u
}

func build(t *testing.T, in Input, opts Options) Graph {
	t.Helper()
	g, err := Build(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func node(g Graph, id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func mustNode(t *testing.T, g Graph, id string) Node {
	t.Helper()
	n, ok := node(g, id)
	if !ok {
		t.Fatalf("no node %q in %v", id, nodeIDs(g))
	}
	return n
}

func edge(g Graph, from, to string) (Edge, bool) {
	for _, e := range g.Edges {
		if e.From == from && e.To == to {
			return e, true
		}
	}
	return Edge{}, false
}

func mustEdge(t *testing.T, g Graph, from, to string) Edge {
	t.Helper()
	e, ok := edge(g, from, to)
	if !ok {
		t.Fatalf("no edge %s -> %s in %v", from, to, edgeNames(g))
	}
	return e
}

func nodeIDs(g Graph) []string {
	var ids []string
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func edgeNames(g Graph) []string {
	var names []string
	for _, e := range g.Edges {
		names = append(names, e.From+" -> "+e.To)
	}
	return names
}

const (
	infraURL = "git@github.com:org/infra.git"
	vpcSrc   = "git::https://github.com/org/tf-vpc.git?ref=v5.1.0"
)

// infra has two environments that both call the vpc module.
func twoEnvironments() Input {
	return Input{
		Repos: []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{
			projectOf(1, 1, infraURL, "envs/dev"),
			projectOf(2, 1, infraURL, "envs/prod"),
		},
		Modules: []store.Module{moduleOf(10, "github.com/org/tf-vpc", "git", 0)},
		Usages: []store.Usage{
			call(1, "", "vpc", 10, vpcSrc, "5.1.0"),
			call(2, "", "vpc", 10, vpcSrc, "5.1.0"),
		},
	}
}

func TestRepoWithSeveralProjectsIsOneGroup(t *testing.T) {
	g := build(t, twoEnvironments(), Options{})

	repo := mustNode(t, g, "repo:1")
	if repo.Type != TypeRepo || repo.Label != "infra" || repo.Projects != 2 {
		t.Errorf("repo group = %+v", repo)
	}
	for _, id := range []string{"project:1", "project:2"} {
		if n := mustNode(t, g, id); n.Group != "repo:1" || n.Type != TypeProject {
			t.Errorf("%s = %+v, want a project in repo:1", id, n)
		}
	}
	if got := mustNode(t, g, "project:1").Label; got != "infra/envs/dev" {
		t.Errorf("label = %q", got)
	}
	for _, from := range []string{"project:1", "project:2"} {
		e := mustEdge(t, g, from, "module:10")
		if e.Calls != 1 || !slices.Equal(e.Origins, []Origin{OriginDirect}) || e.Status != StatusCurrent {
			t.Errorf("edge from %s = %+v", from, e)
		}
	}
	if len(g.Edges) != 2 || g.Level != "project" {
		t.Errorf("edges = %v, level %q", edgeNames(g), g.Level)
	}
}

func TestOneModuleCalledSeveralTimesIsOneEdge(t *testing.T) {
	in := twoEnvironments()
	in.Usages = []store.Usage{
		call(1, "", "vpc_a", 10, vpcSrc, "5.1.0"),
		behind(call(1, "", "vpc_b", 10, "git::https://github.com/org/tf-vpc.git?ref=v3.0.0", "3.0.0"), 2),
	}
	g := build(t, in, Options{})

	e := mustEdge(t, g, "project:1", "module:10")
	if e.Calls != 2 {
		t.Errorf("calls = %d, want 2", e.Calls)
	}
	if !slices.Equal(e.Versions, []string{"5.1.0", "3.0.0"}) {
		t.Errorf("versions = %v, want newest first", e.Versions)
	}
	if e.Status != StatusMajorBehind {
		t.Errorf("status = %q, want the worst of its calls", e.Status)
	}
	if len(g.Edges) != 1 {
		t.Errorf("edges = %v", edgeNames(g))
	}
}

func TestModuleMonorepoHasOneNodePerSubdirectory(t *testing.T) {
	in := Input{
		Repos:    []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{projectOf(1, 1, infraURL, ".")},
		Modules:  []store.Module{moduleOf(10, "github.com/org/mods", "git", 0)},
		Usages: []store.Usage{
			call(1, "", "network", 10, "git::https://github.com/org/mods.git//modules/vpc?ref=v1.0.0", "1.0.0"),
			call(1, "", "cluster", 10, "git::https://github.com/org/mods.git//modules/eks?ref=v1.0.0", "1.0.0"),
			call(1, "", "bucket", 10, "git::https://github.com/org/mods.git//modules/vpc?ref=v1.0.0", "1.0.0"),
		},
	}
	g := build(t, in, Options{})

	vpc := mustNode(t, g, "module:10/modules/vpc")
	eks := mustNode(t, g, "module:10/modules/eks")
	if vpc.Label != "mods//modules/vpc" || vpc.Subdir != "modules/vpc" || eks.Label != "mods//modules/eks" {
		t.Errorf("labels: %q %q", vpc.Label, eks.Label)
	}
	if vpc.Group != "module_repo:10" || eks.Group != "module_repo:10" {
		t.Errorf("groups: %q %q", vpc.Group, eks.Group)
	}
	if grp := mustNode(t, g, "module_repo:10"); grp.Type != TypeModuleRepo || grp.Label != "mods" {
		t.Errorf("group = %+v", grp)
	}
	if e := mustEdge(t, g, "project:1", "module:10/modules/vpc"); e.Calls != 2 {
		t.Errorf("vpc calls = %d, want the two calls to the same subdirectory merged", e.Calls)
	}
	mustEdge(t, g, "project:1", "module:10/modules/eks")
	if _, ok := node(g, "module:10"); ok {
		t.Error("the repo root isn't called, so it shouldn't be a node")
	}
	if vpc.Consumers != 1 {
		t.Errorf("consumers = %d", vpc.Consumers)
	}
}

func TestOneSubdirectoryNeedsNoGroup(t *testing.T) {
	in := twoEnvironments()
	in.Usages = []store.Usage{call(1, "", "n", 10, "git::https://github.com/org/tf-vpc.git//modules/vpc?ref=v1", "1.0.0")}
	g := build(t, in, Options{})
	if n := mustNode(t, g, "module:10/modules/vpc"); n.Group != "" {
		t.Errorf("group = %q, a single module in a repo is shown on its own", n.Group)
	}
	if _, ok := node(g, "module_repo:10"); ok {
		t.Error("no group expected")
	}
}

func TestVersionDriftAcrossProjects(t *testing.T) {
	in := twoEnvironments()
	in.Usages = []store.Usage{
		call(1, "", "vpc", 10, vpcSrc, "5.1.0"),
		behind(call(2, "", "vpc", 10, "git::https://github.com/org/tf-vpc.git?ref=v3.2.0", "3.2.0"), 2),
	}
	g := build(t, in, Options{})

	m := mustNode(t, g, "module:10")
	if !m.Drift || m.Consumers != 2 {
		t.Errorf("drift = %v, consumers = %d", m.Drift, m.Consumers)
	}
	want := []VersionUse{
		{Version: "5.1.0", Projects: 1, Status: StatusCurrent},
		{Version: "3.2.0", Projects: 1, Status: StatusMajorBehind},
	}
	if !slices.Equal(m.Versions, want) {
		t.Errorf("versions = %+v, want %+v", m.Versions, want)
	}
	if mustEdge(t, g, "project:1", "module:10").Status != StatusCurrent || mustEdge(t, g, "project:2", "module:10").Status != StatusMajorBehind {
		t.Error("each edge should carry its own status")
	}
}

func TestSameVersionEverywhereIsNotDrift(t *testing.T) {
	g := build(t, twoEnvironments(), Options{})
	m := mustNode(t, g, "module:10")
	if m.Drift || len(m.Versions) != 1 || m.Versions[0].Projects != 2 {
		t.Errorf("module = %+v", m)
	}
}

func TestUnpinnedAndUnknownVersions(t *testing.T) {
	in := twoEnvironments()
	in.Modules = []store.Module{{ID: 10, Key: "registry.terraform.io/acme/vpc/aws", Kind: "registry"}}
	in.Usages = []store.Usage{call(1, "", "vpc", 10, "acme/vpc/aws", "")}
	g := build(t, in, Options{})

	e := mustEdge(t, g, "project:1", "module:10")
	if e.Status != StatusUnknown || !slices.Equal(e.Versions, []string{"unpinned"}) {
		t.Errorf("edge = %+v", e)
	}
	m := mustNode(t, g, "module:10")
	if !m.External || m.Label != "vpc" || m.Drift {
		t.Errorf("module = %+v", m)
	}
}

func TestNestedCallsBecomeEdgesBetweenModules(t *testing.T) {
	in := Input{
		Repos:    []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{projectOf(1, 1, infraURL, ".")},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/addons", "git", 0),
			moduleOf(11, "github.com/org/tf-vpc", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "addons", 10, "git::https://github.com/org/addons.git?ref=v2.0.0", "2.0.0"),
			outdated(call(1, "addons", "network", 11, vpcSrc, "5.1.0")),
		},
	}
	g := build(t, in, Options{})

	direct := mustEdge(t, g, "project:1", "module:10")
	if !slices.Equal(direct.Origins, []Origin{OriginDirect}) {
		t.Errorf("direct = %+v", direct)
	}
	nested := mustEdge(t, g, "module:10", "module:11")
	if !slices.Equal(nested.Origins, []Origin{OriginNested}) || nested.Status != StatusOutdated || !slices.Equal(nested.Projects, []int64{1}) {
		t.Errorf("nested = %+v", nested)
	}
	if _, ok := edge(g, "project:1", "module:11"); ok {
		t.Error("the project doesn't call the nested module itself")
	}
	if mustNode(t, g, "module:11").Consumers != 1 {
		t.Error("a nested call still makes the project a consumer")
	}
}

func TestDeeplyNestedCallsFollowTheChain(t *testing.T) {
	in := Input{
		Repos:    []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{projectOf(1, 1, infraURL, ".")},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/a", "git", 0),
			moduleOf(11, "github.com/org/b", "git", 0),
			moduleOf(12, "github.com/org/c", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "a", 10, "git::https://github.com/org/a.git", "1.0.0"),
			call(1, "a", "b", 11, "git::https://github.com/org/b.git", "1.0.0"),
			call(1, "a.b", "c", 12, "git::https://github.com/org/c.git", "1.0.0"),
		},
	}
	g := build(t, in, Options{})
	mustEdge(t, g, "project:1", "module:10")
	mustEdge(t, g, "module:10", "module:11")
	mustEdge(t, g, "module:11", "module:12")
	if len(g.Edges) != 3 {
		t.Errorf("edges = %v", edgeNames(g))
	}
}

func nestedThroughLocal() Input {
	return Input{
		Repos:    []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{projectOf(1, 1, infraURL, ".")},
		Modules:  []store.Module{moduleOf(11, "github.com/org/subnets", "git", 0)},
		Usages: []store.Usage{
			call(1, "", "helpers", 0, "./modules/helpers", ""),
			call(1, "helpers", "subnets", 11, "git::https://github.com/org/subnets.git", "1.0.0"),
		},
	}
}

func TestLocalModulesAreFoldedIntoTheirCaller(t *testing.T) {
	g := build(t, nestedThroughLocal(), Options{})

	e := mustEdge(t, g, "project:1", "module:11")
	if !slices.Equal(e.Via, []string{"./modules/helpers"}) || !slices.Equal(e.Origins, []Origin{OriginDirect}) {
		t.Errorf("edge = %+v", e)
	}
	if n := mustNode(t, g, "project:1"); n.LocalCalls != 1 {
		t.Errorf("local calls = %d", n.LocalCalls)
	}
	for _, n := range g.Nodes {
		if n.Type == TypeLocal {
			t.Errorf("unexpected local node %q", n.ID)
		}
	}
}

func TestLocalModulesCanBeShownAsNodes(t *testing.T) {
	g := build(t, nestedThroughLocal(), Options{Locals: true})

	local := mustNode(t, g, "local:1:helpers")
	if local.Type != TypeLocal || local.Source != "./modules/helpers" {
		t.Errorf("local = %+v", local)
	}
	mustEdge(t, g, "project:1", "local:1:helpers")
	e := mustEdge(t, g, "local:1:helpers", "module:11")
	if len(e.Via) != 0 {
		t.Errorf("via = %v, nothing is folded when locals are shown", e.Via)
	}
	if _, ok := edge(g, "project:1", "module:11"); ok {
		t.Error("the call goes through the local module")
	}
}

func TestLocalModuleInsideASharedModule(t *testing.T) {
	in := Input{
		Repos:    []store.Repo{repoOf(1, infraURL, 0)},
		Projects: []store.Project{projectOf(1, 1, infraURL, ".")},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/vpc", "git", 0),
			moduleOf(11, "github.com/org/flowlogs", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "vpc", 10, "git::https://github.com/org/vpc.git", "1.0.0"),
			call(1, "vpc", "internal", 0, "./modules/internal", ""),
			call(1, "vpc.internal", "logs", 11, "git::https://github.com/org/flowlogs.git", "1.0.0"),
		},
	}
	g := build(t, in, Options{})

	e := mustEdge(t, g, "module:10", "module:11")
	if !slices.Equal(e.Origins, []Origin{OriginNested}) || !slices.Equal(e.Via, []string{"./modules/internal"}) {
		t.Errorf("edge = %+v", e)
	}
	if mustNode(t, g, "project:1").LocalCalls != 1 {
		t.Error("the local module is still counted")
	}
}

func TestOrphanedNestedCallAttachesToItsProject(t *testing.T) {
	in := twoEnvironments()
	in.Usages = []store.Usage{call(1, "ghost", "vpc", 10, vpcSrc, "5.1.0")}
	g := build(t, in, Options{})

	mustEdge(t, g, "project:1", "module:10")
	if g.Stats.Orphans != 1 {
		t.Errorf("orphans = %d, want 1", g.Stats.Orphans)
	}
}

func TestProjectsWithoutCallsAndUnusedModulesStillAppear(t *testing.T) {
	in := twoEnvironments()
	in.Usages = nil
	in.Modules = append(in.Modules, moduleOf(11, "github.com/org/unused", "git", 0))
	g := build(t, in, Options{})

	for _, id := range []string{"project:1", "project:2", "module:10", "module:11"} {
		mustNode(t, g, id)
	}
	if len(g.Edges) != 0 {
		t.Errorf("edges = %v", edgeNames(g))
	}
	if m := mustNode(t, g, "module:11"); !m.Unused || m.Consumers != 0 {
		t.Errorf("module = %+v", m)
	}
}

// moduleRepo is a module that was scanned itself, so what it is built on is known.
func moduleRepo() Input {
	const modURL = "git@github.com:org/tf-vpc.git"
	return Input{
		Repos: []store.Repo{repoOf(1, infraURL, 0), repoOf(2, modURL, 10)},
		Projects: []store.Project{
			projectOf(1, 1, infraURL, "."),
			projectOf(2, 2, modURL, "."),
		},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/tf-vpc", "git", 2),
			moduleOf(11, "github.com/org/tf-flowlogs", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "vpc", 10, vpcSrc, "5.1.0"),
			call(2, "", "logs", 11, "git::https://github.com/org/tf-flowlogs.git?ref=v1.0.0", "1.0.0"),
		},
	}
}

func TestModuleRepoDeclaresItsOwnDependencies(t *testing.T) {
	g := build(t, moduleRepo(), Options{})

	e := mustEdge(t, g, "module:10", "module:11")
	if !slices.Equal(e.Origins, []Origin{OriginDeclared}) || !slices.Equal(e.Projects, []int64{2}) {
		t.Errorf("edge = %+v", e)
	}
	if _, ok := node(g, "project:2"); ok {
		t.Error("a module's own root is the module, not a separate project")
	}
	if _, ok := node(g, "repo:2"); ok {
		t.Error("a module repo is not a deployable repo")
	}
	if m := mustNode(t, g, "module:10"); m.External || m.RepoHref != "/repos/2" {
		t.Errorf("module = %+v", m)
	}
}

func TestDeclaredAndNestedKnowledgeMergeIntoOneEdge(t *testing.T) {
	in := moduleRepo()
	in.Usages = append(in.Usages, call(1, "vpc", "logs", 11, "git::https://github.com/org/tf-flowlogs.git?ref=v1.0.0", "1.0.0"))
	g := build(t, in, Options{})

	e := mustEdge(t, g, "module:10", "module:11")
	if !slices.Equal(e.Origins, []Origin{OriginDeclared, OriginNested}) || e.Calls != 2 {
		t.Errorf("edge = %+v", e)
	}
	if len(g.Edges) != 2 {
		t.Errorf("edges = %v", edgeNames(g))
	}
}

func TestModuleRepoThatAlsoHoldsADeployableRoot(t *testing.T) {
	const modURL = "git@github.com:org/tf-vpc.git"
	in := moduleRepo()
	in.Projects = append(in.Projects, projectOf(3, 2, modURL, "envs/prod"))
	in.Usages = append(in.Usages, call(3, "", "vpc", 10, "../../", ""))
	in.Usages[len(in.Usages)-1].ModuleID = nil
	in.Usages = append(in.Usages, call(3, "", "logs", 11, "git::https://github.com/org/tf-flowlogs.git?ref=v1.0.0", "1.0.0"))
	g := build(t, in, Options{})

	if n := mustNode(t, g, "project:3"); n.Type != TypeProject || n.Group != "repo:2" {
		t.Errorf("envs/prod = %+v, want a deployable project in its own repo group", n)
	}
	e := mustEdge(t, g, "project:3", "module:11")
	if !slices.Equal(e.Origins, []Origin{OriginDirect}) {
		t.Errorf("edge = %+v", e)
	}
	mustEdge(t, g, "module:10", "module:11")
}

func TestModuleMonorepoRootsBecomeTheirSubdirectoryModules(t *testing.T) {
	const modURL = "git@github.com:org/mods.git"
	in := Input{
		Repos: []store.Repo{repoOf(1, infraURL, 0), repoOf(2, modURL, 10)},
		Projects: []store.Project{
			projectOf(1, 1, infraURL, "."),
			projectOf(2, 2, modURL, "modules/vpc"),
			projectOf(3, 2, modURL, "modules/eks"),
		},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/mods", "git", 2),
			moduleOf(11, "github.com/org/base", "git", 0),
			moduleOf(12, "github.com/org/iam", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "net", 10, "git::https://github.com/org/mods.git//modules/vpc?ref=v1", "1.0.0"),
			call(1, "", "k8s", 10, "git::https://github.com/org/mods.git//modules/eks?ref=v1", "1.0.0"),
			call(2, "", "base", 11, "git::https://github.com/org/base.git", "1.0.0"),
			call(3, "", "iam", 12, "git::https://github.com/org/iam.git", "1.0.0"),
		},
	}
	g := build(t, in, Options{})

	mustEdge(t, g, "module:10/modules/vpc", "module:11")
	mustEdge(t, g, "module:10/modules/eks", "module:12")
	if _, ok := edge(g, "module:10/modules/vpc", "module:12"); ok {
		t.Error("each subdirectory has only its own dependencies")
	}
	for _, id := range []string{"project:2", "project:3"} {
		if _, ok := node(g, id); ok {
			t.Errorf("%s is part of a module, not a project", id)
		}
	}
}

func TestCyclesAreFlaggedAndTerminate(t *testing.T) {
	const aURL, bURL = "git@github.com:org/a.git", "git@github.com:org/b.git"
	in := Input{
		Repos:    []store.Repo{repoOf(1, aURL, 10), repoOf(2, bURL, 11)},
		Projects: []store.Project{projectOf(1, 1, aURL, "."), projectOf(2, 2, bURL, ".")},
		Modules: []store.Module{
			moduleOf(10, "github.com/org/a", "git", 1),
			moduleOf(11, "github.com/org/b", "git", 2),
			moduleOf(12, "github.com/org/c", "git", 0),
		},
		Usages: []store.Usage{
			call(1, "", "b", 11, "git::https://github.com/org/b.git", "1.0.0"),
			call(2, "", "a", 10, "git::https://github.com/org/a.git", "1.0.0"),
			call(2, "", "c", 12, "git::https://github.com/org/c.git", "1.0.0"),
		},
	}
	g := build(t, in, Options{})

	ab, ba := mustEdge(t, g, "module:10", "module:11"), mustEdge(t, g, "module:11", "module:10")
	if !ab.Cycle || !ba.Cycle {
		t.Errorf("the two modules use each other: %+v %+v", ab, ba)
	}
	if mustEdge(t, g, "module:11", "module:12").Cycle {
		t.Error("an edge leaving the cycle isn't part of it")
	}
	if !mustNode(t, g, "module:10").Cycle || mustNode(t, g, "module:12").Cycle {
		t.Error("only the modules on the cycle are flagged")
	}
	if g.Stats.Cycles != 2 {
		t.Errorf("cycles = %d", g.Stats.Cycles)
	}
}

func TestAModuleThatUsesItselfIsACycle(t *testing.T) {
	const aURL = "git@github.com:org/a.git"
	in := Input{
		Repos:    []store.Repo{repoOf(1, aURL, 10)},
		Projects: []store.Project{projectOf(1, 1, aURL, ".")},
		Modules:  []store.Module{moduleOf(10, "github.com/org/a", "git", 1)},
		Usages:   []store.Usage{call(1, "", "me", 10, "git::https://github.com/org/a.git", "1.0.0")},
	}
	g := build(t, in, Options{})
	if e := mustEdge(t, g, "module:10", "module:10"); !e.Cycle {
		t.Errorf("edge = %+v", e)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	in := moduleRepo()
	in.Usages = append(in.Usages, call(1, "vpc", "logs", 11, "git::https://github.com/org/tf-flowlogs.git", "1.0.0"))
	first, _ := json.Marshal(build(t, in, Options{}))
	for i := 0; i < 20; i++ {
		again, _ := json.Marshal(build(t, in, Options{}))
		if string(again) != string(first) {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, again)
		}
	}
}

func TestStatsAndEmptyInput(t *testing.T) {
	g := build(t, Input{}, Options{})
	if g.Nodes == nil || g.Edges == nil || len(g.Nodes) != 0 {
		t.Errorf("an empty graph should have empty lists, not null: %+v", g)
	}
	s := build(t, twoEnvironments(), Options{}).Stats
	if s.Repos != 1 || s.Projects != 2 || s.Modules != 1 || s.Edges != 2 {
		t.Errorf("stats = %+v", s)
	}
}

func TestOptionsAreValidated(t *testing.T) {
	bad := []Options{
		{Level: "galaxy"},
		{Focus: Focus{Project: 1, Repo: 1}},
		{Focus: Focus{Project: 1, Direction: "sideways"}},
		{Focus: Focus{Project: 1, Depth: -1}},
		{MaxNodes: -1},
	}
	for _, o := range bad {
		if _, err := Build(Input{}, o); !errors.Is(err, ErrInvalid) {
			t.Errorf("Build(%+v) = %v, want ErrInvalid", o, err)
		}
	}
}

func TestVersionOrdering(t *testing.T) {
	versions := []string{"main", "1.0.0", "unpinned", "10.0.0", "2.1.0", "2.1.0-rc.1", "v2.0.0", "2.10.0"}
	slices.SortFunc(versions, func(a, b string) int { return -compareVersions(a, b) })
	want := []string{"10.0.0", "2.10.0", "2.1.0", "2.1.0-rc.1", "v2.0.0", "1.0.0", "main", "unpinned"}
	if !slices.Equal(versions, want) {
		t.Errorf("order = %v, want %v", versions, want)
	}
}

func TestLabels(t *testing.T) {
	tests := []struct{ got, want string }{
		{repoLabel("git@github.com:org/infra.git"), "infra"},
		{repoLabel("https://github.com/org/infra"), "infra"},
		{repoLabel("file://host/home/dev/projects/stack"), "stack"},
		{moduleLabel("github.com/org/tf-vpc", "git", ""), "tf-vpc"},
		{moduleLabel("registry.terraform.io/terraform-aws-modules/vpc/aws", "registry", ""), "vpc"},
		{moduleLabel("registry.terraform.io/terraform-aws-modules/eks/aws", "registry", "modules/karpenter"), "eks//modules/karpenter"},
		{moduleLabel("", "other", ""), "module"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
	if !strings.HasPrefix(projectLabel(store.Project{RepoURL: infraURL, Path: "envs/dev"}), "infra/") {
		t.Error("a project in a folder is labelled with the folder")
	}
}
