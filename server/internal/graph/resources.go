package graph

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

// The resource graph shows which project creates something another project looks up. A data
// block in one project is matched to the resource another project creates by the type and the
// attributes the lookup names it by, and a terraform_remote_state block is matched to the
// project that keeps that state. Names are only known when the source says them, so a lookup
// that depends on something computed is counted but can't be drawn.

const (
	// OriginLookup is a data block that finds a resource another project creates.
	OriginLookup Origin = "lookup"
	// OriginState is a terraform_remote_state block reading another project's state.
	OriginState Origin = "state"
)

// ResourceInput is the data the resource graph is built from.
type ResourceInput struct {
	Repos       []store.Repo
	Projects    []store.Project
	Resources   []store.Resource
	Inventories map[int64]store.Inventory
}

type ResourceOptions struct {
	// Level is "project", "repo", or "resource". The default is "project".
	Level string
	// Focus narrows the graph to one project or repo and what it connects to. The default
	// direction is both.
	Focus Focus
	// Type keeps only the lookups of one resource type.
	Type string
	// Tags keeps only the lookups where the resource looked up, or the one looking, has all
	// of these tags. An empty value only requires the key.
	Tags map[string]string
	// Isolated also shows projects that neither create what others use nor use what others create.
	Isolated bool
	// MaxNodes caps the size of an unfocused graph, leaving out the least connected nodes.
	MaxNodes int
}

func (o ResourceOptions) validate() error {
	switch o.Level {
	case "", "project", "repo", "resource":
	default:
		return fmt.Errorf("%w: level must be project, repo, or resource, not %q", ErrInvalid, o.Level)
	}
	switch o.Focus.Direction {
	case "", "down", "up", "both":
	default:
		return fmt.Errorf("%w: direction must be down, up, or both, not %q", ErrInvalid, o.Focus.Direction)
	}
	if o.Focus.Module != 0 {
		return fmt.Errorf("%w: the resource graph can't focus on a module", ErrInvalid)
	}
	if o.Focus.Project != 0 && o.Focus.Repo != 0 {
		return fmt.Errorf("%w: focus on a project or a repo, not both", ErrInvalid)
	}
	if o.Focus.Depth < 0 || o.MaxNodes < 0 {
		return fmt.Errorf("%w: depth and max_nodes can't be negative", ErrInvalid)
	}
	return nil
}

// link is one lookup and what it found.
type link struct {
	consumer store.Resource
	// producerProject is the project that creates what is looked up; zero when external.
	producerProject int64
	// producer is the resource it creates; nil for a state link, which reads the whole project.
	producer *store.Resource
	origin   Origin
	// exact is false when the match left something unchecked, such as a resource group the
	// creating side doesn't state.
	exact     bool
	ambiguous bool
	external  bool
	// externalKey says what an external lookup was for, such as a resource's name.
	externalKey string
}

// lookupKeys name a resource: a data block that gives one of these finds the resource that has it.
var lookupKeys = []string{"name", "id", "bucket", "zone_name", "cidr_block", "domain", "dns_name"}

// identity is how a data block names what it looks up: the lookup keys and tags it gives.
func identity(r store.Resource) map[string]string {
	id := map[string]string{}
	for _, k := range lookupKeys {
		if v := strings.TrimSpace(r.Attrs[k]); v != "" {
			id[k] = v
		}
	}
	for k, v := range r.Attrs {
		if strings.HasPrefix(k, "tag.") && strings.TrimSpace(v) != "" {
			id[k] = v
		}
	}
	return id
}

// match reports whether the resource a project creates is the one a data block looks up.
func match(d store.Resource, want map[string]string, r store.Resource) (ok, exact bool) {
	if d.Type != r.Type {
		return false, false
	}
	for k, v := range want {
		var have string
		var found bool
		if tag, isTag := strings.CutPrefix(k, "tag."); isTag {
			have, found = r.Tags[tag]
		} else {
			have, found = r.Attrs[k]
		}
		if !found || !strings.EqualFold(strings.TrimSpace(have), v) {
			return false, false
		}
	}
	exact = true
	if rg, ok := d.Attrs["resource_group_name"]; ok {
		have, found := r.Attrs["resource_group_name"]
		switch {
		case !found:
			exact = false
		case !strings.EqualFold(have, rg):
			return false, false
		}
	}
	return true, exact
}

// findLinks matches every data block to what it finds. unresolved counts, by project, the
// lookups whose target depends on something only known when Terraform runs.
func findLinks(in ResourceInput) (links []link, unresolved map[int64]int) {
	unresolved = map[int64]int{}
	producers := map[string][]store.Resource{}
	for _, r := range in.Resources {
		if r.Mode == "managed" && r.Enabled != "false" {
			producers[r.Type] = append(producers[r.Type], r)
		}
	}
	stateOwners := map[string][]int64{}
	for id, inv := range in.Inventories {
		if inv.StateKey != "" {
			stateOwners[inv.StateKey] = append(stateOwners[inv.StateKey], id)
		}
	}
	for _, owners := range stateOwners {
		sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	}

	for _, d := range in.Resources {
		if d.Mode != "data" || d.Enabled == "false" {
			continue
		}
		if d.Type == "terraform_remote_state" {
			key := d.Attrs["state_key"]
			if key == "" {
				unresolved[d.ProjectID]++
				continue
			}
			var owners []int64
			for _, id := range stateOwners[key] {
				if id != d.ProjectID {
					owners = append(owners, id)
				}
			}
			if len(owners) == 0 {
				links = append(links, link{consumer: d, origin: OriginState, external: true, externalKey: key})
				continue
			}
			for _, id := range owners {
				links = append(links, link{consumer: d, producerProject: id, origin: OriginState, exact: true, ambiguous: len(owners) > 1})
			}
			continue
		}

		want := identity(d)
		if len(want) == 0 {
			// Nothing to look it up by. Either it names nothing in particular, like the current
			// client's identity, or it is named by something unresolved.
			if len(d.Unresolved) > 0 && len(producers[d.Type]) > 0 {
				unresolved[d.ProjectID]++
			}
			continue
		}

		var found []store.Resource
		exact := true
		for _, r := range producers[d.Type] {
			if r.ProjectID == d.ProjectID {
				continue
			}
			if ok, ex := match(d, want, r); ok {
				found = append(found, r)
				exact = exact && ex
			}
		}
		if len(found) == 0 {
			if couldBeCreated(producers[d.Type], d, want) {
				unresolved[d.ProjectID]++
				continue
			}
			links = append(links, link{consumer: d, origin: OriginLookup, external: true, externalKey: lookupName(want)})
			continue
		}
		for i := range found {
			r := found[i]
			links = append(links, link{consumer: d, producerProject: r.ProjectID, producer: &r, origin: OriginLookup,
				exact: exact, ambiguous: len(found) > 1})
		}
	}
	return links, unresolved
}

// couldBeCreated reports whether another project creates a resource of the type that has the
// attributes a lookup names it by, but states some of them in a way that couldn't be read. It
// might be what the lookup is for.
func couldBeCreated(rs []store.Resource, d store.Resource, want map[string]string) bool {
	for _, r := range rs {
		if r.ProjectID == d.ProjectID {
			continue
		}
		for k := range want {
			if tag, isTag := strings.CutPrefix(k, "tag."); isTag {
				if _, ok := r.Tags[tag]; ok {
					continue
				}
				_, specific := r.TagsUnresolved[tag]
				_, whole := r.TagsUnresolved["*"]
				if specific || whole {
					return true
				}
			} else if _, ok := r.Attrs[k]; !ok {
				if _, unresolved := r.Unresolved[k]; unresolved {
					return true
				}
			}
		}
	}
	return false
}

func lookupName(want map[string]string) string {
	for _, k := range lookupKeys {
		if v, ok := want[k]; ok {
			return v
		}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0] + "=" + want[keys[0]]
	}
	return ""
}

func hasTags(tags, want map[string]string) bool {
	for k, v := range want {
		have, ok := tags[k]
		if !ok || (v != "" && have != v) {
			return false
		}
	}
	return true
}

// BuildResources builds the graph of which projects create what other projects look up.
func BuildResources(in ResourceInput, opts ResourceOptions) (Graph, error) {
	if err := opts.validate(); err != nil {
		return Graph{}, err
	}
	rb := newResourceBuilder(in, opts)
	if err := rb.checkFocus(); err != nil {
		return Graph{}, err
	}

	links, unresolved := findLinks(in)
	kept := links[:0]
	for _, l := range links {
		if opts.Type != "" && l.consumer.Type != opts.Type {
			continue
		}
		if len(opts.Tags) > 0 {
			ok := hasTags(l.consumer.Tags, opts.Tags)
			if l.producer != nil {
				ok = ok || hasTags(l.producer.Tags, opts.Tags)
			}
			if !ok {
				continue
			}
		}
		kept = append(kept, l)
	}
	rb.unresolved = unresolved

	for _, l := range kept {
		rb.add(l)
	}
	g := rb.graph()

	if opts.Focus.Project != 0 || opts.Focus.Repo != 0 {
		g = rb.focus(g)
	}
	markCycles(&g)
	if opts.Focus.Project == 0 && opts.Focus.Repo == 0 {
		trim(&g, opts.MaxNodes)
	}
	rb.finish(&g)
	return g, nil
}

type resourceBuilder struct {
	in    ResourceInput
	opts  ResourceOptions
	level string

	projects map[int64]store.Project
	repos    map[int64]store.Repo

	nodes map[string]*Node
	edges map[[2]string]*resourceEdge
	// created and looked up, per project.
	created    map[int64]int
	lookedUp   map[int64]int
	unresolved map[int64]int
	links      int
	external   int
}

type resourceEdge struct {
	types     map[string]int
	details   map[string]bool
	projects  map[int64]bool
	origins   map[Origin]bool
	calls     int
	inexact   bool
	ambiguous bool
}

func newResourceBuilder(in ResourceInput, opts ResourceOptions) *resourceBuilder {
	rb := &resourceBuilder{
		in: in, opts: opts, level: opts.Level,
		projects: map[int64]store.Project{}, repos: map[int64]store.Repo{},
		nodes: map[string]*Node{}, edges: map[[2]string]*resourceEdge{},
		created: map[int64]int{}, lookedUp: map[int64]int{},
	}
	if rb.level == "" {
		rb.level = "project"
	}
	for _, p := range in.Projects {
		rb.projects[p.ID] = p
	}
	for _, r := range in.Repos {
		rb.repos[r.ID] = r
	}
	for _, r := range in.Resources {
		if r.Mode == "managed" {
			rb.created[r.ProjectID]++
		} else {
			rb.lookedUp[r.ProjectID]++
		}
	}
	return rb
}

func (rb *resourceBuilder) checkFocus() error {
	f := rb.opts.Focus
	if f.Project != 0 {
		if _, ok := rb.projects[f.Project]; !ok {
			return fmt.Errorf("%w: project %d", ErrNotFound, f.Project)
		}
	}
	if f.Repo != 0 {
		if _, ok := rb.repos[f.Repo]; !ok {
			return fmt.Errorf("%w: repo %d", ErrNotFound, f.Repo)
		}
	}
	return nil
}

const externalNodeID = "external"

// projectNode returns the node standing for a project at the chosen level.
func (rb *resourceBuilder) projectNode(id int64) string {
	p := rb.projects[id]
	if rb.level == "repo" {
		rid := repoNodeID(p.RepoID)
		if _, ok := rb.nodes[rid]; !ok {
			r := rb.repos[p.RepoID]
			rb.nodes[rid] = &Node{ID: rid, Type: TypeRepo, Label: repoLabel(firstNonEmpty(r.RepoURL, p.RepoURL)),
				Href: "/repos/" + strconv.FormatInt(p.RepoID, 10), RepoURL: firstNonEmpty(r.RepoURL, p.RepoURL)}
		}
		return rid
	}
	pid := projectID(id)
	if _, ok := rb.nodes[pid]; !ok {
		rb.nodes[pid] = &Node{ID: pid, Type: TypeProject, Label: projectLabel(p), RepoURL: p.RepoURL, Path: p.Path, Branch: p.LastBranch,
			LastScanAt: p.LastScanAt, Href: "/projects/" + strconv.FormatInt(id, 10), Group: repoNodeID(p.RepoID)}
		rid := repoNodeID(p.RepoID)
		if _, ok := rb.nodes[rid]; !ok {
			r := rb.repos[p.RepoID]
			rb.nodes[rid] = &Node{ID: rid, Type: TypeRepo, Label: repoLabel(firstNonEmpty(r.RepoURL, p.RepoURL)),
				Href: "/repos/" + strconv.FormatInt(p.RepoID, 10), RepoURL: firstNonEmpty(r.RepoURL, p.RepoURL)}
		}
	}
	return pid
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func displayName(r store.Resource) string {
	if n := r.Attrs["name"]; n != "" {
		return n
	}
	return r.Name
}

func resourceNodeID(r store.Resource) string {
	return "resource:" + strconv.FormatInt(r.ProjectID, 10) + ":" + r.Address
}

// resourceNode returns the node of one resource, inside its project, at the resource level.
func (rb *resourceBuilder) resourceNode(r store.Resource) string {
	id := resourceNodeID(r)
	if _, ok := rb.nodes[id]; ok {
		return id
	}
	group := rb.projectNode(r.ProjectID)
	rb.nodes[id] = &Node{
		ID: id, Type: TypeResource, Label: r.Type + "." + r.Name, Sub: displayName(r), Group: group,
		Href: "/projects/" + strconv.FormatInt(r.ProjectID, 10), Details: resourceDetails(r),
	}
	return id
}

func resourceDetails(r store.Resource) []Detail {
	d := []Detail{{"Address", r.Address}}
	if r.Mode == "data" {
		d = append(d, Detail{"Kind", "Looked up (data source)"})
	}
	if n := r.Attrs["name"]; n != "" {
		d = append(d, Detail{"Name", n})
	}
	if rg := r.Attrs["resource_group_name"]; rg != "" {
		d = append(d, Detail{"Resource group", rg})
	}
	if r.File != "" {
		d = append(d, Detail{"Defined at", r.File + ":" + strconv.Itoa(r.Line)})
	}
	if len(r.Tags) > 0 {
		d = append(d, Detail{"Tags", formatTags(r.Tags)})
	}
	return d
}

func formatTags(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + tags[k]
	}
	return strings.Join(parts, ", ")
}

// stateNode stands for a project's state at the resource level.
func (rb *resourceBuilder) stateNode(project int64) string {
	id := "state:" + strconv.FormatInt(project, 10)
	if _, ok := rb.nodes[id]; ok {
		return id
	}
	inv := rb.in.Inventories[project]
	rb.nodes[id] = &Node{ID: id, Type: TypeResource, Label: "terraform state", Sub: inv.StateKey, Group: rb.projectNode(project),
		Href:    "/projects/" + strconv.FormatInt(project, 10),
		Details: []Detail{{"Backend", inv.Backend}, {"State", inv.StateKey}}}
	return id
}

func (rb *resourceBuilder) externalNode(l link) string {
	if rb.level != "resource" {
		if _, ok := rb.nodes[externalNodeID]; !ok {
			rb.nodes[externalNodeID] = &Node{ID: externalNodeID, Type: TypeExternal, Label: "Outside scanned projects",
				Sub: "created elsewhere, or not scanned yet", External: true}
		}
		return externalNodeID
	}
	key := l.consumer.Type + ":" + l.externalKey
	id := "external:" + key
	if _, ok := rb.nodes[id]; !ok {
		label := l.consumer.Type
		if l.origin == OriginState {
			label = "terraform state"
		}
		rb.nodes[id] = &Node{ID: id, Type: TypeExternal, Label: label, Sub: l.externalKey, External: true,
			Details: []Detail{{"Looked up as", l.externalKey}, {"Found in", "no scanned project"}}}
	}
	return id
}

func (rb *resourceBuilder) add(l link) {
	var from, to string
	if rb.level == "resource" {
		from = rb.resourceNode(l.consumer)
	} else {
		from = rb.projectNode(l.consumer.ProjectID)
	}
	switch {
	case l.external:
		to = rb.externalNode(l)
		rb.external++
	case rb.level == "resource" && l.producer != nil:
		to = rb.resourceNode(*l.producer)
	case rb.level == "resource":
		to = rb.stateNode(l.producerProject)
	default:
		to = rb.projectNode(l.producerProject)
	}
	if from == to {
		return
	}
	rb.links++

	e := rb.edges[[2]string{from, to}]
	if e == nil {
		e = &resourceEdge{types: map[string]int{}, details: map[string]bool{}, projects: map[int64]bool{}, origins: map[Origin]bool{}}
		rb.edges[[2]string{from, to}] = e
	}
	kind := l.consumer.Type
	if l.origin == OriginState {
		kind = "terraform state"
	}
	e.types[kind]++
	e.calls++
	e.origins[l.origin] = true
	e.projects[l.consumer.ProjectID] = true
	e.inexact = e.inexact || !l.exact
	e.ambiguous = e.ambiguous || l.ambiguous
	e.details[rb.describe(l)] = true
}

// describe writes one lookup for an edge's details: what looks, what it finds, and where.
func (rb *resourceBuilder) describe(l link) string {
	d := l.consumer
	s := fmt.Sprintf("%s (%s) at %s:%d", d.Address, projectLabel(rb.projects[d.ProjectID]), d.File, d.Line)
	switch {
	case l.external:
		s += " looks up " + l.externalKey + ", which no scanned project creates"
	case l.producer != nil:
		s += " finds " + l.producer.Address + " (" + projectLabel(rb.projects[l.producer.ProjectID]) + ")"
		if !l.exact {
			s += ", matched by name only"
		}
		if l.ambiguous {
			s += ", one of several matches"
		}
	default:
		s += " reads the state of " + projectLabel(rb.projects[l.producerProject])
	}
	return s
}

func (rb *resourceBuilder) graph() Graph {
	g := Graph{Level: rb.level}
	connected := map[string]bool{}
	for k := range rb.edges {
		connected[k[0]], connected[k[1]] = true, true
	}
	if rb.opts.Isolated && rb.level != "resource" {
		have := map[int64]bool{}
		for _, r := range rb.in.Resources {
			if !have[r.ProjectID] {
				have[r.ProjectID] = true
				if _, ok := rb.projects[r.ProjectID]; ok {
					connected[rb.projectNode(r.ProjectID)] = true
				}
			}
		}
	}

	for id, n := range rb.nodes {
		if n.Type == TypeRepo && rb.level != "repo" {
			continue
		}
		if !connected[id] {
			continue
		}
		g.Nodes = append(g.Nodes, *n)
	}
	// A resource sits in its project, and a project in its repo, kept only while something is in them.
	if rb.level != "repo" {
		present := map[string]bool{}
		for _, n := range g.Nodes {
			present[n.ID] = true
		}
		addGroups := func(kind NodeType) {
			have := map[string]bool{}
			for _, n := range g.Nodes {
				if n.Group != "" {
					have[n.Group] = true
				}
			}
			for id := range have {
				if n, ok := rb.nodes[id]; ok && n.Type == kind && !present[id] {
					present[id] = true
					g.Nodes = append(g.Nodes, *n)
				}
			}
		}
		if rb.level == "resource" {
			addGroups(TypeProject)
		}
		addGroups(TypeRepo)
	}
	for k, e := range rb.edges {
		g.Edges = append(g.Edges, rb.edge(k, e))
	}
	return g
}

func (rb *resourceBuilder) edge(k [2]string, e *resourceEdge) Edge {
	type kv struct {
		k string
		n int
	}
	var items []kv
	for t, n := range e.types {
		items = append(items, kv{t, n})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		return items[i].k < items[j].k
	})
	out := Edge{From: k[0], To: k[1], Calls: e.calls, Status: StatusCurrent, Versions: []string{}, Origins: []Origin{}}
	for _, it := range items {
		text := it.k
		if it.n > 1 {
			text += " ×" + strconv.Itoa(it.n)
		}
		out.Items = append(out.Items, text)
	}
	out.Label = out.Items[0]
	if len(out.Items) > 1 {
		out.Label += " +" + strconv.Itoa(len(out.Items)-1)
	}
	if e.inexact || e.ambiguous {
		out.Status = StatusUnknown
	}
	for o := range e.origins {
		out.Origins = append(out.Origins, o)
	}
	sort.Slice(out.Origins, func(i, j int) bool { return out.Origins[i] < out.Origins[j] })
	for d := range e.details {
		out.Details = append(out.Details, d)
	}
	sort.Strings(out.Details)
	for p := range e.projects {
		out.Projects = append(out.Projects, p)
	}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i] < out.Projects[j] })
	return out
}

// focus narrows the graph to what the focused project or repo connects to.
func (rb *resourceBuilder) focus(g Graph) Graph {
	f := rb.opts.Focus
	direction := f.Direction
	if direction == "" {
		direction = "both"
	}
	inFocus := func(n Node) bool {
		var project store.Project
		switch {
		case n.Type == TypeProject || n.Type == TypeResource && n.Group != "":
			pid, ok := projectOfNode(n)
			if !ok {
				return false
			}
			project = rb.projects[pid]
		case n.Type == TypeRepo:
			if f.Project != 0 {
				return n.ID == repoNodeID(rb.projects[f.Project].RepoID)
			}
			return n.ID == repoNodeID(f.Repo)
		default:
			return false
		}
		return f.Project != 0 && project.ID == f.Project || f.Repo != 0 && project.RepoID == f.Repo
	}

	var seeds []string
	for _, n := range g.Nodes {
		if n.Type == TypeResource || n.Type == TypeProject && rb.level == "project" || n.Type == TypeRepo && rb.level == "repo" {
			if inFocus(n) {
				seeds = append(seeds, n.ID)
			}
		}
	}
	down, up := map[string][]string{}, map[string][]string{}
	for _, e := range g.Edges {
		down[e.From] = append(down[e.From], e.To)
		up[e.To] = append(up[e.To], e.From)
	}
	keep := map[string]bool{}
	for _, s := range seeds {
		keep[s] = true
	}
	if direction == "down" || direction == "both" {
		walk(seeds, down, f.Depth, keep)
	}
	if direction == "up" || direction == "both" {
		walk(seeds, up, f.Depth, keep)
	}

	out := Graph{Level: g.Level, Stats: g.Stats}
	// The groups around what is kept stay: a resource's project, and a project's repo.
	include := map[string]bool{}
	for id := range keep {
		include[id] = true
	}
	for range 2 {
		members := map[string]bool{}
		for _, n := range g.Nodes {
			if include[n.ID] && n.Group != "" {
				members[n.Group] = true
			}
		}
		for _, n := range g.Nodes {
			isGroup := n.Type == TypeRepo && rb.level != "repo" || n.Type == TypeProject && rb.level == "resource"
			if isGroup && members[n.ID] {
				include[n.ID] = true
			}
		}
	}
	for _, n := range g.Nodes {
		if include[n.ID] {
			out.Nodes = append(out.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		if keep[e.From] && keep[e.To] {
			out.Edges = append(out.Edges, e)
		}
	}
	return out
}

// projectOfNode reads the project a project or resource node belongs to.
func projectOfNode(n Node) (int64, bool) {
	switch {
	case strings.HasPrefix(n.ID, "project:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(n.ID, "project:"), 10, 64)
		return id, err == nil
	case strings.HasPrefix(n.ID, "resource:"), strings.HasPrefix(n.ID, "state:"):
		rest := n.ID[strings.Index(n.ID, ":")+1:]
		if i := strings.Index(rest, ":"); i >= 0 {
			rest = rest[:i]
		}
		id, err := strconv.ParseInt(rest, 10, 64)
		return id, err == nil
	}
	return 0, false
}

// finish describes the nodes and totals the graph.
func (rb *resourceBuilder) finish(g *Graph) {
	present := map[string]bool{}
	for _, n := range g.Nodes {
		present[n.ID] = true
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Group != "" && !present[n.Group] {
			n.Group = ""
		}
		switch n.Type {
		case TypeProject:
			if id, ok := projectOfNode(*n); ok {
				n.Sub = rb.summary(id)
				n.Details = rb.projectDetails(id)
			}
		case TypeRepo:
			n.Sub, n.Details = rb.repoSummary(*n)
		}
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})

	g.Level = rb.level
	s := &g.Stats
	*s = Stats{Edges: len(g.Edges)}
	unresolved := 0
	for _, n := range g.Nodes {
		switch n.Type {
		case TypeRepo:
			s.Repos++
		case TypeProject:
			s.Projects++
		}
	}
	for _, c := range rb.unresolved {
		unresolved += c
	}
	for _, e := range g.Edges {
		s.Links += e.Calls
		if e.Cycle {
			s.Cycles++
		}
		if e.To == externalNodeID || strings.HasPrefix(e.To, "external:") {
			s.External += e.Calls
		}
	}
	s.Unresolved = unresolved
	if g.Nodes == nil {
		g.Nodes = []Node{}
	}
	if g.Edges == nil {
		g.Edges = []Edge{}
	}
}

func (rb *resourceBuilder) summary(project int64) string {
	return fmt.Sprintf("%d created, %d looked up", rb.created[project], rb.lookedUp[project])
}

func (rb *resourceBuilder) projectDetails(project int64) []Detail {
	d := []Detail{
		{"Creates", strconv.Itoa(rb.created[project]) + " resources"},
		{"Looks up", strconv.Itoa(rb.lookedUp[project]) + " data sources"},
	}
	if n := rb.unresolved[project]; n > 0 {
		d = append(d, Detail{"Unresolved lookups", fmt.Sprintf("%d, which depend on values only known when Terraform runs", n)})
	}
	if inv, ok := rb.in.Inventories[project]; ok && inv.StateKey != "" {
		d = append(d, Detail{"State", inv.StateKey})
	}
	return d
}

func (rb *resourceBuilder) repoSummary(n Node) (string, []Detail) {
	rid, err := strconv.ParseInt(strings.TrimPrefix(n.ID, "repo:"), 10, 64)
	if err != nil {
		return "", nil
	}
	created, looked, unresolved, projects := 0, 0, 0, 0
	for _, p := range rb.in.Projects {
		if p.RepoID != rid {
			continue
		}
		projects++
		created += rb.created[p.ID]
		looked += rb.lookedUp[p.ID]
		unresolved += rb.unresolved[p.ID]
	}
	d := []Detail{
		{"Projects", strconv.Itoa(projects)},
		{"Creates", strconv.Itoa(created) + " resources"},
		{"Looks up", strconv.Itoa(looked) + " data sources"},
	}
	if unresolved > 0 {
		d = append(d, Detail{"Unresolved lookups", strconv.Itoa(unresolved)})
	}
	return fmt.Sprintf("%d created, %d looked up", created, looked), d
}
