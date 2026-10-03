package graph

import (
	"fmt"
	"sort"
	"strconv"
)

// focus narrows the graph to one project, repo, or module and what it connects to.
func (b *builder) focus(g Graph) (Graph, error) {
	f := b.opts.Focus
	seeds, scope, err := b.seeds(g, f)
	if err != nil {
		return Graph{}, err
	}

	direction := f.Direction
	if direction == "" {
		direction = "down"
		if f.Module != 0 {
			direction = "both"
		}
	}

	// A call nested inside a module is only known through the projects that initialised it, so
	// when looking at particular projects, other projects' nested calls are left out. A module's
	// own declared dependencies are the same for everyone.
	allowed := func(e Edge) bool {
		if scope == nil {
			return true
		}
		for _, o := range e.Origins {
			if o != OriginNested {
				return true
			}
		}
		for _, p := range e.Projects {
			if scope[p] {
				return true
			}
		}
		return false
	}

	down := map[string][]string{}
	for _, e := range g.Edges {
		if allowed(e) {
			down[e.From] = append(down[e.From], e.To)
		}
	}

	keep := map[string]bool{}
	for _, s := range seeds {
		keep[s] = true
	}
	if direction == "down" || direction == "both" {
		walk(seeds, down, f.Depth, keep)
	}
	if direction == "up" || direction == "both" {
		walkUp(seeds, g.Edges, allowed, f.Depth, keep)
	}

	out := Graph{Stats: g.Stats}
	hasMember := map[string]bool{}
	for _, n := range g.Nodes {
		if keep[n.ID] && n.Group != "" {
			hasMember[n.Group] = true
		}
	}
	for _, n := range g.Nodes {
		switch {
		case n.Type == TypeRepo || n.Type == TypeModuleRepo:
			if hasMember[n.ID] {
				out.Nodes = append(out.Nodes, n)
			}
		case keep[n.ID]:
			out.Nodes = append(out.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		if keep[e.From] && keep[e.To] && allowed(e) {
			out.Edges = append(out.Edges, e)
		}
	}
	return out, nil
}

// walk adds everything reachable from the starting nodes along the edges, up to depth steps.
// A depth of zero has no limit.
func walk(start []string, next map[string][]string, depth int, seen map[string]bool) {
	frontier := start
	for step := 1; len(frontier) > 0 && (depth == 0 || step <= depth); step++ {
		var following []string
		for _, id := range frontier {
			for _, to := range next[id] {
				if !seen[to] {
					seen[to] = true
					following = append(following, to)
				}
			}
		}
		frontier = following
	}
}

// walkUp finds what depends on the starting nodes. A module's nested calls are only known through
// the projects that initialised it, so going up through one only reaches the projects that were
// seen with it. Without this, every consumer of a module would seem to depend on whatever any
// other consumer's copy of it contained.
func walkUp(start []string, edges []Edge, allowed func(Edge) bool, depth int, seen map[string]bool) {
	into := map[string][]Edge{}
	for _, e := range edges {
		if allowed(e) {
			into[e.To] = append(into[e.To], e)
		}
	}

	type state struct {
		id string
		// projects is nil when nothing restricts which projects count.
		projects map[int64]bool
	}
	visited := map[string]bool{}
	key := func(s state) string {
		if s.projects == nil {
			return s.id + "|*"
		}
		ids := make([]int64, 0, len(s.projects))
		for p := range s.projects {
			ids = append(ids, p)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return s.id + "|" + fmt.Sprint(ids)
	}

	var frontier []state
	for _, id := range start {
		s := state{id: id}
		visited[key(s)] = true
		frontier = append(frontier, s)
	}
	for step := 1; len(frontier) > 0 && (depth == 0 || step <= depth); step++ {
		var following []state
		for _, s := range frontier {
			for _, e := range into[s.id] {
				declared := false
				for _, o := range e.Origins {
					if o == OriginDeclared {
						declared = true
					}
				}
				next := state{id: e.From, projects: s.projects}
				if !declared {
					// Known only through particular projects: keep to those.
					next.projects = restrict(s.projects, e.Projects)
					if len(next.projects) == 0 {
						continue
					}
				}
				seen[e.From] = true
				if k := key(next); !visited[k] {
					visited[k] = true
					following = append(following, next)
				}
			}
		}
		frontier = following
	}
}

// restrict narrows a set of projects to those in the list. A nil set stands for all projects.
func restrict(set map[int64]bool, projects []int64) map[int64]bool {
	out := map[int64]bool{}
	for _, p := range projects {
		if set == nil || set[p] {
			out[p] = true
		}
	}
	return out
}

// seeds finds the nodes a focus starts from, and the projects it covers. scope is nil when the
// focus isn't tied to particular projects.
func (b *builder) seeds(g Graph, f Focus) ([]string, map[int64]bool, error) {
	exists := map[string]bool{}
	for _, n := range g.Nodes {
		exists[n.ID] = true
	}

	switch {
	case f.Project != 0:
		p, ok := b.projects[f.Project]
		if !ok {
			return nil, nil, fmt.Errorf("%w: project %d", ErrNotFound, f.Project)
		}
		scope := map[int64]bool{p.ID: true}
		if ref, internal := b.internal[p.ID]; internal {
			// A project in a module's own repo is part of that module.
			return []string{moduleNodeID(ref.id, ref.subdir)}, scope, nil
		}
		return []string{projectID(p.ID)}, scope, nil

	case f.Repo != 0:
		r, ok := b.repos[f.Repo]
		if !ok {
			return nil, nil, fmt.Errorf("%w: repo %d", ErrNotFound, f.Repo)
		}
		scope := map[int64]bool{}
		var seeds []string
		for _, p := range b.in.Projects {
			if p.RepoID != r.ID {
				continue
			}
			scope[p.ID] = true
			if ref, internal := b.internal[p.ID]; internal {
				seeds = append(seeds, moduleNodeID(ref.id, ref.subdir))
			} else {
				seeds = append(seeds, projectID(p.ID))
			}
		}
		sort.Strings(seeds)
		return dedupe(seeds), scope, nil

	default:
		if _, ok := b.modules[f.Module]; !ok {
			return nil, nil, fmt.Errorf("%w: module %d", ErrNotFound, f.Module)
		}
		var seeds []string
		prefix := "module:" + strconv.FormatInt(f.Module, 10)
		for id := range exists {
			if id == prefix || len(id) > len(prefix) && id[:len(prefix)+1] == prefix+"/" {
				seeds = append(seeds, id)
			}
		}
		sort.Strings(seeds)
		return seeds, nil, nil
	}
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
