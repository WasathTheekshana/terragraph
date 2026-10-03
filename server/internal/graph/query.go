package graph

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const (
	// DefaultMaxNodes is the size an unfocused graph is kept to unless the request asks otherwise.
	DefaultMaxNodes = 400
	// LargestMaxNodes is the most a request may ask for.
	LargestMaxNodes = 5000
)

// Source is where the graph's data is read from.
type Source interface {
	ListRepos(ctx context.Context) ([]store.Repo, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	ListModules(ctx context.Context) ([]store.Module, error)
	ListUsages(ctx context.Context) ([]store.Usage, error)
}

// Load reads everything the graph is built from.
func Load(ctx context.Context, s Source) (Input, error) {
	var in Input
	var err error
	if in.Repos, err = s.ListRepos(ctx); err != nil {
		return in, err
	}
	if in.Projects, err = s.ListProjects(ctx); err != nil {
		return in, err
	}
	if in.Modules, err = s.ListModules(ctx); err != nil {
		return in, err
	}
	in.Usages, err = s.ListUsages(ctx)
	return in, err
}

// OptionsFromQuery reads project, repo, or module to focus on; direction (down, up, both);
// depth; level (project or repo); locals; and max_nodes.
func OptionsFromQuery(q url.Values) (Options, error) {
	opts := Options{
		Level:    q.Get("level"),
		MaxNodes: DefaultMaxNodes,
		Focus:    Focus{Direction: q.Get("direction")},
	}

	for name, target := range map[string]*int64{"project": &opts.Focus.Project, "repo": &opts.Focus.Repo, "module": &opts.Focus.Module} {
		if !q.Has(name) {
			continue
		}
		id, err := strconv.ParseInt(q.Get(name), 10, 64)
		if err != nil || id <= 0 {
			return opts, fmt.Errorf("%s must be a positive integer", name)
		}
		*target = id
	}
	if q.Has("depth") {
		depth, err := strconv.Atoi(q.Get("depth"))
		if err != nil || depth < 0 {
			return opts, errors.New("depth must be a whole number, 0 or more")
		}
		opts.Focus.Depth = depth
	}
	if q.Has("locals") {
		locals, err := strconv.ParseBool(q.Get("locals"))
		if err != nil {
			return opts, errors.New("locals must be true or false")
		}
		opts.Locals = locals
	}
	if q.Has("max_nodes") {
		n, err := strconv.Atoi(q.Get("max_nodes"))
		if err != nil || n < 1 || n > LargestMaxNodes {
			return opts, fmt.Errorf("max_nodes must be between 1 and %d", LargestMaxNodes)
		}
		opts.MaxNodes = n
	}
	return opts, nil
}
