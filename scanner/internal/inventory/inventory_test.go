package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func localModules(callerDir, _ string, source string) string {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return filepath.Join(callerDir, filepath.FromSlash(source))
	}
	return ""
}

func collect(t *testing.T, files map[string]string) Inventory {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, files)
	inv, err := Collect(dir, Options{ModuleDir: localModules})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func find(t *testing.T, inv Inventory, address string) Resource {
	t.Helper()
	for _, r := range inv.Resources {
		if r.Address == address {
			return r
		}
	}
	var have []string
	for _, r := range inv.Resources {
		have = append(have, r.Address)
	}
	t.Fatalf("no resource %s; have %v", address, have)
	return Resource{}
}

func variable(t *testing.T, inv Inventory, path, name string) Variable {
	t.Helper()
	for _, v := range inv.Variables {
		if v.ModulePath == path && v.Name == name {
			return v
		}
	}
	t.Fatalf("no variable %s in %q", name, path)
	return Variable{}
}

const envMain = `
variable "environment" { type = string }
variable "enable_firewall" {
  type    = bool
  default = false
}
variable "db_password" {
  type    = string
  default = "hunter2"
}
variable "api_token" {
  type      = string
  sensitive = true
  default   = "abc"
}

locals {
  prefix = "acme-${var.environment}"
  tags = {
    environment = var.environment
    owner       = "platform"
  }
}

resource "azurerm_resource_group" "main" {
  name     = "rg-${local.prefix}"
  location = "westeurope"
  tags     = merge(local.tags, { tier = "core" })
}

resource "azurerm_virtual_network" "main" {
  name                = "vnet-${local.prefix}"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  address_space       = ["10.20.0.0/16"]
  tags                = local.tags
}

resource "azurerm_firewall" "main" {
  count = var.enable_firewall ? 1 : 0
  name  = "fw-${local.prefix}"
  tags  = local.tags
}

data "azurerm_key_vault" "shared" {
  name                = "kv-shared"
  resource_group_name = "rg-platform"
}

data "terraform_remote_state" "network" {
  backend = "azurerm"
  config = {
    storage_account_name = "sttfstate"
    container_name       = "tfstate"
    key                  = "network/${var.environment}.tfstate"
  }
}

output "vnet_id" { value = azurerm_virtual_network.main.id }
output "secret" {
  value     = var.db_password
  sensitive = true
}

terraform {
  backend "azurerm" {
    storage_account_name = "sttfstate"
    container_name       = "tfstate"
    key                  = "app/dev.tfstate"
  }
}
`

func TestResourcesAreResolvedThroughVariablesLocalsAndTags(t *testing.T) {
	inv := collect(t, map[string]string{"main.tf": envMain, "terraform.tfvars": `environment = "dev"`})

	rg := find(t, inv, "azurerm_resource_group.main")
	if rg.Mode != ModeManaged || rg.Attrs["name"] != "rg-acme-dev" || rg.Attrs["location"] != "westeurope" || rg.Enabled != EnabledTrue {
		t.Errorf("resource group = %+v", rg)
	}
	if rg.Tags["environment"] != "dev" || rg.Tags["owner"] != "platform" || rg.Tags["tier"] != "core" || rg.TagsUnresolved != nil {
		t.Errorf("tags = %v, unresolved %v", rg.Tags, rg.TagsUnresolved)
	}

	vnet := find(t, inv, "azurerm_virtual_network.main")
	if vnet.Attrs["name"] != "vnet-acme-dev" || vnet.Attrs["address_space"] != `["10.20.0.0/16"]` {
		t.Errorf("vnet attrs = %v", vnet.Attrs)
	}
	if vnet.Attrs["resource_group_name"] != "rg-acme-dev" || vnet.Attrs["location"] != "westeurope" || len(vnet.Unresolved) != 0 {
		t.Errorf("a reference to another resource's name should resolve to what it is: attrs %v, unresolved %v", vnet.Attrs, vnet.Unresolved)
	}
	if vnet.File != "main.tf" || vnet.Line == 0 {
		t.Errorf("position = %s:%d", vnet.File, vnet.Line)
	}
}

func TestACountSwitchedOffByAVariableIsDeclaredButNotEnabled(t *testing.T) {
	off := collect(t, map[string]string{"main.tf": envMain, "terraform.tfvars": `environment = "dev"`})
	fw := find(t, off, "azurerm_firewall.main")
	if fw.Enabled != EnabledFalse || fw.Instances == nil || *fw.Instances != 0 || fw.Condition != "count = var.enable_firewall ? 1 : 0" {
		t.Errorf("firewall = %+v", fw)
	}

	on := collect(t, map[string]string{"main.tf": envMain, "terraform.tfvars": "environment = \"prod\"\nenable_firewall = true\n"})
	fw = find(t, on, "azurerm_firewall.main")
	if fw.Enabled != EnabledTrue || *fw.Instances != 1 {
		t.Errorf("firewall = %+v", fw)
	}

	flag := variable(t, on, "", "enable_firewall")
	if !flag.Flag || flag.Value != "true" || flag.Source != "tfvars" || len(flag.Controls) != 1 || flag.Controls[0] != "azurerm_firewall.main" {
		t.Errorf("flag = %+v", flag)
	}
	if v := variable(t, off, "", "enable_firewall"); v.Value != "false" || v.Source != "default" {
		t.Errorf("default flag = %+v", v)
	}
}

func TestACountThatDependsOnTheUnknownIsUnknown(t *testing.T) {
	inv := collect(t, map[string]string{"main.tf": `
variable "enabled" { type = bool }
resource "aws_instance" "a" { count = var.enabled ? 1 : 0 }
resource "aws_instance" "b" { for_each = toset(["x", "y"]) }
resource "aws_instance" "c" { count = length(aws_instance.b) }
`})
	if a := find(t, inv, "aws_instance.a"); a.Enabled != EnabledUnknown || a.Instances != nil {
		t.Errorf("a = %+v", a)
	}
	if b := find(t, inv, "aws_instance.b"); b.Enabled != EnabledTrue || *b.Instances != 2 {
		t.Errorf("b = %+v", b)
	}
	if c := find(t, inv, "aws_instance.c"); c.Enabled != EnabledUnknown {
		t.Errorf("c = %+v", c)
	}
}

func TestSecretsAreNeverRecorded(t *testing.T) {
	inv := collect(t, map[string]string{"main.tf": envMain, "terraform.tfvars": `environment = "dev"`})
	for _, name := range []string{"db_password", "api_token"} {
		v := variable(t, inv, "", name)
		if v.Value != "" || !v.Sensitive || v.Flag {
			t.Errorf("%s = %+v, want hidden", name, v)
		}
	}
	for _, o := range inv.Outputs {
		if o.Name == "secret" && (o.Expr != "" || !o.Sensitive) {
			t.Errorf("sensitive output = %+v", o)
		}
	}
	// Attributes outside the recorded set are not read at all.
	inv = collect(t, map[string]string{"main.tf": `
resource "azurerm_mssql_server" "s" {
  name                         = "sql-a"
  administrator_login_password = "hunter2"
}`})
	r := find(t, inv, "azurerm_mssql_server.s")
	for k, v := range r.Attrs {
		if strings.Contains(v, "hunter2") {
			t.Errorf("attribute %s = %q", k, v)
		}
	}
	if _, ok := r.Unresolved["administrator_login_password"]; ok || len(r.Attrs) != 1 {
		t.Errorf("attrs = %v, unresolved = %v", r.Attrs, r.Unresolved)
	}
}

func TestDataSourcesAndWhereStateLives(t *testing.T) {
	inv := collect(t, map[string]string{"main.tf": envMain, "terraform.tfvars": `environment = "dev"`})

	kv := find(t, inv, "data.azurerm_key_vault.shared")
	if kv.Mode != ModeData || kv.Attrs["name"] != "kv-shared" || kv.Attrs["resource_group_name"] != "rg-platform" {
		t.Errorf("key vault lookup = %+v", kv)
	}
	rs := find(t, inv, "data.terraform_remote_state.network")
	if rs.Attrs["state_key"] != "azurerm:sttfstate/tfstate/network/dev.tfstate" || rs.Attrs["backend"] != "azurerm" {
		t.Errorf("remote state = %v", rs.Attrs)
	}
	if inv.Backend != "azurerm" || inv.StateKey != "azurerm:sttfstate/tfstate/app/dev.tfstate" {
		t.Errorf("state = %s %s", inv.Backend, inv.StateKey)
	}
}

func TestHCPTerraformStateKeysMatchBetweenCloudAndRemoteState(t *testing.T) {
	a := collect(t, map[string]string{"main.tf": `
terraform {
  cloud {
    organization = "Acme"
    workspaces { name = "network-prod" }
  }
}`})
	b := collect(t, map[string]string{"main.tf": `
data "terraform_remote_state" "net" {
  backend = "remote"
  config = {
    organization = "acme"
    workspaces = { name = "network-prod" }
  }
}`})
	if a.StateKey == "" || a.StateKey != find(t, b, "data.terraform_remote_state.net").Attrs["state_key"] {
		t.Errorf("cloud %q, remote_state %q", a.StateKey, find(t, b, "data.terraform_remote_state.net").Attrs["state_key"])
	}
}

func TestAwsStyleLookupsByTag(t *testing.T) {
	inv := collect(t, map[string]string{"main.tf": `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags       = { Name = "shared-vpc", Team = "net" }
}
data "aws_vpc" "shared" {
  filter {
    name   = "tag:Name"
    values = ["shared-vpc"]
  }
}`})
	d := find(t, inv, "data.aws_vpc.shared")
	if d.Attrs["tag.Name"] != "shared-vpc" {
		t.Errorf("attrs = %v", d.Attrs)
	}
	if find(t, inv, "aws_vpc.main").Tags["Name"] != "shared-vpc" {
		t.Error("the tag a lookup would match is missing")
	}
}

func TestLocalModulesGetTheirInputsAndAddresses(t *testing.T) {
	inv := collect(t, map[string]string{
		"main.tf": `
variable "env" { default = "qa" }
module "net" {
  source = "./modules/net"
  name   = "vnet-${var.env}"
  create_nat = false
  tags   = { environment = var.env }
}
module "off" {
  count  = 0
  source = "./modules/net"
  name   = "x"
}
module "remote" {
  source = "git::https://example.com/x.git?ref=v1"
}
`,
		"modules/net/main.tf": `
variable "name" {}
variable "create_nat" { default = true }
variable "tags" { default = {} }
resource "azurerm_virtual_network" "this" {
  name = var.name
  tags = var.tags
}
resource "azurerm_nat_gateway" "this" {
  count = var.create_nat ? 1 : 0
  name  = "nat-${var.name}"
}
output "id" { value = azurerm_virtual_network.this.id }
`,
	})
	vnet := find(t, inv, "module.net.azurerm_virtual_network.this")
	if vnet.ModulePath != "net" || vnet.Attrs["name"] != "vnet-qa" || vnet.Tags["environment"] != "qa" {
		t.Errorf("vnet = %+v", vnet)
	}
	if nat := find(t, inv, "module.net.azurerm_nat_gateway.this"); nat.Enabled != EnabledFalse {
		t.Errorf("a feature switched off by the caller = %+v", nat)
	}
	if v := variable(t, inv, "net", "create_nat"); v.Source != "input" || v.Value != "false" || !v.Flag {
		t.Errorf("module flag = %+v", v)
	}
	if off := find(t, inv, "module.off.azurerm_virtual_network.this"); off.Enabled != EnabledFalse {
		t.Errorf("a module with count = 0 holds nothing that is enabled: %+v", off)
	}
	for _, r := range inv.Resources {
		if strings.Contains(r.Address, "remote") {
			t.Errorf("a module that isn't on disk has nothing to read: %s", r.Address)
		}
	}
}

func TestModuleCyclesAndDepthAreBounded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"main.tf": `module "a" { source = "./loop" }`,
		"loop/main.tf": `module "again" { source = "../loop" }
resource "null_resource" "x" {}`,
	})
	inv, err := Collect(dir, Options{ModuleDir: localModules})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Resources) == 0 || len(inv.Resources) > 12 {
		t.Errorf("%d resources", len(inv.Resources))
	}
}

func TestUnreadableTagsSayWhy(t *testing.T) {
	inv := collect(t, map[string]string{"main.tf": `
variable "extra" {}
resource "azurerm_storage_account" "a" {
  name = "st1"
  tags = merge(var.extra, { team = "x", cost = data.azurerm_x.y.code })
}
resource "azurerm_storage_account" "b" {
  name = "st2"
  tags = module.tags.all
}`})
	a := find(t, inv, "azurerm_storage_account.a")
	if a.Tags["team"] != "x" || a.TagsUnresolved["cost"] != "data.azurerm_x.y.code" || a.TagsUnresolved["*"] == "" {
		t.Errorf("a: tags %v, unresolved %v", a.Tags, a.TagsUnresolved)
	}
	b := find(t, inv, "azurerm_storage_account.b")
	if len(b.Tags) != 0 || b.TagsUnresolved["*"] != "module.tags.all" {
		t.Errorf("b: tags %v, unresolved %v", b.Tags, b.TagsUnresolved)
	}
}

func TestAnUnparsableFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"main.tf": `resource "a" "b" {`})
	if _, err := Collect(dir, Options{}); err == nil {
		t.Error("want an error")
	}
}

func TestStateKey(t *testing.T) {
	cases := []struct {
		backend string
		cfg     map[string]string
		want    string
	}{
		{"s3", map[string]string{"bucket": "B", "key": "a/b.tfstate"}, "s3:b/a/b.tfstate"},
		{"gcs", map[string]string{"bucket": "b", "prefix": "p"}, "gcs:b/p"},
		{"remote", map[string]string{"organization": "o", "workspace": "w"}, "cloud:o/w"},
		{"local", map[string]string{"path": "x"}, ""},
		{"azurerm", map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := StateKey(c.backend, c.cfg); got != c.want {
			t.Errorf("StateKey(%s, %v) = %q, want %q", c.backend, c.cfg, got, c.want)
		}
	}
}

func TestReferencesAreOnlyResolvedWithinTheModuleAndOnlyWhenTheyAreKnown(t *testing.T) {
	inv := collect(t, map[string]string{
		"main.tf": `
variable "x" {}
resource "azurerm_resource_group" "a" { name = var.x }
resource "azurerm_resource_group" "b" { name = "rg-b" }
resource "azurerm_virtual_network" "v" {
  name                = "v1"
  resource_group_name = azurerm_resource_group.a.name
  location            = azurerm_resource_group.b.location
}
resource "azurerm_subnet" "s" {
  name                = "s1"
  resource_group_name = azurerm_resource_group.b.name
  virtual_network_name = azurerm_virtual_network.v.name
}
resource "azurerm_subnet" "t" {
  name                = "t1"
  resource_group_name = module.m.rg
}
module "m" { source = "./m" }
`,
		"m/main.tf": `resource "azurerm_resource_group" "a" { name = "rg-in-module" }`,
	})
	v := find(t, inv, "azurerm_virtual_network.v")
	if v.Unresolved["resource_group_name"] != "azurerm_resource_group.a.name" || v.Attrs["resource_group_name"] != "" {
		t.Errorf("a reference to something unknown stays unresolved: %v %v", v.Attrs, v.Unresolved)
	}
	if v.Unresolved["location"] != "azurerm_resource_group.b.location" {
		t.Errorf("an attribute the target doesn't have stays unresolved: %v", v.Unresolved)
	}
	if s := find(t, inv, "azurerm_subnet.s"); s.Attrs["resource_group_name"] != "rg-b" {
		t.Errorf("subnet = %+v", s)
	}
	if ts := find(t, inv, "azurerm_subnet.t"); ts.Unresolved["resource_group_name"] != "module.m.rg" {
		t.Errorf("a reference to a module output stays unresolved: %v", ts.Unresolved)
	}
}
