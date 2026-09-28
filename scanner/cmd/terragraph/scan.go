package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/WasathTheekshana/terragraph/scanner/internal/client"
	"github.com/WasathTheekshana/terragraph/scanner/internal/scan"
)

type scanFlags struct {
	mode               string
	opts               scan.Options
	skipModuleVersions bool
	concurrency        int
	apiURL             string
	token              string
	githubOIDC         bool
	oidcAudience       string
	out                string
	dryRun             bool
}

func newScanCmd() *cobra.Command {
	var f scanFlags

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan Terraform under a path and report it to TerraGraph",
		Long: `Scan finds every Terraform root under --path, whether that's one repo, a
folder inside a repo, or a folder of many repos at any depth. Each root is
reported as a project, and the git repos its modules come from are checked
for released versions.

Directories that another root uses as a local module, hidden directories,
node_modules, and examples are skipped. Use --exclude for anything else.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read from the environment here rather than as flag defaults, so
			// the token never appears in --help or usage output in CI logs.
			if f.apiURL == "" {
				f.apiURL = os.Getenv("TERRAGRAPH_API_URL")
			}
			if f.token == "" {
				f.token = os.Getenv("TERRAGRAPH_TOKEN")
			}
			if !cmd.Flags().Changed("github-oidc") {
				f.githubOIDC, _ = strconv.ParseBool(os.Getenv("TERRAGRAPH_GITHUB_OIDC"))
			}
			if f.githubOIDC && f.token != "" {
				return fmt.Errorf("use either --github-oidc or a token, not both")
			}
			return runScan(cmd.Context(), f)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&f.mode, "mode", "project", "project: scan Terraform under --path | module-repo: list one module repo's versions")
	fl.StringVar(&f.opts.Path, "path", ".", "directory to scan: a repo, a folder inside one, or a folder of many repos")
	fl.StringSliceVar(&f.opts.Exclude, "exclude", nil, "directory name or relative path glob to skip; repeatable")
	fl.StringVar(&f.opts.RepoURL, "repo-url", "", "repo URL to report; project mode reads it from git (single repo only), module-repo mode requires it")
	fl.StringVar(&f.opts.Commit, "commit", "", "commit SHA to report instead of git's (single repo only)")
	fl.StringVar(&f.opts.Branch, "branch", "", "branch to report for every repo instead of git's, e.g. from CI variables")
	fl.BoolVar(&f.skipModuleVersions, "skip-module-versions", false, "don't list released versions of the git repos modules come from")
	fl.IntVar(&f.concurrency, "concurrency", 4, "items to scan at the same time")
	fl.StringVar(&f.apiURL, "api-url", "", "TerraGraph server base URL (default: env TERRAGRAPH_API_URL)")
	fl.StringVar(&f.token, "token", "", "auth token for the TerraGraph server (default: env TERRAGRAPH_TOKEN)")
	fl.BoolVar(&f.githubOIDC, "github-oidc", false, "in GitHub Actions, sign in with the workflow's ID token instead of a token (default: env TERRAGRAPH_GITHUB_OIDC)")
	fl.StringVar(&f.oidcAudience, "oidc-audience", "terragraph", "audience to request for --github-oidc; must match the server's TERRAGRAPH_GITHUB_OIDC_AUDIENCE")
	fl.StringVar(&f.out, "out", "", "also write the reports to this file as a JSON array")
	fl.BoolVar(&f.dryRun, "dry-run", false, "print the reports instead of submitting them")
	return cmd
}

func runScan(ctx context.Context, f scanFlags) error {
	targets, label, err := plan(f)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Found %s.\n", describe(targets))

	if f.dryRun || f.out != "" || f.apiURL == "" {
		return collect(ctx, f, targets)
	}

	c := client.New(strings.TrimRight(f.apiURL, "/"), f.token)
	if f.githubOIDC {
		gh, err := client.GitHubOIDCFromEnv(f.oidcAudience)
		if err != nil {
			return err
		}
		c.TokenSource = gh.Token
	}
	runner := &scan.Runner{
		Client:      c,
		Concurrency: f.concurrency,
		Out:         os.Stderr,
		UIBase:      strings.TrimRight(f.apiURL, "/"),
	}
	sum, err := runner.Run(ctx, label, targets)
	if sum.RunID != 0 {
		fmt.Fprintf(os.Stderr, "\n%d done, %d failed", sum.Done, sum.Failed)
		if sum.NotApplied > 0 {
			fmt.Fprintf(os.Stderr, ", %d recorded but not current", sum.NotApplied)
		}
		fmt.Fprintf(os.Stderr, ". Results: %s/runs/%d\n", runner.UIBase, sum.RunID)
	}
	if err != nil {
		return err
	}
	if sum.Failed > 0 {
		return fmt.Errorf("%d of %d items failed", sum.Failed, sum.Total)
	}
	return nil
}

func plan(f scanFlags) ([]scan.Target, string, error) {
	host, _ := os.Hostname()
	switch f.mode {
	case "project":
		targets, err := scan.PlanProjects(f.opts)
		if err != nil {
			return nil, "", err
		}
		if !f.skipModuleVersions {
			targets = append(targets, scan.PlanModuleRepos(targets)...)
		}
		abs, _ := filepath.Abs(f.opts.Path)
		return targets, fmt.Sprintf("%s: %s", host, abs), nil
	case "module-repo":
		if f.opts.RepoURL == "" {
			return nil, "", fmt.Errorf("--repo-url is required in module-repo mode")
		}
		return []scan.Target{scan.ModuleRepo(f.opts.RepoURL)}, fmt.Sprintf("%s: module repo %s", host, f.opts.RepoURL), nil
	default:
		return nil, "", fmt.Errorf("--mode must be %q or %q, got %q", "project", "module-repo", f.mode)
	}
}

func describe(targets []scan.Target) string {
	projects, repos, modules := 0, map[string]bool{}, 0
	for _, t := range targets {
		if t.Kind == client.ItemKindModuleRepo {
			modules++
			continue
		}
		projects++
		repos[t.RepoURL] = true
	}
	if projects == 0 {
		return fmt.Sprintf("%d module repo(s) to check", modules)
	}
	return fmt.Sprintf("%d Terraform root(s) in %d repo(s), and %d module repo(s) to check", projects, len(repos), modules)
}

// collect builds every report locally and prints or saves them, for use
// without a server.
func collect(ctx context.Context, f scanFlags, targets []scan.Target) error {
	reports, buildErr := scan.Collect(ctx, targets)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(reports); err != nil {
		return fmt.Errorf("encoding reports: %w", err)
	}

	if f.out != "" {
		if err := os.WriteFile(f.out, buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", f.out, err)
		}
		fmt.Fprintf(os.Stderr, "Wrote %d report(s) to %s\n", len(reports), f.out)
	} else {
		os.Stdout.Write(buf.Bytes())
	}
	return buildErr
}
