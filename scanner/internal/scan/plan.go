// Package scan plans and runs a scan: it finds every Terraform root under a
// path, the module repos they use, and reports each to the server as one run.
package scan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/WasathTheekshana/terragraph/scanner/internal/client"
	"github.com/WasathTheekshana/terragraph/scanner/internal/discover"
	"github.com/WasathTheekshana/terragraph/scanner/internal/gitinfo"
	"github.com/WasathTheekshana/terragraph/scanner/internal/gittags"
	"github.com/WasathTheekshana/terragraph/scanner/internal/moduleinit"
	"github.com/WasathTheekshana/terragraph/scanner/internal/report"
)

type Options struct {
	Path    string
	Exclude []string
	// RepoURL and Commit override what git reports. They only make sense when
	// Path holds a single repo.
	RepoURL string
	Commit  string
	// Branch overrides every repo's branch.
	Branch string
}

// Target is one item of a run: a Terraform root, or a module repo whose
// versions to list.
type Target struct {
	Kind    string
	RepoURL string
	Path    string
	// Report is built while planning for projects; module repos are listed
	// when the run executes, since that needs the network.
	Report *report.ScanReport
	// Err is a planning failure (unparsable configuration) to report.
	Err error
}

func (t Target) spec() client.ItemSpec {
	return client.ItemSpec{Kind: t.Kind, RepoURL: t.RepoURL, Path: t.Path}
}

func (t Target) String() string {
	if t.Kind == client.ItemKindProject && t.Path != "." {
		return t.RepoURL + " (" + t.Path + ")"
	}
	return t.RepoURL
}

// PlanProjects finds every Terraform root under opts.Path and builds its
// report.
func PlanProjects(opts Options) ([]Target, error) {
	base, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, err
	}
	roots, err := discover.Roots(base, discover.Options{Exclude: opts.Exclude})
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no Terraform files found under %s", base)
	}

	repos := map[string]gitinfo.Repo{}
	targets := make([]Target, 0, len(roots))
	for _, root := range roots {
		repo := repoFor(root.Dir, base, repos)
		t := Target{
			Kind:    client.ItemKindProject,
			RepoURL: cmp.Or(opts.RepoURL, repo.RemoteURL, localURL(repo.Root)),
			Path:    relPath(repo.Root, root.Dir),
			Err:     root.Err,
		}
		if root.Err == nil {
			r := projectReport(root, t, cmp.Or(opts.Commit, repo.CommitSHA), cmp.Or(opts.Branch, repo.Branch))
			t.Report = &r
		}
		targets = append(targets, t)
	}

	if (opts.RepoURL != "" || opts.Commit != "") && len(repos) > 1 {
		return nil, fmt.Errorf("--repo-url and --commit need a path holding one repo, but %s holds %d", base, len(repos))
	}
	return targets, nil
}

// repoFor returns the checkout holding dir, reading each checkout's metadata
// once. Directories outside any checkout belong to base.
func repoFor(dir, base string, cache map[string]gitinfo.Repo) gitinfo.Repo {
	root, ok := gitinfo.FindRoot(dir)
	if !ok {
		root = base
	}
	if r, ok := cache[root]; ok {
		return r
	}
	r := gitinfo.Repo{Root: root}
	if ok {
		r = gitinfo.Read(root)
	}
	cache[root] = r
	return r
}

// localURL identifies a directory that isn't a git checkout by where it is.
func localURL(dir string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	return "file://" + strings.ToLower(host) + "/" + strings.TrimPrefix(filepath.ToSlash(dir), "/")
}

func relPath(repoRoot, dir string) string {
	rel, err := filepath.Rel(repoRoot, dir)
	if err != nil {
		return "."
	}
	return filepath.ToSlash(rel)
}

func projectReport(root discover.Root, t Target, commit, branch string) report.ScanReport {
	manifest, err := moduleinit.Load(root.Dir)
	if err != nil {
		manifest = nil
	}
	facts := make([]report.Fact, 0, len(root.Calls))
	for _, c := range root.Calls {
		f := report.Fact{
			Type:             report.FactTypeModuleCall,
			CallName:         c.CallName,
			Source:           c.Source,
			RefDeclared:      c.RefDeclared,
			RefResolved:      c.RefDeclared,
			ResolutionSource: report.ResolutionSourceParse,
			File:             c.File,
			Line:             c.Line,
		}
		if res, ok := manifest.Resolve(c.CallName); ok {
			f.ResolutionSource = report.ResolutionSourceModulesJSON
			f.VersionResolved = res.Version
			if res.Commit != "" {
				f.RefResolved = res.Commit
			}
		}
		facts = append(facts, f)
	}
	return report.New(report.ScannerTypeModuleUsage, report.Subject{
		Kind:      report.SubjectKindProject,
		RepoURL:   t.RepoURL,
		Path:      t.Path,
		CommitSHA: commit,
		Branch:    branch,
	}, facts)
}

// PlanModuleRepos returns one target per git repo the projects' modules come
// from, so their released versions are listed alongside.
func PlanModuleRepos(projects []Target) []Target {
	seen := map[string]bool{}
	var out []Target
	for _, p := range projects {
		if p.Report == nil {
			continue
		}
		for _, f := range p.Report.Facts {
			url, ok := gittags.SourceURL(f.Source)
			if !ok || seen[strings.ToLower(url)] {
				continue
			}
			seen[strings.ToLower(url)] = true
			out = append(out, ModuleRepo(url))
		}
	}
	slices.SortFunc(out, func(a, b Target) int { return cmp.Compare(a.RepoURL, b.RepoURL) })
	return out
}

func ModuleRepo(url string) Target {
	return Target{Kind: client.ItemKindModuleRepo, RepoURL: url}
}

// moduleRepoReport lists a module repo's tags.
func moduleRepoReport(ctx context.Context, url string) (report.ScanReport, error) {
	tags, err := gittags.List(ctx, url)
	if err != nil {
		return report.ScanReport{}, err
	}
	facts := make([]report.Fact, 0, len(tags))
	for _, t := range tags {
		facts = append(facts, report.Fact{Type: report.FactTypeVersionTag, Tag: t.Name, SemVer: t.SemVer, CommitSHA: t.CommitSHA})
	}
	return report.New(report.ScannerTypeModuleRepo, report.Subject{Kind: report.SubjectKindModuleRepo, RepoURL: url}, facts), nil
}

// Collect builds every target's report without a server, for --dry-run and
// --out. Failed targets are returned as errors.
func Collect(ctx context.Context, targets []Target) ([]report.ScanReport, error) {
	var reports []report.ScanReport
	var errs []error
	for _, t := range targets {
		r, err := build(ctx, t)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t, err))
			continue
		}
		reports = append(reports, r)
	}
	return reports, errors.Join(errs...)
}

func build(ctx context.Context, t Target) (report.ScanReport, error) {
	switch {
	case t.Err != nil:
		return report.ScanReport{}, t.Err
	case t.Report != nil:
		return *t.Report, nil
	default:
		return moduleRepoReport(ctx, t.RepoURL)
	}
}
