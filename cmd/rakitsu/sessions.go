package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/spf13/cobra"
)

var (
	sessionsLimit     int
	sessionsResumable bool
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List recent agent sessions",
	Long: `List recent sessions recorded by rakitsu run.

Use the session ID with --resume to continue a failed or interrupted pipeline run.

Examples:
  rakitsu sessions                  # list last 20 sessions
  rakitsu sessions --resumable      # show only sessions with a checkpoint
  rakitsu sessions --limit 5        # show last 5 sessions
  rakitsu run config.yaml "query" --resume <SESSION ID>`,
	RunE: runSessions,
}

func init() {
	sessionsCmd.Flags().IntVar(&sessionsLimit, "limit", 20, "max sessions to show")
	sessionsCmd.Flags().BoolVar(&sessionsResumable, "resumable", false, "show only sessions with a resumable checkpoint")
	rootCmd.AddCommand(sessionsCmd)
}

func runSessions(_ *cobra.Command, _ []string) error {
	ss, err := store.NewSessionStore()
	if err != nil {
		return fmt.Errorf("cannot open session store: %w\n  hint: check disk space and permissions on ~/.rakitsu", err)
	}

	sessions, err := ss.ListSessions()
	if err != nil {
		return fmt.Errorf("cannot list sessions: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION ID\tNAME\tSTATUS\tDURATION\tRESUMABLE\tQUERY")

	shown := 0
	for _, s := range sessions {
		if shown >= sessionsLimit {
			break
		}
		resumable := ss.HasCheckpoint(s.ID)
		if sessionsResumable && !resumable {
			continue
		}

		dur := sessionFmtDuration(s.DurationMs)
		resumeFlag := ""
		if resumable {
			resumeFlag = "yes"
		}
		query := s.Query
		if len(query) > 50 {
			query = query[:47] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Name, string(s.Status), dur, resumeFlag, query)
		shown++
	}
	w.Flush()

	if shown == 0 {
		fmt.Println("No sessions found.")
		return nil
	}
	fmt.Printf("\nTo resume a pipeline: rakitsu run <config.yaml> <query> --resume <SESSION ID>\n")
	return nil
}

func sessionFmtDuration(ms int64) string {
	if ms == 0 {
		return "-"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}
