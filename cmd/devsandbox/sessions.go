package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"devsandbox/internal/session"
)

func newSessionsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List running sandbox sessions",
		Long:  "List running sandbox sessions and their forwarded ports",
		Example: `  devsandbox sessions
  devsandbox sessions --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := session.DefaultStore()
			if err != nil {
				return err
			}

			store.CleanStale()

			sessions, err := store.ListLive()
			if err != nil {
				return err
			}

			if len(sessions) == 0 {
				fmt.Println("No running sandbox sessions.")
				return nil
			}

			if jsonOutput {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(sessions)
			}

			return printSessionsTable(sessions)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func printSessionsTable(sessions []*session.Session) error {
	table := tablewriter.NewWriter(os.Stdout)
	table.Header("NAME", "PID", "STARTED", "WORK DIR", "FORWARDED PORTS")

	for _, s := range sessions {
		workDir := s.WorkDir
		if len(workDir) > 40 {
			workDir = "..." + workDir[len(workDir)-37:]
		}

		_ = table.Append(
			s.Name,
			strconv.Itoa(s.PID),
			formatTimeAgo(s.StartedAt),
			workDir,
			formatPorts(s.ForwardedPorts),
		)
	}

	return table.Render()
}

func formatPorts(ports []session.ForwardedPort) string {
	if len(ports) == 0 {
		return "(none)"
	}

	var b strings.Builder
	for i, p := range ports {
		if i > 0 {
			b.WriteString(", ")
		}
		if p.HostPort == p.SandboxPort {
			b.WriteString(strconv.Itoa(p.HostPort))
		} else {
			b.WriteString(strconv.Itoa(p.HostPort))
			b.WriteString("→")
			b.WriteString(strconv.Itoa(p.SandboxPort))
		}
	}
	return b.String()
}

func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02 15:04")
	}
}
