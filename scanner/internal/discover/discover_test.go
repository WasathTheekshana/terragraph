package discover

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const localModule = `module "net" {
  source = "./modules/net"
}
`

func TestRoots(t *testing.T) {
	base := t.TempDir()
	write(t, base, "repo-a/main.tf", localModule)
	write(t, base, "repo-a/modules/net/main.tf", `variable "cidr" {}`)
	write(t, base, "repo-a/envs/prod/main.tf", `module "net" {
  source = "../../modules/net"
}
module "vpc" {
  source = "git::https://github.com/org/vpc.git?ref=v1.0.0"
}
`)
	write(t, base, "group/deep/repo-b/main.tf.json", `{"module": {"vpc": {"source": "github.com/org/vpc?ref=v2.0.0"}}}`)
	write(t, base, "repo-c/.terraform/modules/x/main.tf", `variable "x" {}`)
	write(t, base, "repo-c/node_modules/pkg/main.tf", `variable "x" {}`)
	write(t, base, "repo-d/examples/basic/main.tf", `module "m" { source = "../.." }`)
	write(t, base, "repo-e/broken/main.tf", `module "x" {`)
	write(t, base, "repo-f/legacy/main.tf", `variable "x" {}`)
	write(t, base, "repo-f/README.md", "not terraform")

	roots, err := Roots(base, Options{Exclude: []string{"legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range roots {
		rel, _ := filepath.Rel(base, r.Dir)
		got = append(got, filepath.ToSlash(rel))
	}
	want := []string{"group/deep/repo-b", "repo-a", "repo-a/envs/prod", "repo-e/broken"}
	if !slices.Equal(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}

	byDir := map[string]Root{}
	for i, r := range roots {
		byDir[got[i]] = r
	}
	if r := byDir["repo-a/envs/prod"]; r.Err != nil || len(r.Calls) != 2 {
		t.Errorf("repo-a/envs/prod = %d calls, err %v; want 2 calls", len(r.Calls), r.Err)
	}
	if r := byDir["group/deep/repo-b"]; len(r.Calls) != 1 || r.Calls[0].RefDeclared != "v2.0.0" {
		t.Errorf(".tf.json root calls = %+v", r.Calls)
	}
	if byDir["repo-e/broken"].Err == nil {
		t.Error("broken root has no error")
	}
}

func TestRootsOnASingleRoot(t *testing.T) {
	base := t.TempDir()
	write(t, base, "main.tf", localModule)
	write(t, base, "modules/net/main.tf", `variable "x" {}`)

	roots, err := Roots(base, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].Dir != base {
		t.Errorf("roots = %+v, want just the base directory", roots)
	}
}

func TestExcludeMatchesRelativePaths(t *testing.T) {
	base := t.TempDir()
	write(t, base, "envs/dev/main.tf", `variable "x" {}`)
	write(t, base, "envs/prod/main.tf", `variable "x" {}`)

	roots, err := Roots(base, Options{Exclude: []string{"envs/dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || filepath.Base(roots[0].Dir) != "prod" {
		t.Errorf("roots = %+v, want only envs/prod", roots)
	}
}

func TestRootsErrors(t *testing.T) {
	base := t.TempDir()
	write(t, base, "main.tf", `variable "x" {}`)
	if _, err := Roots(filepath.Join(base, "missing"), Options{}); err == nil {
		t.Error("missing directory: want error")
	}
	if _, err := Roots(filepath.Join(base, "main.tf"), Options{}); err == nil {
		t.Error("file instead of directory: want error")
	}
	if _, err := Roots(base, Options{Exclude: []string{"["}}); err == nil {
		t.Error("bad pattern: want error")
	}
}
