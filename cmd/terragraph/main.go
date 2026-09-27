// Command terragraph is the TerraGraph scanner CLI: it runs inside a
// project's or module repo's CI/GitOps pipeline and reports module usage /
// version facts to a central TerraGraph server (see docs/design.md).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "terragraph",
		Short: "TerraGraph scanner - reports Terraform module usage/versions to a TerraGraph server",
	}
	root.AddCommand(newScanCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
