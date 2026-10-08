package envmatrix

import (
	"testing"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func intp(n int) *int { return &n }

func res(project int64, address, typ, mode, enabled, name string, tags map[string]string) store.Resource {
	return store.Resource{ProjectID: project, Address: address, Type: typ, Name: address, Mode: mode, Enabled: enabled,
		Attrs: map[string]string{"name": name}, Tags: tags, File: "main.tf", Line: 7}
}

func fixture() Input {
	return Input{
		Projects: []store.Project{{ID: 1, Path: "envs/dev"}, {ID: 2, Path: "envs/staging"}, {ID: 3, Path: "envs/prod"}},
		Resources: []store.Resource{
			res(1, "azurerm_virtual_network.main", "azurerm_virtual_network", "managed", "true", "vnet-dev", map[string]string{"env": "dev", "owner": "net"}),
			res(2, "azurerm_virtual_network.main", "azurerm_virtual_network", "managed", "true", "vnet-stg", map[string]string{"env": "staging", "owner": "net"}),
			res(3, "azurerm_virtual_network.main", "azurerm_virtual_network", "managed", "true", "vnet-prod", map[string]string{"env": "prod", "owner": "net", "cost_center": "42"}),
			// The firewall is off in dev, on in prod, and not declared in staging.
			res(1, "azurerm_firewall.main", "azurerm_firewall", "managed", "false", "fw-dev", nil),
			res(3, "azurerm_firewall.main", "azurerm_firewall", "managed", "true", "fw-prod", nil),
			// DNS is the same everywhere.
			res(1, "azurerm_private_dns_zone.z", "azurerm_private_dns_zone", "managed", "true", "z", nil),
			res(2, "azurerm_private_dns_zone.z", "azurerm_private_dns_zone", "managed", "true", "z", nil),
			res(3, "azurerm_private_dns_zone.z", "azurerm_private_dns_zone", "managed", "true", "z", nil),
			// Something that can't be told.
			res(1, "azurerm_x.y", "azurerm_x", "managed", "unknown", "", nil),
			res(2, "azurerm_x.y", "azurerm_x", "managed", "true", "", nil),
			res(3, "azurerm_x.y", "azurerm_x", "managed", "true", "", nil),
		},
		Inventories: map[int64]store.Inventory{
			1: {ProjectID: 1, Variables: []store.Variable{
				{Name: "enable_firewall", Type: "bool", Value: "false", Source: "default", Flag: true, Controls: []string{"azurerm_firewall.main"}},
				{Name: "sku", Value: "Standard", Source: "tfvars"},
				{Name: "location", Value: "westeurope", Source: "default"},
				{Name: "admin_password", Sensitive: true},
			}},
			2: {ProjectID: 2, Variables: []store.Variable{
				{Name: "enable_firewall", Type: "bool", Value: "false", Source: "default", Flag: true, Controls: []string{"azurerm_firewall.main"}},
				{Name: "sku", Value: "Standard", Source: "default"},
				{Name: "location", Value: "westeurope", Source: "default"},
			}},
			3: {ProjectID: 3, Variables: []store.Variable{
				{Name: "enable_firewall", Type: "bool", Value: "true", Source: "tfvars", Flag: true, Controls: []string{"azurerm_firewall.main"}},
				{Name: "sku", Value: "Premium", Source: "tfvars"},
				{Name: "location", Value: "westeurope", Source: "default"},
				{Name: "enable_ddos", Type: "bool", Value: "true", Source: "tfvars", Flag: true},
			}},
		},
	}
}

func row(t *testing.T, m Matrix, address string) ResourceRow {
	t.Helper()
	for _, r := range m.Resources {
		if r.Address == address {
			return r
		}
	}
	t.Fatalf("no row %s", address)
	return ResourceRow{}
}

func TestEnvironmentsKeepTheOrderGiven(t *testing.T) {
	m := Compare(fixture())
	if len(m.Environments) != 3 || m.Environments[0].Path != "envs/dev" || m.Environments[2].Path != "envs/prod" {
		t.Fatalf("environments = %+v", m.Environments)
	}
	if m.Environments[0].Resources != 4 || m.Environments[0].Disabled != 1 || !m.Environments[0].Scanned {
		t.Errorf("dev = %+v", m.Environments[0])
	}
}

func TestAFeatureOnInSomeEnvironmentsOnly(t *testing.T) {
	m := Compare(fixture())
	fw := row(t, m, "azurerm_firewall.main")
	states := []string{fw.Cells[0].State, fw.Cells[1].State, fw.Cells[2].State}
	if states[0] != StateDisabled || states[1] != StateAbsent || states[2] != StateEnabled || !fw.Differs {
		t.Errorf("firewall = %v differs=%v", states, fw.Differs)
	}
	if fw.Cells[0].Name != "fw-dev" || fw.Cells[2].Name != "fw-prod" || fw.Cells[2].File != "main.tf" {
		t.Errorf("cells = %+v", fw.Cells)
	}
	if dns := row(t, m, "azurerm_private_dns_zone.z"); dns.Differs {
		t.Error("DNS is the same everywhere")
	}
	if vnet := row(t, m, "azurerm_virtual_network.main"); vnet.Differs {
		t.Error("a different name per environment isn't a difference in what is created")
	}
	if x := row(t, m, "azurerm_x.y"); !x.Differs || x.Cells[0].State != StateUnknown {
		t.Errorf("x = %+v", x)
	}
	if m.Summary.Resources != 2 {
		t.Errorf("summary = %+v", m.Summary)
	}
}

func TestInstancesAndConditionArePassedThrough(t *testing.T) {
	in := fixture()
	in.Resources[3].Instances, in.Resources[3].Condition = intp(0), "count = var.enable_firewall ? 1 : 0"
	fw := row(t, Compare(in), "azurerm_firewall.main")
	if fw.Cells[0].Instances == nil || *fw.Cells[0].Instances != 0 || fw.Cells[0].Condition != "count = var.enable_firewall ? 1 : 0" {
		t.Errorf("cell = %+v", fw.Cells[0])
	}
}

func TestFlagsAndSettingsAreSeparatedAndSensitiveOnesLeftOut(t *testing.T) {
	m := Compare(fixture())
	if len(m.Flags) != 2 || m.Flags[0].Name != "enable_ddos" || m.Flags[1].Name != "enable_firewall" {
		t.Fatalf("flags = %+v", m.Flags)
	}
	ddos := m.Flags[0]
	if ddos.Cells[0].Present || ddos.Cells[1].Present || !ddos.Cells[2].Present || ddos.Cells[2].Value != "true" || !ddos.Differs {
		t.Errorf("a flag declared in one environment = %+v", ddos)
	}
	fw := m.Flags[1]
	if !fw.Differs || fw.Cells[2].Value != "true" || fw.Cells[2].Source != "tfvars" || len(fw.Controls) != 1 {
		t.Errorf("firewall flag = %+v", fw)
	}

	if len(m.Settings) != 2 {
		t.Fatalf("settings = %+v", m.Settings)
	}
	byName := map[string]VariableRow{}
	for _, s := range m.Settings {
		byName[s.Name] = s
	}
	if !byName["sku"].Differs || byName["location"].Differs {
		t.Errorf("settings = %+v", byName)
	}
	if _, ok := byName["admin_password"]; ok {
		t.Error("a sensitive variable has no place here")
	}
	if m.Summary.Flags != 2 || m.Summary.Settings != 1 {
		t.Errorf("summary = %+v", m.Summary)
	}
}

func TestTagsShowWhereAKeyIsMissing(t *testing.T) {
	m := Compare(fixture())
	tags := map[string]TagRow{}
	for _, r := range m.Tags {
		tags[r.Key] = r
	}
	cost := tags["cost_center"]
	if !cost.Differs || cost.Cells[0].Resources != 0 || cost.Cells[2].Resources != 1 || cost.Cells[2].Values[0] != "42" {
		t.Errorf("cost_center = %+v", cost)
	}
	owner := tags["owner"]
	if owner.Differs || owner.Varies {
		t.Errorf("owner is the same everywhere: %+v", owner)
	}
	env := tags["env"]
	if env.Differs || !env.Varies {
		t.Errorf("env is on everything but with a value per environment: %+v", env)
	}
	if m.Summary.Tags != 1 {
		t.Errorf("summary = %+v", m.Summary)
	}
}

func TestDataSourcesAreNotCountedForTags(t *testing.T) {
	in := fixture()
	in.Resources = append(in.Resources, res(1, "data.azurerm_x.y", "azurerm_x", "data", "true", "n", map[string]string{"only_on_data": "x"}))
	for _, r := range Compare(in).Tags {
		if r.Key == "only_on_data" {
			t.Error("a data source doesn't carry tags")
		}
	}
}

func TestResourcesOfOtherProjectsAreIgnoredAndNothingIsNull(t *testing.T) {
	in := fixture()
	in.Resources = append(in.Resources, res(99, "other.thing", "other", "managed", "true", "x", nil))
	m := Compare(in)
	for _, r := range m.Resources {
		if r.Address == "other.thing" {
			t.Error("a project outside the comparison")
		}
	}
	e := Compare(Input{Projects: []store.Project{{ID: 1, Path: "."}}})
	if e.Resources == nil || e.Flags == nil || e.Settings == nil || e.Tags == nil || e.Environments[0].Scanned {
		t.Errorf("empty = %+v", e)
	}
	if Label(e.Environments[0]) != "(repo root)" || Label(Environment{Path: "envs/prod"}) != "envs/prod" {
		t.Error("labels")
	}
}
