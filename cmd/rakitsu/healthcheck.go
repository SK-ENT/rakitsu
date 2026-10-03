package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// healthcheckCmd is the "rakitsu healthcheck" command.
// It queries the /healthz endpoint and exits 0 if healthy, 1 otherwise.
var healthcheckCmd = &cobra.Command{
	Use:   "healthcheck",
	Short: "Check the health of a running rakitsu serve instance",
	Long: `Check the health of a running rakitsu serve instance via /healthz.
Exits 0 if all monitors are healthy, 1 otherwise.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		url, err := cmd.Flags().GetString("url")
		if err != nil {
			return err
		}

		timeout, err := cmd.Flags().GetDuration("timeout")
		if err != nil {
			return err
		}

		client := &http.Client{Timeout: timeout}
		resp, err := client.Get(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: failed to reach %s: %v\n", url, err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: failed to read response: %v\n", err)
			os.Exit(1)
		}

		var result struct {
			Status   string            `json:"status"`
			Monitors map[string]string `json:"monitors"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: invalid JSON response: %v\n", err)
			os.Exit(1)
		}

		if result.Status != "ok" {
			fmt.Fprintf(os.Stderr, "healthcheck: status is not ok: %s\n", result.Status)
			if len(result.Monitors) > 0 {
				fmt.Fprintf(os.Stderr, "unhealthy monitors:\n")
				for id, state := range result.Monitors {
					if state != "ok" {
						fmt.Fprintf(os.Stderr, "  %s: %s\n", id, state)
					}
				}
			}
			os.Exit(1)
		}

		os.Exit(0)
		return nil
	},
}

func init() {
	healthcheckCmd.Flags().String("url", "http://localhost:9100/healthz", "URL of the /healthz endpoint")
	healthcheckCmd.Flags().Duration("timeout", 5*time.Second, "request timeout")
	rootCmd.AddCommand(healthcheckCmd)
}
