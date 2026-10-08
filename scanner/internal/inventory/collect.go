package inventory

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// Options configures Collect.
type Options struct {
	// ModuleDir returns where a called module's code is on disk, or "" when it isn't
	// available (a remote module that hasn't been downloaded).
	ModuleDir func(callerDir, address, source string) string
	// MaxDepth bounds how deep calls inside modules are followed. The default is 10.
	MaxDepth int
}

// recorded are the attributes whose values are kept. They name and place a
// resource and are what a data source looks it up by. Anything else, which is
// where secrets live, is never read.
var recorded = map[string]bool{
	"name": true, "id": true, "resource_group_name": true, "location": true, "region": true,
	"availability_zone": true, "cidr_block": true, "address_space": true, "address_prefixes": true,
	"sku": true, "sku_name": true, "tier": true, "kind": true, "size": true, "instance_type": true,
	"account_tier": true, "account_replication_type": true, "engine": true, "engine_version": true,
	"bucket": true, "zone_name": true, "domain": true, "dns_name": true, "environment": true,
	"project": true, "namespace": true, "version": true,
}

var moduleMeta = map[string]bool{"source": true, "version": true, "count": true, "for_each": true, "providers": true, "depends_on": true}

type collector struct {
	rootDir string
	opts    Options
	inv     *Inventory
	onPath  map[string]bool
}

// Collect reads the Terraform root in rootDir, and the modules it calls that are
// available on disk.
func Collect(rootDir string, opts Options) (Inventory, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 10
	}
	m, err := load(rootDir)
	if err != nil {
		return Inventory{}, err
	}
	var inv Inventory
	c := &collector{rootDir: rootDir, opts: opts, inv: &inv, onPath: map[string]bool{rootDir: true}}

	vars, sources := c.variableValues(m, nil)
	f := newFrame(m, vars)
	inv.Backend, inv.StateKey = c.state(m, f)
	c.walk(m, f, sources, "", EnabledTrue, 0)
	resolveReferences(inv.Resources)

	sort.Slice(inv.Resources, func(i, j int) bool { return inv.Resources[i].Address < inv.Resources[j].Address })
	sort.Slice(inv.Variables, func(i, j int) bool {
		a, b := inv.Variables[i], inv.Variables[j]
		if a.ModulePath != b.ModulePath {
			return a.ModulePath < b.ModulePath
		}
		return a.Name < b.Name
	})
	sort.Slice(inv.Outputs, func(i, j int) bool {
		a, b := inv.Outputs[i], inv.Outputs[j]
		if a.ModulePath != b.ModulePath {
			return a.ModulePath < b.ModulePath
		}
		return a.Name < b.Name
	})
	return inv, nil
}

// variableValues works out what each variable of a module is set to: what the
// caller passed in, else terraform.tfvars for the root, else the default.
func (c *collector) variableValues(m *module, inputs map[string]cty.Value) (map[string]cty.Value, map[string]string) {
	plain := &hcl.EvalContext{Functions: functions}
	vals := map[string]cty.Value{}
	sources := map[string]string{}
	for _, b := range m.variables {
		name := b.Labels[0]
		if v, ok := inputs[name]; ok {
			vals[name], sources[name] = v, "input"
			continue
		}
		if e, ok := m.tfvars[name]; ok && inputs == nil {
			if v, diags := e.Value(plain); !diags.HasErrors() {
				vals[name], sources[name] = v, "tfvars"
				continue
			}
		}
		if a, ok := b.Body.Attributes["default"]; ok {
			if v, diags := a.Expr.Value(plain); !diags.HasErrors() {
				vals[name], sources[name] = v, "default"
				continue
			}
		}
		vals[name], sources[name] = cty.DynamicVal, "unset"
	}
	return vals, sources
}

func (c *collector) walk(m *module, f *frame, sources map[string]string, path, enabled string, depth int) {
	controls := map[string][]string{}
	control := func(e hclsyntax.Expression, address string) {
		refs := map[string]bool{}
		f.variableRefs(e, map[string]bool{}, refs)
		for name := range refs {
			controls[name] = append(controls[name], address)
		}
	}

	for _, b := range m.resources {
		r := c.resource(m, f, b, path, enabled)
		for _, key := range []string{"count", "for_each"} {
			if a, ok := b.Body.Attributes[key]; ok {
				control(a.Expr, r.Address)
			}
		}
		c.inv.Resources = append(c.inv.Resources, r)
	}

	for _, b := range m.outputs {
		o := Output{ModulePath: path, Name: b.Labels[0]}
		if a, ok := b.Body.Attributes["value"]; ok {
			o.Expr = m.text(a.Expr)
		}
		if a, ok := b.Body.Attributes["sensitive"]; ok {
			if v, ok := f.value(a.Expr); ok && v.Type() == cty.Bool && v.True() {
				o.Sensitive, o.Expr = true, ""
			}
		}
		c.inv.Outputs = append(c.inv.Outputs, o)
	}

	for _, b := range m.calls {
		c.call(m, f, b, path, enabled, depth, control)
	}

	for _, b := range m.variables {
		c.inv.Variables = append(c.inv.Variables, c.variable(f, b, path, sources, controls))
	}
}

func (c *collector) variable(f *frame, b *hclsyntax.Block, path string, sources map[string]string, controls map[string][]string) Variable {
	name := b.Labels[0]
	v := Variable{ModulePath: path, Name: name, Source: sources[name]}
	if a, ok := b.Body.Attributes["type"]; ok {
		v.Type = f.m.text(a.Expr)
	}
	if a, ok := b.Body.Attributes["sensitive"]; ok {
		if sv, ok := f.value(a.Expr); ok && sv.Type() == cty.Bool && sv.True() {
			v.Sensitive = true
		}
	}
	hidden := v.Sensitive || hasSecretName(name)
	val := f.vars[name]
	known := val.IsWhollyKnown() && !val.IsNull()
	if known && !hidden {
		v.Value = render(val)
		const limit = 300
		if len(v.Value) > limit {
			v.Value = v.Value[:limit-3] + "..."
		}
	}
	if hidden {
		v.Sensitive = true
	}
	v.Controls = dedupe(controls[name])
	isBool := v.Type == "bool" || (known && val.Type() == cty.Bool)
	v.Flag = !hidden && (isBool || len(v.Controls) > 0)
	return v
}

func (c *collector) resource(m *module, f *frame, b *hclsyntax.Block, path, enabled string) Resource {
	r := Resource{
		Mode: ModeManaged, Type: b.Labels[0], Name: b.Labels[1], ModulePath: path,
		File: m.rel(c.rootDir, b.DefRange().Filename), Line: b.DefRange().Start.Line,
		Attrs: map[string]string{}, Unresolved: map[string]string{},
	}
	r.Address = address(path, r.Type, r.Name)
	if b.Type == "data" {
		r.Mode = ModeData
		r.Address = dataAddress(path, r.Type, r.Name)
	}
	r.Enabled, r.Instances, r.Condition = c.copies(m, f, b.Body, enabled)

	attrs := b.Body.Attributes
	for _, name := range sortedKeys(attrs) {
		if !recorded[name] {
			continue
		}
		if v, ok := f.str(attrs[name].Expr); ok {
			r.Attrs[name] = v
		} else {
			r.Unresolved[name] = m.text(attrs[name].Expr)
		}
	}
	if a, ok := attrs["tags"]; ok {
		r.Tags, r.TagsUnresolved = f.tags(a.Expr, 0)
	}
	for _, blk := range b.Body.Blocks {
		if blk.Type == "filter" && r.Mode == ModeData {
			c.filter(f, blk, r.Attrs)
		}
	}
	if r.Mode == ModeData && r.Type == "terraform_remote_state" {
		c.remoteState(f, b.Body, r.Attrs)
	}
	if len(r.Attrs) == 0 {
		r.Attrs = nil
	}
	if len(r.Unresolved) == 0 {
		r.Unresolved = nil
	}
	if len(r.Tags) == 0 {
		r.Tags = nil
	}
	if len(r.TagsUnresolved) == 0 {
		r.TagsUnresolved = nil
	}
	return r
}

// copies reads count and for_each: whether the thing is created, and how many of it.
func (c *collector) copies(m *module, f *frame, body *hclsyntax.Body, parent string) (string, *int, string) {
	enabled, instances, condition := EnabledTrue, (*int)(nil), ""
	for _, key := range []string{"count", "for_each"} {
		a, ok := body.Attributes[key]
		if !ok {
			continue
		}
		condition = key + " = " + m.text(a.Expr)
		if n, ok := f.count(a.Expr, key == "for_each"); ok {
			instances = &n
			enabled = EnabledTrue
			if n == 0 {
				enabled = EnabledFalse
			}
		} else {
			enabled = EnabledUnknown
		}
	}
	return combine(parent, enabled), instances, condition
}

func combine(parent, own string) string {
	switch {
	case parent == EnabledFalse || own == EnabledFalse:
		return EnabledFalse
	case parent == EnabledUnknown || own == EnabledUnknown:
		return EnabledUnknown
	}
	return EnabledTrue
}

// filter records a data source's filter { name = "tag:Name" values = [...] } as a
// tag it looks for.
func (c *collector) filter(f *frame, b *hclsyntax.Block, attrs map[string]string) {
	n, ok := b.Body.Attributes["name"]
	if !ok {
		return
	}
	name, ok := f.str(n.Expr)
	if !ok || !strings.HasPrefix(name, "tag:") {
		return
	}
	v, ok := b.Body.Attributes["values"]
	if !ok {
		return
	}
	val, ok := f.value(v.Expr)
	if !ok || !val.CanIterateElements() || val.LengthInt() == 0 {
		return
	}
	for _, first := range val.AsValueSlice() {
		attrs["tag."+strings.TrimPrefix(name, "tag:")] = render(first)
		break
	}
}

// remoteState records where a terraform_remote_state data source reads from.
func (c *collector) remoteState(f *frame, body *hclsyntax.Body, attrs map[string]string) {
	backend := ""
	if a, ok := body.Attributes["backend"]; ok {
		backend, _ = f.str(a.Expr)
	}
	cfg := map[string]string{}
	if a, ok := body.Attributes["config"]; ok {
		if obj, ok := a.Expr.(*hclsyntax.ObjectConsExpr); ok {
			c.settings(f, obj, cfg)
		}
	}
	if key := StateKey(backend, cfg); key != "" {
		attrs["state_key"] = key
	}
	if backend != "" {
		attrs["backend"] = backend
	}
}

// settings reads the values of a backend's settings, one level deep.
func (c *collector) settings(f *frame, obj *hclsyntax.ObjectConsExpr, into map[string]string) {
	for _, item := range obj.Items {
		kv, ok := f.value(item.KeyExpr)
		if !ok || kv.IsNull() {
			continue
		}
		key := render(kv)
		if inner, ok := item.ValueExpr.(*hclsyntax.ObjectConsExpr); ok && key == "workspaces" {
			nested := map[string]string{}
			c.settings(f, inner, nested)
			if name := nested["name"]; name != "" {
				into["workspace"] = name
			}
			continue
		}
		if v, ok := f.str(item.ValueExpr); ok {
			into[key] = v
		}
	}
}

// state finds where the root keeps its state, from its backend or cloud block.
func (c *collector) state(m *module, f *frame) (backend, key string) {
	for _, tf := range m.terraform {
		for _, b := range tf.Body.Blocks {
			cfg := map[string]string{}
			for name, a := range b.Body.Attributes {
				if v, ok := f.str(a.Expr); ok {
					cfg[name] = v
				}
			}
			for _, nested := range b.Body.Blocks {
				if nested.Type != "workspaces" {
					continue
				}
				if a, ok := nested.Body.Attributes["name"]; ok {
					if v, ok := f.str(a.Expr); ok {
						cfg["workspace"] = v
					}
				}
			}
			switch b.Type {
			case "backend":
				if len(b.Labels) == 1 {
					return b.Labels[0], StateKey(b.Labels[0], cfg)
				}
			case "cloud":
				return "cloud", StateKey("cloud", cfg)
			}
		}
	}
	return "", ""
}

func (c *collector) call(m *module, f *frame, b *hclsyntax.Block, path, enabled string, depth int, control func(hclsyntax.Expression, string)) {
	name := b.Labels[0]
	addr := name
	if path != "" {
		addr = path + "." + name
	}
	own, _, _ := c.copies(m, f, b.Body, EnabledTrue)
	for _, key := range []string{"count", "for_each"} {
		if a, ok := b.Body.Attributes[key]; ok {
			control(a.Expr, "module."+strings.ReplaceAll(addr, ".", ".module."))
		}
	}

	srcAttr, ok := b.Body.Attributes["source"]
	if !ok || c.opts.ModuleDir == nil || depth+1 >= c.opts.MaxDepth {
		return
	}
	source, ok := f.str(srcAttr.Expr)
	if !ok {
		return
	}
	dir := c.opts.ModuleDir(m.dir, addr, source)
	if dir == "" {
		return
	}
	dir = filepath.Clean(dir)
	if c.onPath[dir] {
		return
	}
	child, err := load(dir)
	if err != nil {
		return
	}

	inputs := map[string]cty.Value{}
	for argName, a := range b.Body.Attributes {
		if moduleMeta[argName] {
			continue
		}
		if v, ok := f.value(a.Expr); ok {
			inputs[argName] = v
		} else {
			inputs[argName] = cty.DynamicVal
		}
	}
	vars, sources := c.variableValues(child, inputs)
	c.onPath[dir] = true
	c.walk(child, newFrame(child, vars), sources, addr, combine(enabled, own), depth+1)
	delete(c.onPath, dir)
}

func address(path, typ, name string) string {
	return modulePrefix(path) + typ + "." + name
}

func dataAddress(path, typ, name string) string {
	return modulePrefix(path) + "data." + typ + "." + name
}

func modulePrefix(path string) string {
	if path == "" {
		return ""
	}
	return "module." + strings.ReplaceAll(path, ".", ".module.") + "."
}

// reference matches an expression that is only a reference to an attribute of another resource,
// such as azurerm_resource_group.rg.name or data.azurerm_client_config.current.tenant_id.
var reference = regexp.MustCompile(`^(data\.)?([A-Za-z0-9_]+)\.([A-Za-z0-9_-]+)\.([a-z_]+)$`)

// resolveReferences fills in attributes that are a reference to another resource in the same
// module from what that resource's attribute resolved to. The resource group a network is
// created in is usually written this way, and a lookup of it names the resource group directly.
func resolveReferences(resources []Resource) {
	type key struct{ path, mode, typ, name string }
	byKey := map[key]*Resource{}
	for i := range resources {
		r := &resources[i]
		byKey[key{r.ModulePath, r.Mode, r.Type, r.Name}] = r
	}
	// A reference can lead to another that is itself a reference, so go round until nothing changes.
	for range 5 {
		changed := false
		for i := range resources {
			r := &resources[i]
			for attr, expr := range r.Unresolved {
				m := reference.FindStringSubmatch(expr)
				if m == nil {
					continue
				}
				mode := ModeManaged
				if m[1] != "" {
					mode = ModeData
				}
				target, ok := byKey[key{r.ModulePath, mode, m[2], m[3]}]
				if !ok || target == r {
					continue
				}
				value, ok := target.Attrs[m[4]]
				if !ok {
					continue
				}
				if r.Attrs == nil {
					r.Attrs = map[string]string{}
				}
				r.Attrs[attr] = value
				delete(r.Unresolved, attr)
				changed = true
			}
			if len(r.Unresolved) == 0 {
				r.Unresolved = nil
			}
		}
		if !changed {
			return
		}
	}
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// StateKey names a state location from a backend type and its settings, in a
// form that is the same whether it comes from a backend block or from a
// terraform_remote_state data source reading it. It is empty when the backend
// has nothing that identifies a state.
func StateKey(backend string, cfg map[string]string) string {
	pick := func(keys ...string) string {
		var parts []string
		for _, k := range keys {
			if v := strings.TrimSpace(cfg[k]); v != "" {
				parts = append(parts, v)
			}
		}
		return strings.Join(parts, "/")
	}
	var id string
	switch backend {
	case "azurerm":
		id = pick("storage_account_name", "container_name", "key")
	case "s3":
		id = pick("bucket", "key")
	case "gcs":
		id = pick("bucket", "prefix")
	case "remote", "cloud":
		backend = "cloud"
		id = pick("organization", "workspace")
	case "http", "consul":
		id = pick("address", "path")
	default:
		return ""
	}
	if id == "" {
		return ""
	}
	return backend + ":" + strings.ToLower(id)
}
