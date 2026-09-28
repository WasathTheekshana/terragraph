package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/scanner/internal/client"
	"github.com/WasathTheekshana/terragraph/scanner/internal/report"
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

func gitRepo(t *testing.T, dir, origin, branch string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", branch},
		{"remote", "add", "origin", origin},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

const vpcCall = `module "vpc" {
  source = "git::git@github.com:org/vpc.git?ref=v1.0.0"
}
`

// workspace is a folder of clones like a developer's: a multi-root repo, a
// repo nested a few levels down, and Terraform outside any repo.
func workspace(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	write(t, base, "infra/envs/dev/main.tf", vpcCall)
	write(t, base, "infra/envs/prod/main.tf", vpcCall+`module "s3" {
  source = "github.com/org/s3?ref=v2.0.0"
}
`)
	write(t, base, "infra/main.tf", `module "net" {
  source = "./modules/net"
}
`)
	write(t, base, "infra/modules/net/main.tf", vpcCall)
	gitRepo(t, filepath.Join(base, "infra"), "git@github.com:org/infra.git", "main")

	write(t, base, "team-a/services/api/main.tf", `module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"
}
`)
	gitRepo(t, filepath.Join(base, "team-a/services/api"), "https://github.com/org/api.git", "develop")

	write(t, base, "scratch/broken/main.tf", `module "x" {`)
	return base
}

func TestPlanProjects(t *testing.T) {
	base := workspace(t)
	targets, err := PlanProjects(Options{Path: base})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]Target{}
	for _, tg := range targets {
		got[tg.String()] = tg
	}
	want := []string{
		"git@github.com:org/infra.git",
		"git@github.com:org/infra.git (envs/dev)",
		"git@github.com:org/infra.git (envs/prod)",
		"https://github.com/org/api.git",
	}
	for _, w := range want {
		tg, ok := got[w]
		if !ok {
			t.Errorf("missing target %q; have %v", w, keys(got))
			continue
		}
		if tg.Report == nil || tg.Report.Subject.Path != tg.Path || len(tg.Report.Subject.CommitSHA) != 40 {
			t.Errorf("%s: report = %+v", w, tg.Report)
		}
	}
	if b := got["https://github.com/org/api.git"]; b.Report != nil && b.Report.Subject.Branch != "develop" {
		t.Errorf("branch = %q, want the repo's own branch", b.Report.Subject.Branch)
	}

	var broken *Target
	for i := range targets {
		if strings.HasPrefix(targets[i].RepoURL, "file://") {
			broken = &targets[i]
		}
	}
	if len(targets) != 5 || broken == nil || broken.Err == nil || broken.Path != "scratch/broken" {
		t.Errorf("want the non-git folder reported by location with its parse error, got %+v", broken)
	}
}

func TestPlanSubfolderKeepsRepoIdentity(t *testing.T) {
	base := workspace(t)
	targets, err := PlanProjects(Options{Path: filepath.Join(base, "infra", "envs", "prod"), Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].RepoURL != "git@github.com:org/infra.git" || targets[0].Path != "envs/prod" {
		t.Errorf("targets = %+v; a subfolder should keep its repo and path", targets)
	}
}

func TestPlanOverridesNeedOneRepo(t *testing.T) {
	base := workspace(t)
	if _, err := PlanProjects(Options{Path: base, RepoURL: "https://github.com/org/x.git"}); err == nil {
		t.Error("--repo-url across several repos: want error")
	}
	targets, err := PlanProjects(Options{Path: filepath.Join(base, "infra"), RepoURL: "https://github.com/org/renamed.git", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.RepoURL != "https://github.com/org/renamed.git" || tg.Report.Subject.Branch != "main" {
			t.Errorf("override not applied: %+v", tg)
		}
	}
}

func TestPlanModuleRepos(t *testing.T) {
	targets, err := PlanProjects(Options{Path: workspace(t)})
	if err != nil {
		t.Fatal(err)
	}
	mods := PlanModuleRepos(targets)
	var urls []string
	for _, m := range mods {
		if m.Kind != client.ItemKindModuleRepo {
			t.Errorf("kind = %q", m.Kind)
		}
		urls = append(urls, m.RepoURL)
	}
	want := "[git@github.com:org/vpc.git https://github.com/org/s3.git]"
	if got := strings.Join(urls, " "); "["+got+"]" != want {
		t.Errorf("module repos = %v, want %s (deduplicated, registry modules skipped)", urls, want)
	}
}

func facts(t *testing.T, dir string) map[string]report.Fact {
	t.Helper()
	targets, err := PlanProjects(Options{Path: dir, RepoURL: "https://github.com/org/app.git"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]report.Fact{}
	for _, tg := range targets {
		if tg.Report == nil {
			t.Fatalf("%s: %v", tg, tg.Err)
		}
		for _, f := range tg.Report.Facts {
			addr := f.CallName
			if f.Parent != "" {
				addr = f.Parent + "." + f.CallName
			}
			out[addr] = f
		}
	}
	return out
}

func TestNestedLocalModules(t *testing.T) {
	base := t.TempDir()
	write(t, base, "envs/prod/main.tf", `module "addons" {
  source = "../../modules/addons"
}
module "addons_again" {
  source = "../../modules/addons"
}
`)
	write(t, base, "modules/addons/main.tf", `module "vpc" {
  source = "git::https://github.com/org/vpc.git?ref=v1.0.0"
}
module "net" {
  source = "./net"
}
`)
	write(t, base, "modules/addons/net/main.tf", vpcCall)

	got := facts(t, base)
	want := map[string]string{
		"addons":               "",
		"addons.vpc":           "addons",
		"addons.net":           "addons",
		"addons.net.vpc":       "addons.net",
		"addons_again.vpc":     "addons_again",
		"addons_again.net.vpc": "addons_again.net",
	}
	for addr, parent := range want {
		f, ok := got[addr]
		if !ok {
			t.Errorf("missing %s; have %v", addr, mapKeys(got))
			continue
		}
		if f.Parent != parent {
			t.Errorf("%s parent = %q, want %q", addr, f.Parent, parent)
		}
	}
	if f := got["addons.net.vpc"]; f.File != "../../modules/addons/net/main.tf" || f.Line != 1 {
		t.Errorf("nested file = %s:%d, want it relative to the root", f.File, f.Line)
	}
	if got["addons"].File != "main.tf" {
		t.Errorf("direct call file = %q", got["addons"].File)
	}
}

func TestNestingStopsOnCycles(t *testing.T) {
	base := t.TempDir()
	write(t, base, "main.tf", `module "a" {
  source = "./modules/a"
}
`)
	write(t, base, "modules/a/main.tf", `module "b" {
  source = "../b"
}
`)
	write(t, base, "modules/b/main.tf", `module "a" {
  source = "../a"
}
`)
	got := facts(t, base)
	if len(got) != 3 || got["a.b.a"].Parent != "a.b" {
		t.Errorf("facts = %v; want a, a.b, a.b.a and no further", mapKeys(got))
	}
}

func TestNestedRemoteModulesAfterInit(t *testing.T) {
	base := t.TempDir()
	write(t, base, "main.tf", `module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"
}
`)
	write(t, base, ".terraform/modules/eks/main.tf", `module "kms" {
  source  = "terraform-aws-modules/kms/aws"
  version = "2.1.0"
}
`)
	write(t, base, ".terraform/modules/modules.json", `{"Modules":[
		{"Key":"","Source":"","Dir":"."},
		{"Key":"eks","Source":"registry.terraform.io/terraform-aws-modules/eks/aws","Version":"20.8.5","Dir":".terraform/modules/eks"},
		{"Key":"eks.kms","Source":"registry.terraform.io/terraform-aws-modules/kms/aws","Version":"2.1.0","Dir":".terraform/modules/eks.kms"}
	]}`)

	got := facts(t, base)
	kms, ok := got["eks.kms"]
	if !ok {
		t.Fatalf("remote module's own call not found: %v", mapKeys(got))
	}
	if kms.Parent != "eks" || kms.VersionResolved != "2.1.0" || kms.ResolutionSource != report.ResolutionSourceModulesJSON {
		t.Errorf("eks.kms = %+v", kms)
	}
	if kms.File != ".terraform/modules/eks/main.tf" {
		t.Errorf("eks.kms file = %q", kms.File)
	}
	if got["eks"].VersionResolved != "20.8.5" {
		t.Errorf("eks = %+v", got["eks"])
	}
}

func TestRemoteModulesWithoutInitAreNotExpanded(t *testing.T) {
	base := t.TempDir()
	write(t, base, "main.tf", vpcCall)
	if got := facts(t, base); len(got) != 1 {
		t.Errorf("facts = %v; a remote module's code isn't on disk without init", mapKeys(got))
	}
}

func mapKeys(m map[string]report.Fact) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPlanEmptyFolder(t *testing.T) {
	if _, err := PlanProjects(Options{Path: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "no Terraform files") {
		t.Errorf("err = %v", err)
	}
}

func keys(m map[string]Target) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
