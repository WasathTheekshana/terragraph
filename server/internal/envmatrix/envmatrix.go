// Package envmatrix compares the Terraform projects of one repository, which are usually its
// environments: which resources each one creates, which feature switches are on, how the other
// settings differ, and which tags each puts on things.
package envmatrix

import (
	"slices"
	"sort"
	"strings"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

// Resource states in a cell.
const (
	StateEnabled  = "enabled"
	StateDisabled = "disabled"
	StateUnknown  = "unknown"
	StateAbsent   = "absent"
)

type Input struct {
	// Projects are the environments to compare, in the order to show them.
	Projects    []store.Project
	Resources   []store.Resource
	Inventories map[int64]store.Inventory
}

type Matrix struct {
	Environments []Environment `json:"environments"`
	Resources    []ResourceRow `json:"resources"`
	// Flags are the variables that switch things on and off.
	Flags []VariableRow `json:"flags"`
	// Settings are the other variables whose values are known.
	Settings []VariableRow `json:"settings"`
	Tags     []TagRow      `json:"tags"`
	Summary  Summary       `json:"summary"`
}

type Environment struct {
	ProjectID int64  `json:"project_id"`
	Path      string `json:"path"`
	// Scanned is false when the project has no resource inventory yet.
	Scanned   bool `json:"scanned"`
	Resources int  `json:"resources"`
	Disabled  int  `json:"disabled"`
}

type ResourceRow struct {
	Address    string         `json:"address"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Mode       string         `json:"mode"`
	ModulePath string         `json:"module_path,omitempty"`
	Cells      []ResourceCell `json:"cells"`
	// Differs is true when the environments don't all agree on whether it is created.
	Differs bool `json:"differs"`
}

type ResourceCell struct {
	State string `json:"state"`
	// Name is the name it is given there, which usually carries the environment.
	Name      string `json:"name,omitempty"`
	Instances *int   `json:"instances,omitempty"`
	// Condition is the count or for_each that decides it.
	Condition string `json:"condition,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
}

type VariableRow struct {
	ModulePath string `json:"module_path,omitempty"`
	Name       string `json:"name"`
	Type       string `json:"type,omitempty"`
	// Controls are the resources whose count depends on it, in any environment.
	Controls []string       `json:"controls,omitempty"`
	Cells    []VariableCell `json:"cells"`
	Differs  bool           `json:"differs"`
}

type VariableCell struct {
	Present bool   `json:"present"`
	Value   string `json:"value,omitempty"`
	// Source is where the value comes from: default, tfvars, input, or unset.
	Source string `json:"source,omitempty"`
}

type TagRow struct {
	Key   string    `json:"key"`
	Cells []TagCell `json:"cells"`
	// Differs is true when the key is on resources in some environments and not in others.
	Differs bool `json:"differs"`
	// Varies is true when the environments use different values.
	Varies bool `json:"varies"`
}

type TagCell struct {
	// Values are the distinct values the key has on resources there.
	Values []string `json:"values,omitempty"`
	// Resources counts the resources that carry the key.
	Resources int `json:"resources"`
}

type Summary struct {
	Resources int `json:"resources"`
	Flags     int `json:"flags"`
	Settings  int `json:"settings"`
	Tags      int `json:"tags"`
}

// Compare lines up the environments' resources, switches, settings, and tags.
func Compare(in Input) Matrix {
	m := Matrix{Environments: []Environment{}, Resources: []ResourceRow{}, Flags: []VariableRow{}, Settings: []VariableRow{}, Tags: []TagRow{}}
	index := map[int64]int{}
	for i, p := range in.Projects {
		index[p.ID] = i
		_, scanned := in.Inventories[p.ID]
		m.Environments = append(m.Environments, Environment{ProjectID: p.ID, Path: p.Path, Scanned: scanned})
	}
	n := len(in.Projects)

	rows := map[string]*ResourceRow{}
	var order []string
	for _, r := range in.Resources {
		i, ok := index[r.ProjectID]
		if !ok {
			continue
		}
		row := rows[r.Address]
		if row == nil {
			row = &ResourceRow{Address: r.Address, Type: r.Type, Name: r.Name, Mode: r.Mode, ModulePath: r.ModulePath, Cells: absent(n)}
			rows[r.Address] = row
			order = append(order, r.Address)
		}
		row.Cells[i] = ResourceCell{State: state(r.Enabled), Name: r.Attrs["name"], Instances: r.Instances, Condition: r.Condition, File: r.File, Line: r.Line}
		m.Environments[i].Resources++
		if r.Enabled == "false" {
			m.Environments[i].Disabled++
		}
	}
	sort.Strings(order)
	for _, addr := range order {
		row := rows[addr]
		row.Differs = statesDiffer(row.Cells)
		if row.Differs {
			m.Summary.Resources++
		}
		m.Resources = append(m.Resources, *row)
	}

	m.Flags, m.Settings = variableRows(in, index, n)
	for _, row := range m.Flags {
		if row.Differs {
			m.Summary.Flags++
		}
	}
	for _, row := range m.Settings {
		if row.Differs {
			m.Summary.Settings++
		}
	}
	m.Tags = tagRows(in, index, n)
	for _, row := range m.Tags {
		if row.Differs {
			m.Summary.Tags++
		}
	}
	return m
}

func absent(n int) []ResourceCell {
	cells := make([]ResourceCell, n)
	for i := range cells {
		cells[i].State = StateAbsent
	}
	return cells
}

func state(enabled string) string {
	switch enabled {
	case "true":
		return StateEnabled
	case "false":
		return StateDisabled
	}
	return StateUnknown
}

// statesDiffer reports whether the environments don't all have the same state.
func statesDiffer(cells []ResourceCell) bool {
	for _, c := range cells[1:] {
		if c.State != cells[0].State {
			return true
		}
	}
	return false
}

func variableRows(in Input, index map[int64]int, n int) (flags, settings []VariableRow) {
	type key struct{ module, name string }
	rows := map[key]*VariableRow{}
	var order []key
	flagged := map[key]bool{}
	for id, inv := range in.Inventories {
		i, ok := index[id]
		if !ok {
			continue
		}
		for _, v := range inv.Variables {
			if v.Sensitive {
				continue
			}
			k := key{v.ModulePath, v.Name}
			row := rows[k]
			if row == nil {
				row = &VariableRow{ModulePath: v.ModulePath, Name: v.Name, Type: v.Type, Cells: make([]VariableCell, n)}
				rows[k] = row
				order = append(order, k)
			}
			row.Cells[i] = VariableCell{Present: true, Value: v.Value, Source: v.Source}
			row.Controls = union(row.Controls, v.Controls)
			if v.Flag {
				row.Type = firstNonEmpty(row.Type, v.Type)
			}
			if v.Flag && row.Type == "" {
				row.Type = "bool"
			}
			flagged[k] = flagged[k] || v.Flag
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].module != order[j].module {
			return order[i].module < order[j].module
		}
		return order[i].name < order[j].name
	})
	for _, k := range order {
		row := rows[k]
		row.Differs = variablesDiffer(row.Cells)
		if flagged[k] {
			flags = append(flags, *row)
		} else if anyValue(row.Cells) {
			settings = append(settings, *row)
		}
	}
	if flags == nil {
		flags = []VariableRow{}
	}
	if settings == nil {
		settings = []VariableRow{}
	}
	return flags, settings
}

// variablesDiffer reports whether the environments differ in having the variable or in its value.
func variablesDiffer(cells []VariableCell) bool {
	for _, c := range cells[1:] {
		if c.Present != cells[0].Present || c.Value != cells[0].Value {
			return true
		}
	}
	return false
}

func anyValue(cells []VariableCell) bool {
	for _, c := range cells {
		if c.Value != "" {
			return true
		}
	}
	return false
}

func tagRows(in Input, index map[int64]int, n int) []TagRow {
	type acc struct {
		values    []map[string]bool
		resources []int
	}
	keys := map[string]*acc{}
	for _, r := range in.Resources {
		i, ok := index[r.ProjectID]
		if !ok || r.Mode != "managed" {
			continue
		}
		for k, v := range r.Tags {
			a := keys[k]
			if a == nil {
				a = &acc{values: make([]map[string]bool, n), resources: make([]int, n)}
				keys[k] = a
			}
			if a.values[i] == nil {
				a.values[i] = map[string]bool{}
			}
			a.values[i][v] = true
			a.resources[i]++
		}
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	rows := []TagRow{}
	for _, k := range names {
		a := keys[k]
		row := TagRow{Key: k, Cells: make([]TagCell, n)}
		for i := range row.Cells {
			row.Cells[i].Resources = a.resources[i]
			for v := range a.values[i] {
				row.Cells[i].Values = append(row.Cells[i].Values, v)
			}
			sort.Strings(row.Cells[i].Values)
		}
		for _, c := range row.Cells[1:] {
			if (c.Resources > 0) != (row.Cells[0].Resources > 0) {
				row.Differs = true
			}
			if !slices.Equal(c.Values, row.Cells[0].Values) {
				row.Varies = true
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func union(a, b []string) []string {
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Label is a short name for an environment: its folder, or the repo root.
func Label(e Environment) string {
	if e.Path == "." || e.Path == "" {
		return "(repo root)"
	}
	return strings.TrimPrefix(e.Path, "./")
}
