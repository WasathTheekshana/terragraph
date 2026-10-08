package store

import (
	"context"
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/report"
)

func resource(address, typ, name, mode string, attrs, tags map[string]string) report.Fact {
	return report.Fact{Type: report.FactTypeResource, Address: address, ResourceType: typ, Name: name, Mode: mode, Enabled: report.EnabledTrue,
		Attrs: attrs, Tags: tags, File: "main.tf", Line: 3}
}

func inventoryScan(path string, facts ...report.Fact) report.Report {
	r := projectScan("https://github.com/org/infra.git", "main", t0, report.Fact{CallName: "vpc", Source: "./vpc"})
	r.Subject.Path = path
	r.Facts = append(r.Facts, report.Fact{Type: report.FactTypeInventory, Backend: "azurerm", StateKey: "azurerm:st/tf/" + path})
	r.Facts = append(r.Facts, facts...)
	return r
}

func TestInventoryIsStoredAndFiltered(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	off := 0
	fw := resource("azurerm_firewall.main", "azurerm_firewall", "main", report.ModeManaged, map[string]string{"name": "fw-dev"}, nil)
	fw.Enabled, fw.Instances, fw.Condition = report.EnabledFalse, &off, "count = var.fw ? 1 : 0"
	dev := inventoryScan("envs/dev",
		resource("azurerm_virtual_network.main", "azurerm_virtual_network", "main", report.ModeManaged,
			map[string]string{"name": "vnet-dev", "resource_group_name": "rg-dev"}, map[string]string{"env": "dev", "owner": "net"}),
		fw,
		resource("data.azurerm_key_vault.kv", "azurerm_key_vault", "kv", report.ModeData, map[string]string{"name": "kv-shared"}, nil),
		report.Fact{Type: report.FactTypeVariable, Name: "fw", VarType: "bool", Value: "false", ValueSource: "default", Flag: true, Controls: []string{"azurerm_firewall.main"}},
		report.Fact{Type: report.FactTypeOutput, Name: "vnet_id", Expr: "azurerm_virtual_network.main.id"},
	)
	mustIngest(t, s, dev, true)
	prod := inventoryScan("envs/prod",
		resource("azurerm_virtual_network.main", "azurerm_virtual_network", "main", report.ModeManaged,
			map[string]string{"name": "vnet-prod"}, map[string]string{"env": "prod"}))
	mustIngest(t, s, prod, true)

	all, err := s.ListResources(ctx, ResourceFilter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("resources = %d, %v", len(all), err)
	}
	if r := all[0]; r.ProjectPath != "envs/dev" || r.Address != "azurerm_firewall.main" || r.Enabled != "false" || r.Instances == nil || *r.Instances != 0 || r.Condition == "" {
		t.Errorf("first = %+v", r)
	}

	count := func(f ResourceFilter) int {
		t.Helper()
		got, err := s.ListResources(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	for name, c := range map[string]struct {
		f    ResourceFilter
		want int
	}{
		"by type":           {ResourceFilter{Type: "azurerm_virtual_network"}, 2},
		"data sources":      {ResourceFilter{Mode: "data"}, 1},
		"managed only":      {ResourceFilter{Mode: "managed"}, 3},
		"switched off":      {ResourceFilter{Enabled: "false"}, 1},
		"tag value":         {ResourceFilter{Tags: map[string]string{"env": "prod"}}, 1},
		"tag key":           {ResourceFilter{Tags: map[string]string{"owner": ""}}, 1},
		"two tags":          {ResourceFilter{Tags: map[string]string{"env": "dev", "owner": "net"}}, 1},
		"no such tag":       {ResourceFilter{Tags: map[string]string{"env": "qa"}}, 0},
		"search by name":    {ResourceFilter{Query: "VNET-pr"}, 1},
		"search an address": {ResourceFilter{Query: "firewall"}, 1},
		"search is literal": {ResourceFilter{Query: "%"}, 0},
		"one project":       {ResourceFilter{ProjectID: all[0].ProjectID}, 3},
		"one repo":          {ResourceFilter{RepoID: all[0].RepoID}, 4},
	} {
		if got := count(c.f); got != c.want {
			t.Errorf("%s: %d resources, want %d", name, got, c.want)
		}
	}

	inv, err := s.ListInventories(ctx)
	if err != nil || len(inv) != 2 {
		t.Fatalf("inventories = %v, %v", inv, err)
	}
	d := inv[all[0].ProjectID]
	if d.Backend != "azurerm" || d.StateKey != "azurerm:st/tf/envs/dev" || len(d.Variables) != 1 || !d.Variables[0].Flag ||
		d.Variables[0].Controls[0] != "azurerm_firewall.main" || len(d.Outputs) != 1 {
		t.Errorf("inventory = %+v", d)
	}
}

func TestAScanReplacesWhatWasThereButAnOldScannerLeavesItAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	vnet := resource("azurerm_virtual_network.main", "azurerm_virtual_network", "main", report.ModeManaged, map[string]string{"name": "v"}, nil)
	subnet := resource("azurerm_subnet.a", "azurerm_subnet", "a", report.ModeManaged, map[string]string{"name": "s"}, nil)
	mustIngest(t, s, inventoryScan("envs/dev", vnet, subnet), true)

	// The subnet is removed from the code.
	later := inventoryScan("envs/dev", vnet)
	later.GeneratedAt = t0.Add(1)
	mustIngest(t, s, later, true)
	if got, _ := s.ListResources(ctx, ResourceFilter{}); len(got) != 1 || got[0].Type != "azurerm_virtual_network" {
		t.Errorf("after a rescan = %+v", got)
	}

	// A scanner that doesn't know about resources sends none of the inventory facts.
	old := projectScan("https://github.com/org/infra.git", "main", t0.Add(2), report.Fact{CallName: "vpc", Source: "./vpc"})
	old.Subject.Path = "envs/dev"
	mustIngest(t, s, old, true)
	if got, _ := s.ListResources(ctx, ResourceFilter{}); len(got) != 1 {
		t.Errorf("an old scanner wiped the inventory: %+v", got)
	}

	// A scan that finds nothing is not the same: the project now creates nothing.
	empty := inventoryScan("envs/dev")
	empty.GeneratedAt = t0.Add(3)
	mustIngest(t, s, empty, true)
	if got, _ := s.ListResources(ctx, ResourceFilter{}); len(got) != 0 {
		t.Errorf("resources left after a scan that found none: %+v", got)
	}
}

func TestUntrackedAndStaleScansDoNotTouchTheInventory(t *testing.T) {
	s := newTestStore(t)
	vnet := resource("azurerm_virtual_network.main", "azurerm_virtual_network", "main", report.ModeManaged, map[string]string{"name": "v"}, nil)
	mustIngest(t, s, inventoryScan("envs/dev", vnet), true)

	branch := inventoryScan("envs/dev")
	branch.Subject.Branch = "feature"
	mustIngest(t, s, branch, false)
	stale := inventoryScan("envs/dev")
	stale.GeneratedAt = t0.Add(-1)
	mustIngest(t, s, stale, true)

	if got, _ := s.ListResources(context.Background(), ResourceFilter{}); len(got) != 1 {
		t.Errorf("resources = %+v, want the tracked, newest scan's", got)
	}
}

func TestInventoryFollowsTheProjectWhenItIsDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mustIngest(t, s, inventoryScan("envs/dev", resource("a.b", "a", "b", report.ModeManaged, nil, nil)), true)
	if _, err := s.pool.Exec(ctx, `DELETE FROM projects`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM resources) + (SELECT count(*) FROM project_inventory)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left = %d, %v", n, err)
	}
}

func TestResourcePagingCountsAndFacets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mustIngest(t, s, inventoryScan("envs/dev",
		resource("azurerm_virtual_network.a", "azurerm_virtual_network", "a", report.ModeManaged, map[string]string{"name": "va"}, map[string]string{"env": "dev", "owner": "net"}),
		resource("azurerm_virtual_network.b", "azurerm_virtual_network", "b", report.ModeManaged, map[string]string{"name": "vb"}, map[string]string{"env": "dev"}),
		resource("azurerm_subnet.a", "azurerm_subnet", "a", report.ModeManaged, map[string]string{"name": "sa"}, map[string]string{"env": "prod"}),
	), true)

	page, err := s.ListResources(ctx, ResourceFilter{Limit: 2})
	if err != nil || len(page) != 2 || page[0].Address != "azurerm_subnet.a" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, _ = s.ListResources(ctx, ResourceFilter{Limit: 2, Offset: 2})
	if len(page) != 1 {
		t.Errorf("second page = %+v", page)
	}
	if n, err := s.CountResources(ctx, ResourceFilter{Limit: 1, Type: "azurerm_virtual_network"}); err != nil || n != 2 {
		t.Errorf("count = %d, %v; want it to ignore paging", n, err)
	}

	f, err := s.ResourceFacets(ctx, ResourceFilter{Type: "azurerm_virtual_network", Tags: map[string]string{"env": ""}})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, c := range f.Types {
		types[c.Value] = c.Count
	}
	if types["azurerm_virtual_network"] != 2 || types["azurerm_subnet"] != 1 {
		t.Errorf("types = %+v, want the others still counted while one type is chosen", f.Types)
	}
	keys := map[string]int{}
	for _, c := range f.TagKeys {
		keys[c.Value] = c.Count
	}
	if keys["env"] != 2 || keys["owner"] != 1 {
		t.Errorf("tag keys = %+v", f.TagKeys)
	}
	if f.TagKey != "env" || len(f.TagValues) != 1 || f.TagValues[0].Value != "dev" || f.TagValues[0].Count != 2 {
		t.Errorf("tag values = %q %+v", f.TagKey, f.TagValues)
	}

	empty, err := s.ResourceFacets(ctx, ResourceFilter{Type: "nothing"})
	if err != nil || empty.Types == nil || empty.TagKeys == nil {
		t.Errorf("facets of nothing = %+v, %v", empty, err)
	}
}
