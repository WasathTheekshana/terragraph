package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/WasathTheekshana/terragraph/server/internal/report"
)

// Resource is something a project creates (managed) or looks up (data).
type Resource struct {
	ProjectID      int64             `json:"project_id"`
	RepoID         int64             `json:"repo_id"`
	RepoURL        string            `json:"repo_url"`
	ProjectPath    string            `json:"project_path"`
	Address        string            `json:"address"`
	Mode           string            `json:"mode"`
	Type           string            `json:"type"`
	Name           string            `json:"name"`
	ModulePath     string            `json:"module_path,omitempty"`
	Enabled        string            `json:"enabled"`
	Instances      *int              `json:"instances,omitempty"`
	Condition      string            `json:"condition,omitempty"`
	File           string            `json:"file,omitempty"`
	Line           int               `json:"line,omitempty"`
	Attrs          map[string]string `json:"attrs"`
	Unresolved     map[string]string `json:"unresolved,omitempty"`
	Tags           map[string]string `json:"tags"`
	TagsUnresolved map[string]string `json:"tags_unresolved,omitempty"`
}

// Variable is an input of a project or of a module it calls.
type Variable struct {
	ModulePath string   `json:"module_path,omitempty"`
	Name       string   `json:"name"`
	Type       string   `json:"type,omitempty"`
	Value      string   `json:"value,omitempty"`
	Source     string   `json:"source,omitempty"`
	Sensitive  bool     `json:"sensitive,omitempty"`
	Flag       bool     `json:"flag,omitempty"`
	Controls   []string `json:"controls,omitempty"`
}

type Output struct {
	ModulePath string `json:"module_path,omitempty"`
	Name       string `json:"name"`
	Expr       string `json:"expr,omitempty"`
	Sensitive  bool   `json:"sensitive,omitempty"`
}

// Inventory is what a project's scan recorded besides its resources.
type Inventory struct {
	ProjectID int64 `json:"project_id"`
	// Scanned is false for a project whose scans haven't included resources.
	Scanned   bool       `json:"scanned"`
	Backend   string     `json:"backend,omitempty"`
	StateKey  string     `json:"state_key,omitempty"`
	Variables []Variable `json:"variables"`
	Outputs   []Output   `json:"outputs"`
}

// ResourceFilter narrows ListResources. Zero fields match everything.
type ResourceFilter struct {
	ProjectID int64
	RepoID    int64
	// Type is a resource type, "azurerm_virtual_network".
	Type string
	// Mode is "managed" or "data".
	Mode string
	// Enabled is "true", "false", or "unknown".
	Enabled string
	// Tags must all be present; an empty value only requires the key.
	Tags map[string]string
	// Query matches the address or a recorded name, ignoring case.
	Query string
	// Limit and Offset page the list. A zero limit returns everything.
	Limit, Offset int
}

// resourceWhere builds the WHERE clause for a filter, with resources aliased r and projects p.
// skipType leaves the type out, for counting what the other types would add.
func resourceWhere(f ResourceFilter, skipType bool) (string, []any) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.ProjectID != 0 {
		where = append(where, "r.project_id = "+arg(f.ProjectID))
	}
	if f.RepoID != 0 {
		where = append(where, "p.repo_id = "+arg(f.RepoID))
	}
	if f.Type != "" && !skipType {
		where = append(where, "r.resource_type = "+arg(f.Type))
	}
	if f.Mode != "" {
		where = append(where, "r.mode = "+arg(f.Mode))
	}
	if f.Enabled != "" {
		where = append(where, "r.enabled = "+arg(f.Enabled))
	}
	for _, k := range sortedTagKeys(f.Tags) {
		if v := f.Tags[k]; v == "" {
			where = append(where, "r.tags ? "+arg(k))
		} else {
			b, _ := json.Marshal(map[string]string{k: v})
			where = append(where, "r.tags @> "+arg(string(b))+"::jsonb")
		}
	}
	if f.Query != "" {
		q := arg("%" + escapeLike(f.Query) + "%")
		where = append(where, "(r.address ILIKE "+q+" OR r.attrs->>'name' ILIKE "+q+")")
	}
	if len(where) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(where, " AND "), args
}

func sortedTagKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ListResources returns the resources matching the filter, ordered by repo, project, and address.
func (s *Store) ListResources(ctx context.Context, f ResourceFilter) ([]Resource, error) {
	clause, args := resourceWhere(f, false)
	page := ""
	if f.Limit > 0 {
		args = append(args, f.Limit, f.Offset)
		page = fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.project_id, p.repo_id, p.repo_url, p.path, r.address, r.mode, r.resource_type, r.name, r.module_path,
			r.enabled, r.instances, r.condition, r.file, r.line, r.attrs, r.unresolved, r.tags, r.tags_unresolved
		FROM resources r
		JOIN projects p ON p.id = r.project_id
		`+clause+`
		ORDER BY p.repo_url, p.path, r.address`+page, args...)
	if err != nil {
		return nil, fmt.Errorf("querying resources: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Resource, error) {
		var r Resource
		err := row.Scan(&r.ProjectID, &r.RepoID, &r.RepoURL, &r.ProjectPath, &r.Address, &r.Mode, &r.Type, &r.Name, &r.ModulePath,
			&r.Enabled, &r.Instances, &r.Condition, &r.File, &r.Line, &r.Attrs, &r.Unresolved, &r.Tags, &r.TagsUnresolved)
		return r, err
	})
}

// CountResources counts the resources matching the filter, ignoring its paging.
func (s *Store) CountResources(ctx context.Context, f ResourceFilter) (int, error) {
	clause, args := resourceWhere(f, false)
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM resources r JOIN projects p ON p.id = r.project_id `+clause, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting resources: %w", err)
	}
	return n, nil
}

// Count is a value and how many resources have it.
type Count struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Facets summarise the resources that match a filter, for narrowing it further.
type Facets struct {
	// Types are the resource types, counted without the filter's own type, so the others stay
	// visible while one is chosen.
	Types   []Count `json:"types"`
	TagKeys []Count `json:"tag_keys"`
	// TagValues are the values of the first tag the filter asks for without a value.
	TagKey    string  `json:"tag_key,omitempty"`
	TagValues []Count `json:"tag_values,omitempty"`
}

const facetLimit = 100

func (s *Store) ResourceFacets(ctx context.Context, f ResourceFilter) (Facets, error) {
	facets := Facets{Types: []Count{}, TagKeys: []Count{}}
	counts := func(query string, args ...any) ([]Count, error) {
		rows, err := s.pool.Query(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("querying resource facets: %w", err)
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Count, error) {
			var c Count
			return c, row.Scan(&c.Value, &c.Count)
		})
		if out == nil {
			out = []Count{}
		}
		return out, err
	}

	clause, args := resourceWhere(f, true)
	var err error
	facets.Types, err = counts(`
		SELECT r.resource_type, count(*) FROM resources r JOIN projects p ON p.id = r.project_id `+clause+`
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT `+fmt.Sprint(facetLimit), args...)
	if err != nil {
		return facets, err
	}

	clause, args = resourceWhere(f, false)
	facets.TagKeys, err = counts(`
		SELECT k, count(*) FROM resources r JOIN projects p ON p.id = r.project_id, jsonb_object_keys(r.tags) k `+clause+`
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT `+fmt.Sprint(facetLimit), args...)
	if err != nil {
		return facets, err
	}

	for _, k := range sortedTagKeys(f.Tags) {
		if f.Tags[k] != "" {
			continue
		}
		facets.TagKey = k
		args = append(args, k)
		facets.TagValues, err = counts(`
			SELECT r.tags->>$`+fmt.Sprint(len(args))+`, count(*) FROM resources r JOIN projects p ON p.id = r.project_id `+clause+`
			GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT `+fmt.Sprint(facetLimit), args...)
		break
	}
	return facets, err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListInventories returns what each project's scan recorded besides its resources,
// by project id. Projects that haven't been scanned with resources have none.
func (s *Store) ListInventories(ctx context.Context) (map[int64]Inventory, error) {
	rows, err := s.pool.Query(ctx, `SELECT project_id, backend, state_key, variables, outputs FROM project_inventory`)
	if err != nil {
		return nil, fmt.Errorf("querying inventories: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Inventory, error) {
		var inv Inventory
		inv.Scanned = true
		err := row.Scan(&inv.ProjectID, &inv.Backend, &inv.StateKey, &inv.Variables, &inv.Outputs)
		return inv, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]Inventory, len(list))
	for _, inv := range list {
		out[inv.ProjectID] = inv
	}
	return out, nil
}

// replaceInventory makes a project's recorded resources, variables, and outputs
// those of a scan. It does nothing for a report with no inventory fact, such as
// one from an older scanner, which leaves what is there alone.
func replaceInventory(ctx context.Context, tx pgx.Tx, projectID, scanID int64, facts []report.Fact) error {
	var marker *report.Fact
	for i := range facts {
		if facts[i].Type == report.FactTypeInventory {
			marker = &facts[i]
		}
	}
	if marker == nil {
		return nil
	}

	variables, outputs := []Variable{}, []Output{}
	var rows [][]any
	for _, f := range facts {
		switch f.Type {
		case report.FactTypeResource:
			rows = append(rows, []any{
				projectID, f.Address, f.Mode, f.ResourceType, f.Name, f.Parent, f.Enabled, f.Instances, f.Condition, f.File, f.Line,
				stringMap(f.Attrs), stringMap(f.Unresolved), stringMap(f.Tags), stringMap(f.TagsUnresolved), scanID,
			})
		case report.FactTypeVariable:
			variables = append(variables, Variable{ModulePath: f.Parent, Name: f.Name, Type: f.VarType, Value: f.Value,
				Source: f.ValueSource, Sensitive: f.Sensitive, Flag: f.Flag, Controls: f.Controls})
		case report.FactTypeOutput:
			outputs = append(outputs, Output{ModulePath: f.Parent, Name: f.Name, Expr: f.Expr, Sensitive: f.Sensitive})
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM resources WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("clearing resources: %w", err)
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"resources"}, []string{
		"project_id", "address", "mode", "resource_type", "name", "module_path", "enabled", "instances", "condition", "file", "line",
		"attrs", "unresolved", "tags", "tags_unresolved", "scan_id",
	}, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("inserting resources: %w", err)
	}

	vars, err := json.Marshal(variables)
	if err != nil {
		return err
	}
	outs, err := json.Marshal(outputs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO project_inventory (project_id, backend, state_key, variables, outputs, scan_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project_id) DO UPDATE SET backend = EXCLUDED.backend, state_key = EXCLUDED.state_key,
			variables = EXCLUDED.variables, outputs = EXCLUDED.outputs, scan_id = EXCLUDED.scan_id`,
		projectID, marker.Backend, marker.StateKey, vars, outs, scanID)
	if err != nil {
		return fmt.Errorf("storing inventory: %w", err)
	}
	return nil
}

// stringMap is m, or an empty map for nil, so JSON columns never hold null.
func stringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// ProjectInventory returns what a project's scan recorded besides its resources, or an
// unscanned, empty inventory for a project whose scans haven't included any.
func (s *Store) ProjectInventory(ctx context.Context, projectID int64) (Inventory, error) {
	if err := s.mustExist(ctx, "projects", projectID); err != nil {
		return Inventory{}, err
	}
	inv := Inventory{ProjectID: projectID, Variables: []Variable{}, Outputs: []Output{}}
	err := s.pool.QueryRow(ctx, `SELECT backend, state_key, variables, outputs FROM project_inventory WHERE project_id = $1`, projectID).
		Scan(&inv.Backend, &inv.StateKey, &inv.Variables, &inv.Outputs)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return inv, nil
	case err != nil:
		return Inventory{}, fmt.Errorf("querying inventory: %w", err)
	}
	inv.Scanned = true
	return inv, nil
}

// ParseTags reads tag filters written "key=value", or just "key" for any value.
func ParseTags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	tags := map[string]string{}
	for _, v := range values {
		k, val, _ := strings.Cut(v, "=")
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, errors.New("tag must be key or key=value")
		}
		tags[k] = val
	}
	return tags, nil
}

// MaxResourcePage is the most resources one request may ask for, and DefaultResourcePage how many
// it gets without asking.
const (
	MaxResourcePage     = 5000
	DefaultResourcePage = 500
)

// ResourceFilterFromQuery reads project, repo, type, mode, enabled, tag (repeatable), q, limit,
// and offset.
func ResourceFilterFromQuery(q url.Values) (ResourceFilter, error) {
	f := ResourceFilter{Type: q.Get("type"), Mode: q.Get("mode"), Enabled: q.Get("enabled"), Query: strings.TrimSpace(q.Get("q")),
		Limit: DefaultResourcePage}
	id := func(name string, into *int64) error {
		if !q.Has(name) {
			return nil
		}
		v, err := strconv.ParseInt(q.Get(name), 10, 64)
		if err != nil || v <= 0 {
			return fmt.Errorf("%s must be a positive integer", name)
		}
		*into = v
		return nil
	}
	if err := id("project", &f.ProjectID); err != nil {
		return f, err
	}
	if err := id("repo", &f.RepoID); err != nil {
		return f, err
	}
	if f.Mode != "" && f.Mode != "managed" && f.Mode != "data" {
		return f, errors.New("mode must be managed or data")
	}
	if f.Enabled != "" && f.Enabled != "true" && f.Enabled != "false" && f.Enabled != "unknown" {
		return f, errors.New("enabled must be true, false, or unknown")
	}
	var err error
	if f.Tags, err = ParseTags(q["tag"]); err != nil {
		return f, err
	}
	if q.Has("limit") {
		f.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || f.Limit < 1 || f.Limit > MaxResourcePage {
			return f, fmt.Errorf("limit must be between 1 and %d", MaxResourcePage)
		}
	}
	if q.Has("offset") {
		f.Offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || f.Offset < 0 {
			return f, errors.New("offset must be 0 or more")
		}
	}
	return f, nil
}
