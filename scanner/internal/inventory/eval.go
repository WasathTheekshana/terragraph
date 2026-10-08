package inventory

import (
	"math/big"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

var functions = map[string]function.Function{
	"lower":      stdlib.LowerFunc,
	"upper":      stdlib.UpperFunc,
	"title":      stdlib.TitleFunc,
	"format":     stdlib.FormatFunc,
	"join":       stdlib.JoinFunc,
	"split":      stdlib.SplitFunc,
	"concat":     stdlib.ConcatFunc,
	"replace":    stdlib.ReplaceFunc,
	"trimspace":  stdlib.TrimSpaceFunc,
	"substr":     stdlib.SubstrFunc,
	"coalesce":   stdlib.CoalesceFunc,
	"merge":      stdlib.MergeFunc,
	"length":     stdlib.LengthFunc,
	"contains":   stdlib.ContainsFunc,
	"lookup":     stdlib.LookupFunc,
	"element":    stdlib.ElementFunc,
	"keys":       stdlib.KeysFunc,
	"values":     stdlib.ValuesFunc,
	"flatten":    stdlib.FlattenFunc,
	"distinct":   stdlib.DistinctFunc,
	"tostring":   stdlib.MakeToFunc(cty.String),
	"tonumber":   stdlib.MakeToFunc(cty.Number),
	"tobool":     stdlib.MakeToFunc(cty.Bool),
	"tolist":     stdlib.MakeToFunc(cty.List(cty.DynamicPseudoType)),
	"toset":      stdlib.MakeToFunc(cty.Set(cty.DynamicPseudoType)),
	"tomap":      stdlib.MakeToFunc(cty.Map(cty.DynamicPseudoType)),
	"jsonencode": stdlib.JSONEncodeFunc,
}

// frame is one module's variables and locals, as evaluated for one call of it.
type frame struct {
	m *module
	// vars are the module's variables. An unknown value stands for one that is
	// only known when Terraform runs.
	vars   map[string]cty.Value
	locals map[string]cty.Value
	ctx    *hcl.EvalContext
}

func newFrame(m *module, vars map[string]cty.Value) *frame {
	f := &frame{m: m, vars: vars, locals: map[string]cty.Value{}}
	f.ctx = &hcl.EvalContext{Functions: functions}
	f.refresh()

	// Locals refer to each other in any order, so settle them over a few passes.
	names := sortedKeys(m.locals)
	for _, name := range names {
		f.locals[name] = cty.DynamicVal
	}
	settled := map[string]bool{}
	for pass := 0; pass < len(names)+1; pass++ {
		f.refresh()
		changed := false
		for _, name := range names {
			if settled[name] {
				continue
			}
			v, diags := m.locals[name].Value(f.ctx)
			if diags.HasErrors() || !v.IsWhollyKnown() {
				continue
			}
			f.locals[name] = v
			settled[name] = true
			changed = true
		}
		if !changed {
			break
		}
	}
	f.refresh()
	return f
}

func (f *frame) refresh() {
	f.ctx.Variables = map[string]cty.Value{
		"var":   object(f.vars),
		"local": object(f.locals),
	}
}

func object(m map[string]cty.Value) cty.Value {
	if len(m) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(m)
}

// value evaluates an expression, reporting false when it depends on anything
// that isn't known from the source.
func (f *frame) value(e hclsyntax.Expression) (cty.Value, bool) {
	v, diags := e.Value(f.ctx)
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsMarked() {
		return cty.NilVal, false
	}
	return v, true
}

func (f *frame) str(e hclsyntax.Expression) (string, bool) {
	v, ok := f.value(e)
	if !ok || v.IsNull() {
		return "", false
	}
	return render(v), true
}

// render writes a value as plain text: strings and numbers as they are, anything
// else as JSON.
func render(v cty.Value) string {
	switch {
	case v.Type() == cty.String:
		return v.AsString()
	case v.Type() == cty.Bool:
		if v.True() {
			return "true"
		}
		return "false"
	case v.Type() == cty.Number:
		return v.AsBigFloat().Text('f', -1)
	}
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return ""
	}
	return string(b)
}

// count reads a count or for_each expression into the number of copies it makes.
func (f *frame) count(e hclsyntax.Expression, forEach bool) (int, bool) {
	v, ok := f.value(e)
	if !ok || v.IsNull() {
		return 0, false
	}
	if forEach {
		if !v.CanIterateElements() {
			return 0, false
		}
		return v.LengthInt(), true
	}
	n, err := convert.Convert(v, cty.Number)
	if err != nil {
		return 0, false
	}
	i, acc := n.AsBigFloat().Int64()
	if acc != big.Exact && acc != big.Below && acc != big.Above {
		return 0, false
	}
	return int(i), true
}

// tags reads a tags expression into the tags it resolves to and, for the rest, the
// expression behind each. Tags are usually a literal map, a merge of maps, or a
// local holding one, so each is followed.
func (f *frame) tags(e hclsyntax.Expression, depth int) (resolved, unresolved map[string]string) {
	resolved, unresolved = map[string]string{}, map[string]string{}
	if depth > 6 {
		unresolved["*"] = f.m.text(e)
		return
	}
	set := func(r, u map[string]string) {
		for k, v := range r {
			resolved[k] = v
			delete(unresolved, k)
		}
		for k, v := range u {
			if k == "*" {
				unresolved[k] = v
				continue
			}
			unresolved[k] = v
			delete(resolved, k)
		}
	}

	switch x := e.(type) {
	case *hclsyntax.ObjectConsExpr:
		for _, item := range x.Items {
			kv, ok := f.value(item.KeyExpr)
			if !ok || kv.IsNull() {
				unresolved["*"] = f.m.text(e)
				continue
			}
			key := render(kv)
			if v, ok := f.str(item.ValueExpr); ok {
				delete(unresolved, key)
				resolved[key] = v
			} else {
				delete(resolved, key)
				unresolved[key] = f.m.text(item.ValueExpr)
			}
		}
		return
	case *hclsyntax.FunctionCallExpr:
		if x.Name == "merge" {
			for _, arg := range x.Args {
				set(f.tags(arg, depth+1))
			}
			return
		}
	case *hclsyntax.ScopeTraversalExpr:
		if x.Traversal.RootName() == "local" && len(x.Traversal) == 2 {
			if name, ok := x.Traversal[1].(hcl.TraverseAttr); ok {
				if le, ok := f.m.locals[name.Name]; ok {
					return f.tags(le, depth+1)
				}
			}
		}
	case *hclsyntax.ParenthesesExpr:
		return f.tags(x.Expression, depth+1)
	}

	if v, ok := f.value(e); ok && !v.IsNull() && (v.Type().IsObjectType() || v.Type().IsMapType()) {
		for k, ev := range v.AsValueMap() {
			if !ev.IsNull() {
				resolved[k] = render(ev)
			}
		}
		return
	}
	unresolved["*"] = f.m.text(e)
	return
}

// variableRefs lists the variables an expression depends on, following locals.
func (f *frame) variableRefs(e hclsyntax.Expression, seen map[string]bool, out map[string]bool) {
	for _, t := range e.Variables() {
		if len(t) < 2 {
			continue
		}
		attr, ok := t[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		switch t.RootName() {
		case "var":
			out[attr.Name] = true
		case "local":
			if le, ok := f.m.locals[attr.Name]; ok && !seen[attr.Name] {
				seen[attr.Name] = true
				f.variableRefs(le, seen, out)
			}
		}
	}
}

func hasSecretName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"password", "passwd", "secret", "token", "credential", "private_key", "api_key", "access_key", "connection_string"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}
