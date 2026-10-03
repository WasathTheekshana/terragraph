// Package graph builds the dependency graph of scanned projects and the
// modules they call. It works on plain data and touches no database, so every
// shape of input can be tested directly.
//
// Edges point from the caller to what it calls. There are three kinds of
// caller: a project calling a module directly, a module calling another module
// (seen through a project that initialised it), and a module repo's own
// configuration declaring what it is built on.
package graph

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/source"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

var (
	// ErrNotFound is returned when a focus names a project, repo, or module that doesn't exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid is returned for options that don't make sense together.
	ErrInvalid = errors.New("invalid options")
)

type NodeType string

const (
	TypeRepo       NodeType = "repo"        // a repository holding deployable projects
	TypeProject    NodeType = "project"     // one Terraform root
	TypeModule     NodeType = "module"      // a shared module, or one subdirectory of a repo of modules
	TypeModuleRepo NodeType = "module_repo" // groups the modules that live in one source repo
	TypeLocal      NodeType = "local"       // a ./local module, shown only when asked for
)

// Status says how current a pinned version is. Edges and versions carry the
// worst status among the calls they stand for.
type Status string

const (
	StatusCurrent     Status = "current"
	StatusUnknown     Status = "unknown"
	StatusOutdated    Status = "outdated"
	StatusMajorBehind Status = "major_behind"
)

var statusRank = map[Status]int{StatusCurrent: 0, StatusUnknown: 1, StatusOutdated: 2, StatusMajorBehind: 3}

// Origin says how an edge came to be known.
type Origin string

const (
	// OriginDirect is a call in a project's own configuration.
	OriginDirect Origin = "direct"
	// OriginNested is a call made inside a module, seen through a project that initialised it.
	OriginNested Origin = "nested"
	// OriginDeclared is a call in the configuration of a module repo that was scanned itself.
	OriginDeclared Origin = "declared"
)

const unpinned = "unpinned"

type Node struct {
	ID    string   `json:"id"`
	Type  NodeType `json:"type"`
	Label string   `json:"label"`
	// Group is the id of the group node holding this node, if any.
	Group string `json:"group,omitempty"`
	// Href is the page in the web UI for this node.
	Href string `json:"href,omitempty"`

	// Repos and projects.
	RepoURL     string     `json:"repo_url,omitempty"`
	Path        string     `json:"path,omitempty"`
	Branch      string     `json:"branch,omitempty"`
	LastScanAt  *time.Time `json:"last_scan_at,omitempty"`
	Projects    int        `json:"projects,omitempty"`
	ModuleCalls int        `json:"module_calls,omitempty"`
	LocalCalls  int        `json:"local_calls,omitempty"`
	Outdated    int        `json:"outdated_calls,omitempty"`
	MajorBehind int        `json:"major_behind_calls,omitempty"`

	// Modules.
	Key           string       `json:"key,omitempty"`
	Kind          string       `json:"kind,omitempty"`
	Subdir        string       `json:"subdir,omitempty"`
	LatestVersion string       `json:"latest_version,omitempty"`
	Versions      []VersionUse `json:"versions,omitempty"`
	Consumers     int          `json:"consumers,omitempty"`
	// Drift is true when more than one version of the module is pinned.
	Drift bool `json:"drift,omitempty"`
	// External is true when the module's own repo hasn't been scanned, so what it depends on is unknown.
	External bool `json:"external,omitempty"`
	// Unused is true when no scanned project calls the module.
	Unused bool `json:"unused,omitempty"`
	// RepoHref is the page of the module's own repo, when it has been scanned.
	RepoHref string `json:"repo_href,omitempty"`

	// Local modules.
	Source string `json:"source,omitempty"`

	// Cycle is true when the node is part of a dependency cycle.
	Cycle bool `json:"cycle,omitempty"`
}

// VersionUse is one version of a module that something pins.
type VersionUse struct {
	Version  string `json:"version"`
	Projects int    `json:"projects"`
	Status   Status `json:"status"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Origins lists how the edge is known: direct, nested, declared.
	Origins []Origin `json:"origins"`
	// Calls counts the module calls the edge stands for.
	Calls    int      `json:"calls"`
	Versions []string `json:"versions"`
	Status   Status   `json:"status"`
	// Via names the local modules the call passes through, when they are folded away.
	Via []string `json:"via,omitempty"`
	// Projects are the projects whose calls make up the edge.
	Projects []int64 `json:"projects"`
	Cycle    bool    `json:"cycle,omitempty"`
}

type Stats struct {
	Repos    int `json:"repos"`
	Projects int `json:"projects"`
	Modules  int `json:"modules"`
	Edges    int `json:"edges"`
	Cycles   int `json:"cycles"`
	// Orphans counts nested calls whose calling module is missing from the data.
	Orphans int `json:"orphans"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	// Level is the level the graph was drawn at, which can differ from the one asked for when it was too large.
	Level string `json:"level"`
	// Truncated is true when nodes were left out to keep the graph a usable size.
	Truncated   bool  `json:"truncated"`
	HiddenNodes int   `json:"hidden_nodes,omitempty"`
	Stats       Stats `json:"stats"`
}

// Input is the data the graph is built from.
type Input struct {
	Repos    []store.Repo
	Projects []store.Project
	Modules  []store.Module
	Usages   []store.Usage
}

type Options struct {
	// Level is "project", one node per Terraform root, or "repo", one node per repository.
	// The default is "project".
	Level string
	// Locals shows ./local modules as nodes. Otherwise they are folded into whatever calls them.
	Locals bool
	// Focus narrows the graph to one project, repo, or module and what it connects to.
	Focus Focus
	// MaxNodes caps the size of an unfocused graph. A project-level graph over the cap is drawn
	// at repo level instead, and if that is still too large the least connected nodes are left
	// out. Zero means no cap.
	MaxNodes int
}

// Focus narrows a graph. At most one of Project, Repo, and Module is set.
type Focus struct {
	Project int64
	Repo    int64
	Module  int64
	// Direction is "down" (what it depends on), "up" (what depends on it), or "both".
	// The default is down for a project or repo, and both for a module.
	Direction string
	// Depth limits how many steps to follow. Zero follows everything.
	Depth int
}

func (f Focus) set() bool { return f.Project != 0 || f.Repo != 0 || f.Module != 0 }

func (o Options) validate() error {
	if o.Level != "" && o.Level != "project" && o.Level != "repo" {
		return fmt.Errorf("%w: level must be project or repo, not %q", ErrInvalid, o.Level)
	}
	n := 0
	for _, id := range []int64{o.Focus.Project, o.Focus.Repo, o.Focus.Module} {
		if id != 0 {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("%w: focus on one of project, repo, or module", ErrInvalid)
	}
	switch o.Focus.Direction {
	case "", "down", "up", "both":
	default:
		return fmt.Errorf("%w: direction must be down, up, or both, not %q", ErrInvalid, o.Focus.Direction)
	}
	if o.Focus.Depth < 0 || o.MaxNodes < 0 {
		return fmt.Errorf("%w: depth and max nodes can't be negative", ErrInvalid)
	}
	return nil
}

// Build turns the data into a graph. Nodes and edges come back in a stable order.
func Build(in Input, opts Options) (Graph, error) {
	if err := opts.validate(); err != nil {
		return Graph{}, err
	}
	b := newBuilder(in, opts)
	b.addNodes()
	b.addEdges()
	b.finishModules()

	g := b.snapshot()
	if opts.Focus.set() {
		var err error
		if g, err = b.focus(g); err != nil {
			return Graph{}, err
		}
	}

	level := opts.Level
	if level == "" {
		level = "project"
	}
	if level == "project" && !opts.Focus.set() && opts.MaxNodes > 0 && countNodes(g) > opts.MaxNodes {
		level = "repo"
	}
	if level == "repo" {
		g = b.collapseToRepos(g)
	}

	markCycles(&g)
	if !opts.Focus.set() {
		trim(&g, opts.MaxNodes)
	}
	b.finish(&g, level)
	return g, nil
}

// builder holds the state of one Build.
type builder struct {
	in   Input
	opts Options

	repos    map[int64]store.Repo
	projects map[int64]store.Project
	modules  map[int64]store.Module

	// consumed holds, for each module, the subdirectories that something calls.
	consumed map[int64]map[string]bool
	// internal maps a project to the module (and subdirectory) whose own source it is.
	internal map[int64]moduleRef

	nodes map[string]*Node
	edges map[[2]string]*edgeAcc
	// uses records, for each module node, which projects pin which version.
	uses    map[string]map[string]*versionAcc
	callers map[string]map[int64]bool

	orphans int
}

type moduleRef struct {
	id     int64
	subdir string
}

type edgeAcc struct {
	calls    int
	versions map[string]bool
	status   Status
	origins  map[Origin]bool
	via      map[string]bool
	projects map[int64]bool
}

type versionAcc struct {
	projects map[int64]bool
	status   Status
}

func newBuilder(in Input, opts Options) *builder {
	b := &builder{
		in:       in,
		opts:     opts,
		repos:    map[int64]store.Repo{},
		projects: map[int64]store.Project{},
		modules:  map[int64]store.Module{},
		consumed: map[int64]map[string]bool{},
		internal: map[int64]moduleRef{},
		nodes:    map[string]*Node{},
		edges:    map[[2]string]*edgeAcc{},
		uses:     map[string]map[string]*versionAcc{},
		callers:  map[string]map[int64]bool{},
	}
	for _, r := range in.Repos {
		b.repos[r.ID] = r
	}
	for _, p := range in.Projects {
		b.projects[p.ID] = p
	}
	for _, m := range in.Modules {
		b.modules[m.ID] = m
	}
	for _, u := range in.Usages {
		if u.ModuleID != nil {
			b.markConsumed(*u.ModuleID, source.Subdir(u.Source))
		}
	}
	for _, p := range in.Projects {
		repo, ok := b.repos[p.RepoID]
		if !ok || repo.ModuleID == nil {
			continue
		}
		// A project in a module's own repo is part of that module when its folder is one that
		// something calls. Any other root in the repo, such as an environment, is deployed on
		// its own and stays a project.
		if dir := folderSubdir(p.Path); b.consumed[*repo.ModuleID][dir] {
			b.internal[p.ID] = moduleRef{id: *repo.ModuleID, subdir: dir}
		}
	}
	return b
}

func (b *builder) markConsumed(module int64, subdir string) {
	if b.consumed[module] == nil {
		b.consumed[module] = map[string]bool{}
	}
	b.consumed[module][subdir] = true
}

// addNodes creates a node for every project that is deployed on its own, and for every module,
// so that projects with no module calls and modules nobody uses still appear.
func (b *builder) addNodes() {
	repoProjects := map[int64]int{}
	for _, p := range b.in.Projects {
		if _, internal := b.internal[p.ID]; internal {
			continue
		}
		repoProjects[p.RepoID]++
		n := &Node{
			ID:          projectID(p.ID),
			Type:        TypeProject,
			Label:       projectLabel(p),
			Href:        "/projects/" + strconv.FormatInt(p.ID, 10),
			RepoURL:     p.RepoURL,
			Path:        p.Path,
			Branch:      p.LastBranch,
			LastScanAt:  p.LastScanAt,
			ModuleCalls: p.ModuleCalls,
			Outdated:    p.OutdatedCalls,
			MajorBehind: p.MajorBehindCalls,
			Group:       repoNodeID(p.RepoID),
		}
		b.nodes[n.ID] = n
	}
	for repoIDValue, count := range repoProjects {
		r, ok := b.repos[repoIDValue]
		url := ""
		if ok {
			url = r.RepoURL
		}
		b.nodes[repoNodeID(repoIDValue)] = &Node{
			ID:       repoNodeID(repoIDValue),
			Type:     TypeRepo,
			Label:    repoLabel(url),
			Href:     "/repos/" + strconv.FormatInt(repoIDValue, 10),
			RepoURL:  url,
			Projects: count,
		}
	}

	// Every module gets a node for each subdirectory something calls, and one for each
	// subdirectory of its own repo that was scanned. A module nobody calls gets its root.
	for _, m := range b.in.Modules {
		dirs := map[string]bool{}
		for dir := range b.consumed[m.ID] {
			dirs[dir] = true
		}
		for _, ref := range b.internal {
			if ref.id == m.ID {
				dirs[ref.subdir] = true
			}
		}
		if len(dirs) == 0 {
			dirs[""] = true
		}
		for dir := range dirs {
			b.moduleNode(m.ID, dir)
		}
	}
}

// moduleNode returns the node for a module, or one of the subdirectories of its repo.
func (b *builder) moduleNode(id int64, subdir string) *Node {
	key := moduleNodeID(id, subdir)
	if n, ok := b.nodes[key]; ok {
		return n
	}
	m, known := b.modules[id]
	n := &Node{
		ID:     key,
		Type:   TypeModule,
		Label:  moduleLabel(m.Key, m.Kind, subdir),
		Href:   "/modules/" + strconv.FormatInt(id, 10),
		Key:    m.Key,
		Kind:   m.Kind,
		Subdir: subdir,
	}
	if !known {
		n.Label = "module " + strconv.FormatInt(id, 10)
	}
	if m.LatestVersion != nil {
		n.LatestVersion = *m.LatestVersion
	}
	if m.RepoID != nil {
		n.RepoHref = "/repos/" + strconv.FormatInt(*m.RepoID, 10)
	} else {
		n.External = true
	}
	b.nodes[key] = n
	return n
}

// projectCtx is one project's module calls, indexed so that a nested call can find the call it
// sits inside.
type projectCtx struct {
	project  store.Project
	rootID   string
	internal bool
	rows     map[string]store.Usage
}

func fullPath(u store.Usage) string {
	if u.Parent == "" {
		return u.CallName
	}
	return u.Parent + "." + u.CallName
}

func (b *builder) addEdges() {
	byProject := map[int64][]store.Usage{}
	for _, u := range b.in.Usages {
		byProject[u.ProjectID] = append(byProject[u.ProjectID], u)
	}
	ids := make([]int64, 0, len(byProject))
	for id := range byProject {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		p, ok := b.projects[id]
		if !ok {
			continue
		}
		ctx := &projectCtx{project: p, rootID: projectID(id), rows: map[string]store.Usage{}}
		if ref, internal := b.internal[id]; internal {
			ctx.internal = true
			ctx.rootID = b.moduleNode(ref.id, ref.subdir).ID
		}
		rows := byProject[id]
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Parent != rows[j].Parent {
				return rows[i].Parent < rows[j].Parent
			}
			return rows[i].CallName < rows[j].CallName
		})
		for _, u := range rows {
			ctx.rows[fullPath(u)] = u
		}
		for _, u := range rows {
			b.addCall(ctx, u)
		}
	}
}

// addCall records one module call as an edge from whatever contains it.
func (b *builder) addCall(ctx *projectCtx, u store.Usage) {
	from, via := b.caller(ctx, u, 0)

	if u.ModuleID == nil {
		// A local module is part of the code that calls it.
		if n := b.nodes[projectID(ctx.project.ID)]; n != nil && !ctx.internal {
			n.LocalCalls++
		}
		if b.opts.Locals {
			local := b.localNode(ctx, u)
			b.addEdge(from, local.ID, ctx, u, via, b.originOf(ctx, u), "")
		}
		return
	}

	to := b.moduleNode(*u.ModuleID, source.Subdir(u.Source))
	b.addEdge(from, to.ID, ctx, u, via, b.originOf(ctx, u), to.ID)
}

// caller finds the node a call belongs to. A call made inside a shared module belongs to that
// module. A call made inside a local module belongs to whatever called the local module, which
// is recorded in via, unless local modules are shown as nodes of their own.
func (b *builder) caller(ctx *projectCtx, u store.Usage, depth int) (string, []string) {
	if u.Parent == "" {
		return ctx.rootID, nil
	}
	parent, ok := ctx.rows[u.Parent]
	if !ok || depth > 64 {
		// The call says it is nested in a call that was never recorded.
		b.orphans++
		return ctx.rootID, nil
	}
	if parent.ModuleID != nil {
		return b.moduleNode(*parent.ModuleID, source.Subdir(parent.Source)).ID, nil
	}
	if b.opts.Locals {
		return b.localNode(ctx, parent).ID, nil
	}
	id, via := b.caller(ctx, parent, depth+1)
	return id, append(via, localName(parent))
}

// originOf says how an edge is known. A call inside any shared module is nested. Otherwise it
// is declared when it sits in a module's own repo, and direct when it sits in a project.
func (b *builder) originOf(ctx *projectCtx, u store.Usage) Origin {
	for parent, depth := u.Parent, 0; parent != "" && depth < 64; depth++ {
		row, ok := ctx.rows[parent]
		if !ok {
			break
		}
		if row.ModuleID != nil {
			return OriginNested
		}
		parent = row.Parent
	}
	if ctx.internal {
		return OriginDeclared
	}
	return OriginDirect
}

func (b *builder) localNode(ctx *projectCtx, u store.Usage) *Node {
	id := localNodeID(ctx.project.ID, fullPath(u))
	if n, ok := b.nodes[id]; ok {
		return n
	}
	n := &Node{ID: id, Type: TypeLocal, Label: u.CallName, Source: u.Source}
	b.nodes[id] = n
	return n
}

func (b *builder) addEdge(from, to string, ctx *projectCtx, u store.Usage, via []string, origin Origin, moduleNode string) {
	key := [2]string{from, to}
	e := b.edges[key]
	if e == nil {
		e = &edgeAcc{versions: map[string]bool{}, origins: map[Origin]bool{}, via: map[string]bool{}, projects: map[int64]bool{}}
		b.edges[key] = e
	}
	status := usageStatus(u)
	label := versionLabel(u)
	e.calls++
	e.versions[label] = true
	e.origins[origin] = true
	e.projects[ctx.project.ID] = true
	for _, v := range via {
		e.via[v] = true
	}
	e.status = worse(e.status, status, e.calls == 1)

	if moduleNode == "" {
		return
	}
	if b.uses[moduleNode] == nil {
		b.uses[moduleNode] = map[string]*versionAcc{}
		b.callers[moduleNode] = map[int64]bool{}
	}
	v := b.uses[moduleNode][label]
	if v == nil {
		v = &versionAcc{projects: map[int64]bool{}, status: status}
		b.uses[moduleNode][label] = v
	}
	v.projects[ctx.project.ID] = true
	v.status = worse(v.status, status, false)
	b.callers[moduleNode][ctx.project.ID] = true
}

// worse returns the more serious of two statuses.
func worse(a, b Status, first bool) Status {
	if first || a == "" {
		return b
	}
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

// finishModules fills in what is known about each module from the calls that use it.
func (b *builder) finishModules() {
	for id, n := range b.nodes {
		if n.Type != TypeModule {
			continue
		}
		n.Consumers = len(b.callers[id])
		n.Unused = n.Consumers == 0
		pinned := 0
		for version, acc := range b.uses[id] {
			n.Versions = append(n.Versions, VersionUse{Version: version, Projects: len(acc.projects), Status: acc.status})
			if version != unpinned {
				pinned++
			}
		}
		sort.Slice(n.Versions, func(i, j int) bool { return compareVersions(n.Versions[i].Version, n.Versions[j].Version) > 0 })
		n.Drift = pinned > 1
	}

	// Modules that share a repo are grouped, once there is more than one of them.
	members := map[int64][]*Node{}
	for _, n := range b.nodes {
		if n.Type != TypeModule {
			continue
		}
		if id, ok := moduleIDOf(n.ID); ok {
			members[id] = append(members[id], n)
		}
	}
	for id, nodes := range members {
		if len(nodes) < 2 {
			continue
		}
		group := moduleRepoNodeID(id)
		m := b.modules[id]
		b.nodes[group] = &Node{
			ID:    group,
			Type:  TypeModuleRepo,
			Label: moduleLabel(m.Key, m.Kind, ""),
			Href:  "/modules/" + strconv.FormatInt(id, 10),
			Key:   m.Key,
			Kind:  m.Kind,
		}
		for _, n := range nodes {
			n.Group = group
		}
	}
}

// snapshot copies the builder's nodes and edges into a Graph.
func (b *builder) snapshot() Graph {
	g := Graph{Stats: Stats{Orphans: b.orphans}}
	for _, n := range b.nodes {
		g.Nodes = append(g.Nodes, *n)
	}
	for key, e := range b.edges {
		g.Edges = append(g.Edges, finishEdge(key, e))
	}
	return g
}

func finishEdge(key [2]string, e *edgeAcc) Edge {
	edge := Edge{From: key[0], To: key[1], Calls: e.calls, Status: e.status}
	for o := range e.origins {
		edge.Origins = append(edge.Origins, o)
	}
	sort.Slice(edge.Origins, func(i, j int) bool { return edge.Origins[i] < edge.Origins[j] })
	for v := range e.versions {
		edge.Versions = append(edge.Versions, v)
	}
	sort.Slice(edge.Versions, func(i, j int) bool { return compareVersions(edge.Versions[i], edge.Versions[j]) > 0 })
	for v := range e.via {
		edge.Via = append(edge.Via, v)
	}
	sort.Strings(edge.Via)
	for p := range e.projects {
		edge.Projects = append(edge.Projects, p)
	}
	sort.Slice(edge.Projects, func(i, j int) bool { return edge.Projects[i] < edge.Projects[j] })
	return edge
}

// finish orders everything, drops groups with nothing in them, and fills in the totals.
func (b *builder) finish(g *Graph, level string) {
	present := map[string]bool{}
	for _, n := range g.Nodes {
		present[n.ID] = true
	}
	members := map[string]int{}
	for _, n := range g.Nodes {
		if n.Group != "" {
			members[n.Group]++
		}
	}
	nodes := g.Nodes[:0]
	for _, n := range g.Nodes {
		if (n.Type == TypeRepo && level == "project" || n.Type == TypeModuleRepo) && members[n.ID] == 0 {
			continue
		}
		if n.Group != "" && !present[n.Group] {
			n.Group = ""
		}
		nodes = append(nodes, n)
	}
	g.Nodes = nodes
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})

	g.Level = level
	s := &g.Stats
	s.Repos, s.Projects, s.Modules, s.Edges, s.Cycles = 0, 0, 0, len(g.Edges), 0
	for _, n := range g.Nodes {
		switch n.Type {
		case TypeRepo:
			s.Repos++
		case TypeProject:
			s.Projects++
		case TypeModule:
			s.Modules++
		}
	}
	for _, e := range g.Edges {
		if e.Cycle {
			s.Cycles++
		}
	}
	if g.Nodes == nil {
		g.Nodes = []Node{}
	}
	if g.Edges == nil {
		g.Edges = []Edge{}
	}
}

// groupIDs are the nodes that other nodes sit inside. A repo is a group while its projects are
// drawn, and an ordinary node once they are folded into it.
func groupIDs(g Graph) map[string]bool {
	groups := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Group != "" {
			groups[n.Group] = true
		}
	}
	return groups
}

// countNodes counts the nodes that are drawn as themselves, not the groups around them.
func countNodes(g Graph) int {
	groups := groupIDs(g)
	n := 0
	for _, node := range g.Nodes {
		if !groups[node.ID] {
			n++
		}
	}
	return n
}

func usageStatus(u store.Usage) Status {
	switch {
	case u.MajorsBehind != nil && *u.MajorsBehind > 0:
		return StatusMajorBehind
	case u.Outdated:
		return StatusOutdated
	case u.PinnedVersion != nil && u.LatestVersion != nil:
		return StatusCurrent
	}
	return StatusUnknown
}

func versionLabel(u store.Usage) string {
	switch {
	case u.PinnedVersion != nil:
		return *u.PinnedVersion
	case u.RefResolved != "":
		return u.RefResolved
	case u.RefDeclared != "":
		return u.RefDeclared
	}
	return unpinned
}

func projectID(id int64) string        { return "project:" + strconv.FormatInt(id, 10) }
func repoNodeID(id int64) string       { return "repo:" + strconv.FormatInt(id, 10) }
func moduleRepoNodeID(id int64) string { return "module_repo:" + strconv.FormatInt(id, 10) }

func moduleNodeID(id int64, subdir string) string {
	if subdir == "" {
		return "module:" + strconv.FormatInt(id, 10)
	}
	return "module:" + strconv.FormatInt(id, 10) + "/" + subdir
}

func localNodeID(project int64, path string) string {
	return "local:" + strconv.FormatInt(project, 10) + ":" + path
}

// moduleIDOf reads the module id back out of a module node id.
func moduleIDOf(nodeID string) (int64, bool) {
	rest, ok := strings.CutPrefix(nodeID, "module:")
	if !ok {
		return 0, false
	}
	idPart, _, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(idPart, 10, 64)
	return id, err == nil
}

// folderSubdir turns a project's path inside its repo into the subdirectory a module source
// would name: the repo root is "".
func folderSubdir(path string) string {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if path == "." {
		return ""
	}
	return path
}
