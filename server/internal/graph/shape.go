package graph

import (
	"sort"
	"time"
)

// collapseToRepos draws one node per repository instead of one per project. Calls made by
// several projects of one repo become a single edge.
func (b *builder) collapseToRepos(g Graph) Graph {
	repoOf := map[string]string{}
	repos := map[string]*Node{}
	branches := map[string]map[string]bool{}
	for _, n := range g.Nodes {
		if n.Type != TypeRepo {
			continue
		}
		c := n
		c.Projects = 0
		repos[n.ID] = &c
		branches[n.ID] = map[string]bool{}
	}

	out := Graph{Stats: g.Stats}
	for _, n := range g.Nodes {
		if n.Type != TypeProject {
			continue
		}
		r, ok := repos[n.Group]
		if !ok {
			// A project whose repo isn't known stands for itself.
			out.Nodes = append(out.Nodes, n)
			continue
		}
		repoOf[n.ID] = r.ID
		r.Projects++
		r.ModuleCalls += n.ModuleCalls
		r.LocalCalls += n.LocalCalls
		r.Outdated += n.Outdated
		r.MajorBehind += n.MajorBehind
		r.LastScanAt = later(r.LastScanAt, n.LastScanAt)
		branches[r.ID][n.Branch] = true
	}
	for id, r := range repos {
		if len(branches[id]) == 1 {
			for branch := range branches[id] {
				r.Branch = branch
			}
		}
		if r.Projects > 0 {
			out.Nodes = append(out.Nodes, *r)
		}
	}
	for _, n := range g.Nodes {
		if n.Type == TypeProject || n.Type == TypeRepo {
			continue
		}
		out.Nodes = append(out.Nodes, n)
	}

	merged := map[[2]string]*Edge{}
	for _, e := range g.Edges {
		if id, ok := repoOf[e.From]; ok {
			e.From = id
		}
		if id, ok := repoOf[e.To]; ok {
			e.To = id
		}
		key := [2]string{e.From, e.To}
		if m := merged[key]; m != nil {
			mergeEdge(m, e)
			continue
		}
		c := e
		merged[key] = &c
	}
	for _, e := range merged {
		out.Edges = append(out.Edges, *e)
	}
	return out
}

func mergeEdge(into *Edge, e Edge) {
	into.Calls += e.Calls
	into.Origins = unionOrigins(into.Origins, e.Origins)
	into.Versions = unionStrings(into.Versions, e.Versions)
	into.Via = unionStrings(into.Via, e.Via)
	into.Projects = unionInts(into.Projects, e.Projects)
	if statusRank[e.Status] > statusRank[into.Status] {
		into.Status = e.Status
	}
	sort.Slice(into.Versions, func(i, j int) bool { return compareVersions(into.Versions[i], into.Versions[j]) > 0 })
}

func later(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	}
	return a
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

func unionOrigins(a, b []Origin) []Origin {
	seen := map[Origin]bool{}
	var out []Origin
	for _, list := range [][]Origin{a, b} {
		for _, o := range list {
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func unionInts(a, b []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, list := range [][]int64{a, b} {
		for _, n := range list {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// markCycles flags the nodes and edges that lie on a dependency cycle, such as two modules that
// each use the other, so a drawing can show them instead of looping forever on them.
func markCycles(g *Graph) {
	index := map[string]int{}
	for i, n := range g.Nodes {
		index[n.ID] = i
	}
	adj := make([][]int, len(g.Nodes))
	for _, e := range g.Edges {
		from, fok := index[e.From]
		to, tok := index[e.To]
		if fok && tok {
			adj[from] = append(adj[from], to)
		}
	}

	// Tarjan's algorithm for strongly connected components.
	const unvisited = -1
	order := make([]int, len(g.Nodes))
	low := make([]int, len(g.Nodes))
	onStack := make([]bool, len(g.Nodes))
	component := make([]int, len(g.Nodes))
	for i := range order {
		order[i], component[i] = unvisited, unvisited
	}
	var stack []int
	counter, components := 0, 0
	sizes := map[int]int{}

	var visit func(v int)
	visit = func(v int) {
		order[v], low[v] = counter, counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			switch {
			case order[w] == unvisited:
				visit(w)
				low[v] = min(low[v], low[w])
			case onStack[w]:
				low[v] = min(low[v], order[w])
			}
		}
		if low[v] == order[v] {
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				component[w] = components
				sizes[components]++
				if w == v {
					break
				}
			}
			components++
		}
	}
	for v := range g.Nodes {
		if order[v] == unvisited {
			visit(v)
		}
	}

	for i := range g.Edges {
		from, fok := index[g.Edges[i].From]
		to, tok := index[g.Edges[i].To]
		if !fok || !tok {
			continue
		}
		if from == to || (component[from] == component[to] && sizes[component[from]] > 1) {
			g.Edges[i].Cycle = true
			g.Nodes[from].Cycle = true
			g.Nodes[to].Cycle = true
		}
	}
}

// trim keeps a graph to about max nodes by leaving out the least connected ones. Groups don't
// count toward the limit.
func trim(g *Graph, max int) {
	if max <= 0 || countNodes(*g) <= max {
		return
	}
	groups := groupIDs(*g)
	degree := map[string]int{}
	for _, e := range g.Edges {
		degree[e.From]++
		degree[e.To]++
	}
	var real []Node
	for _, n := range g.Nodes {
		if !groups[n.ID] {
			real = append(real, n)
		}
	}
	sort.Slice(real, func(i, j int) bool {
		a, b := score(real[i], degree), score(real[j], degree)
		if a != b {
			return a > b
		}
		return real[i].ID < real[j].ID
	})
	kept := map[string]bool{}
	for _, n := range real[:max] {
		kept[n.ID] = true
	}

	g.HiddenNodes = len(real) - max
	g.Truncated = true
	// Groups stay; finish drops the ones that have nothing left in them.
	nodes := g.Nodes[:0]
	for _, n := range g.Nodes {
		if groups[n.ID] || kept[n.ID] {
			nodes = append(nodes, n)
		}
	}
	g.Nodes = nodes
	edges := g.Edges[:0]
	for _, e := range g.Edges {
		if kept[e.From] && kept[e.To] {
			edges = append(edges, e)
		}
	}
	g.Edges = edges
}

// score ranks nodes for trim: well connected nodes, and ones with something wrong, survive.
func score(n Node, degree map[string]int) int {
	s := degree[n.ID]*2 + n.MajorBehind*3 + n.Outdated
	if n.Cycle || n.Drift {
		s += 5
	}
	return s
}
