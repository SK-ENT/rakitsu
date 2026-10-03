// Wake-up timer turn text formatting for injected turns.
package wake

import (
	"fmt"
	"strings"
)

// BuildTurnText renders the fixed-format, clearly marked text of a timer-sourced turn.
func BuildTurnText(results []CheckResult) string {
	var b strings.Builder
	b.WriteString("[TIMER-SOURCED TURN, not from a user] Checks:\n")
	for _, r := range results {
		fmt.Fprintf(&b, "- %s=%s", r.Name, r.Status)
		if r.Label != "" {
			fmt.Fprintf(&b, " label=%s", r.Label)
		}
		if r.Changed {
			b.WriteString(" (changed)")
		}
		b.WriteString("\n")
		if r.Detail != "" {
			d := truncate(strings.ReplaceAll(r.Detail, "```", "'''"), 512)
			fmt.Fprintf(&b, "```\n%s\n```\n", d)
		}
	}
	b.WriteString("Report what you see. You cannot clear this alarm.")
	return b.String()
}
