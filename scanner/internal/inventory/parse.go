package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// module is the parsed source of one directory.
type module struct {
	dir       string
	src       map[string][]byte
	variables []*hclsyntax.Block
	locals    map[string]hclsyntax.Expression
	resources []*hclsyntax.Block
	outputs   []*hclsyntax.Block
	calls     []*hclsyntax.Block
	terraform []*hclsyntax.Block
	// tfvars holds the values set by terraform.tfvars and *.auto.tfvars.
	tfvars map[string]hclsyntax.Expression
}

// load parses the .tf files directly in dir, the way Terraform scopes one module.
func load(dir string) (*module, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	m := &module{dir: dir, src: map[string][]byte{}, locals: map[string]hclsyntax.Expression{}, tfvars: map[string]hclsyntax.Expression{}}
	for _, f := range files {
		body, err := m.parse(f)
		if err != nil {
			return nil, err
		}
		for _, b := range body.Blocks {
			switch b.Type {
			case "variable":
				m.variables = append(m.variables, b)
			case "locals":
				for name, a := range b.Body.Attributes {
					m.locals[name] = a.Expr
				}
			case "resource", "data":
				if len(b.Labels) == 2 {
					m.resources = append(m.resources, b)
				}
			case "output":
				m.outputs = append(m.outputs, b)
			case "module":
				if len(b.Labels) == 1 {
					m.calls = append(m.calls, b)
				}
			case "terraform":
				m.terraform = append(m.terraform, b)
			}
		}
	}

	vars, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	sort.Strings(vars)
	for _, f := range append([]string{filepath.Join(dir, "terraform.tfvars")}, vars...) {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		body, err := m.parse(f)
		if err != nil {
			return nil, err
		}
		for name, a := range body.Attributes {
			m.tfvars[name] = a.Expr
		}
	}
	return m, nil
}

func (m *module) parse(file string) (*hclsyntax.Body, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	m.src[file] = src
	parsed, diags := hclsyntax.ParseConfig(src, file, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %s", filepath.Base(file), diags[0].Summary)
	}
	return parsed.Body.(*hclsyntax.Body), nil
}

// text is the source of an expression on one line, shortened.
func (m *module) text(e hclsyntax.Expression) string {
	r := e.Range()
	src := m.src[r.Filename]
	if r.End.Byte > len(src) || r.Start.Byte > r.End.Byte {
		return ""
	}
	s := strings.Join(strings.Fields(string(src[r.Start.Byte:r.End.Byte])), " ")
	const limit = 200
	if len(s) > limit {
		s = s[:limit-3] + "..."
	}
	return s
}

func (m *module) rel(root, file string) string {
	if rel, err := filepath.Rel(root, file); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(file)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
