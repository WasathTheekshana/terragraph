package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WasathTheekshana/terragraph/scanner/internal/client"
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
