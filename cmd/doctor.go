package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jesse-osiecki/claudenboxen/internal/prereq"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Report whether the host is ready to run claudenboxen",
	RunE: func(cmd *cobra.Command, args []string) error {
		report := prereq.Doctor(flagRunscPath, flagNetwork, flagIsolation)
		for _, c := range report.Checks {
			status := "OK  "
			if !c.OK {
				status = "FAIL"
			}
			fmt.Printf("[%s] %-40s %s\n", status, c.Name, c.Detail)
			if !c.OK && c.FixHint != "" {
				fmt.Printf("       -> %s\n", c.FixHint)
			}
		}
		if !report.OK() {
			os.Exit(1)
		}
		return nil
	},
}
