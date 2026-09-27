package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"terragraph/scanner/internal/client"
	"terragraph/scanner/internal/gitinfo"
	"terragraph/scanner/internal/gittags"
	"terragraph/scanner/internal/hclscan"
	"terragraph/scanner/internal/moduleinit"
	"terragraph/scanner/internal/report"
)

// commonFlags are shared by every scan mode: where to send the report.
type commonFlags struct {
	apiURL string
	token  string
	out    string
	dryRun bool
}

func newScanCmd() *cobra.Command {
	var mode string
	var common commonFlags

	// project-mode flags
	var path, repoURL, commit, branch string

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan a project or module repo and report facts to TerraGraph",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch mode {
			case "project":
				return runProjectScan(common, path, repoURL, commit, branch)
			case "module-repo":
				return runModuleRepoScan(common, repoURL)
			default:
				return fmt.Errorf("--mode must be %q or %q, got %q", "project", "module-repo", mode)
			}
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "", "scan mode: project | module-repo (required)")
	_ = cmd.MarkFlagRequired("mode")

	cmd.Flags().StringVar(&path, "path", ".", "project mode: root directory of the Terraform config to scan")
	cmd.Flags().StringVar(&repoURL, "repo-url", "", "repo URL of the subject; auto-detected from git origin in project mode, required in module-repo mode")
	cmd.Flags().StringVar(&commit, "commit", "", "project mode: commit SHA; auto-detected from local git if omitted")
	cmd.Flags().StringVar(&branch, "branch", "", "project mode: branch name; auto-detected from local git if omitted")

	cmd.Flags().StringVar(&common.apiURL, "api-url", envDefault("TERRAGRAPH_API_URL", ""), "TerraGraph server base URL (env TERRAGRAPH_API_URL)")
	cmd.Flags().StringVar(&common.token, "token", envDefault("TERRAGRAPH_TOKEN", ""), "auth token for the TerraGraph server (env TERRAGRAPH_TOKEN)")
	cmd.Flags().StringVar(&common.out, "out", "", "also write the JSON report to this file")
	cmd.Flags().BoolVar(&common.dryRun, "dry-run", false, "build the report and print/save it, but don't submit it to the server")

	return cmd
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runProjectScan(common commonFlags, path, repoURL, commit, branch string) error {
	if repoURL == "" {
		repoURL = gitinfo.RemoteURL(path, "origin")
	}
	if commit == "" {
		commit = gitinfo.CommitSHA(path)
	}
	if branch == "" {
		branch = gitinfo.Branch(path)
	}

	calls, err := hclscan.Scan(path)
	if err != nil {
		return err
	}

	manifest, err := moduleinit.Load(path)
	if err != nil {
		// Non-fatal: a malformed/partial modules.json shouldn't block the
		// scan, it just means we fall back to source-parsed refs.
		fmt.Fprintf(os.Stderr, "warning: reading .terraform/modules/modules.json: %v\n", err)
		manifest = nil
	}

	facts := make([]report.Fact, 0, len(calls))
	for _, c := range calls {
		resolved := manifest.ResolvedCommit(c.CallName)
		resolutionSource := report.ResolutionSourceParse
		refResolved := c.RefDeclared
		if resolved != "" {
			resolutionSource = report.ResolutionSourceModulesJSON
			refResolved = resolved
		}

		facts = append(facts, report.Fact{
			Type:             report.FactTypeModuleCall,
			CallName:         c.CallName,
			Source:           c.Source,
			RefDeclared:      c.RefDeclared,
			RefResolved:      refResolved,
			ResolutionSource: resolutionSource,
			File:             c.File,
			Line:             c.Line,
		})
	}

	r := report.New(report.ScannerTypeModuleUsage, report.Subject{
		Kind:      report.SubjectKindProject,
		RepoURL:   repoURL,
		CommitSHA: commit,
		Branch:    branch,
	}, facts)

	return finish(r, common)
}

func runModuleRepoScan(common commonFlags, repoURL string) error {
	if repoURL == "" {
		return fmt.Errorf("--repo-url is required in module-repo mode")
	}

	tags, err := gittags.List(repoURL)
	if err != nil {
		return err
	}

	facts := make([]report.Fact, 0, len(tags))
	for _, t := range tags {
		facts = append(facts, report.Fact{
			Type:      report.FactTypeVersionTag,
			Tag:       t.Name,
			SemVer:    t.SemVer,
			CommitSHA: t.CommitSHA,
		})
	}

	r := report.New(report.ScannerTypeModuleRepo, report.Subject{
		Kind:    report.SubjectKindModuleRepo,
		RepoURL: repoURL,
	}, facts)

	return finish(r, common)
}

// finish writes the report to --out (if set), submits it to the server
// (unless --dry-run or no --api-url), and otherwise prints it to stdout so
// the command is useful standalone before a server exists.
func finish(r report.ScanReport, common commonFlags) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}
	data := buf.Bytes()

	if common.out != "" {
		if err := os.WriteFile(common.out, data, 0o644); err != nil {
			return fmt.Errorf("writing report to %s: %w", common.out, err)
		}
		fmt.Fprintf(os.Stderr, "wrote report (%d facts) to %s\n", len(r.Facts), common.out)
	}

	if common.dryRun || common.apiURL == "" {
		if common.out == "" {
			os.Stdout.Write(data)
		}
		return nil
	}

	c := client.New(common.apiURL, common.token)
	if err := c.SubmitScan(r); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "submitted scan (%d facts) to %s\n", len(r.Facts), common.apiURL)
	return nil
}
