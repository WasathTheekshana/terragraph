// Command terragraph is the TerraGraph scanner CLI: it scans a repo, part of
// one, or a folder of many repos, and reports Terraform module usage and
// versions to a TerraGraph server (see docs/design.md).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "terragraph",
		Short: "TerraGraph scanner - reports Terraform module usage/versions to a TerraGraph server",
		// main prints the error once; usage is only useful for flag mistakes.
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       buildVersion(),
	}
	root.AddCommand(newScanCmd())

	// The first Ctrl+C cancels the scan cleanly; a second one exits at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
