package server

import (
	"fmt"
	"strings"
)

// HandleWakeCommand implements the /wake slash command for chat TUI and serve chat.
// Subcommands: /wake status, /wake stop, /wake resume
// These commands only work on wake-enabled sessions.
func HandleWakeCommand(s *ChatSession, args string) string {
	if s == nil {
		return "session not available"
	}

	// Verify this session has wake enabled
	if s.cfg == nil || !s.cfg.Settings.Wake.Enabled {
		return "wake is not enabled for this session"
	}

	args = strings.TrimSpace(args)
	parts := strings.Fields(args)

	if len(parts) == 0 {
		return "usage: /wake status|stop|resume"
	}

	cmd := parts[0]

	switch cmd {
	case "status":
		return formatWakeStatus(s)
	case "stop":
		return handleWakeStop(s)
	case "resume":
		return handleWakeResume(s)
	default:
		return fmt.Sprintf("unknown wake subcommand: %q (use status, stop, or resume)", cmd)
	}
}

// formatWakeStatus returns a formatted summary of the wake session status.
func formatWakeStatus(s *ChatSession) string {
	status := s.WakeStatus()

	var b strings.Builder
	b.WriteString("Wake Status:\n")
	b.WriteString(fmt.Sprintf("  State:              %s\n", status.State))
	if status.Label != "" {
		b.WriteString(fmt.Sprintf("  Label:              %s\n", status.Label))
	}
	b.WriteString(fmt.Sprintf("  Interval:           %d seconds\n", status.IntervalSeconds))
	b.WriteString(fmt.Sprintf("  Max turns/hour:     %d\n", status.MaxTurnsPerHour))
	b.WriteString(fmt.Sprintf("  Turns this hour:    %d\n", status.TurnsLastHour))
	b.WriteString(fmt.Sprintf("  Running tasks:      %d\n", status.RunningTasks))
	b.WriteString(fmt.Sprintf("  Next tick in:       %d seconds\n", status.NextTickInSeconds))

	if status.LastTick != nil {
		b.WriteString(fmt.Sprintf("  Last tick:          %s\n", status.LastTick.Format("2006-01-02 15:04:05 MST")))
	}
	if status.LastAlarm != nil {
		b.WriteString(fmt.Sprintf("  Last alarm:         %s\n", status.LastAlarm.Format("2006-01-02 15:04:05 MST")))
	}

	return strings.TrimRight(b.String(), "\n")
}

// handleWakeStop stops the wake session.
func handleWakeStop(s *ChatSession) string {
	if err := s.StopWake(); err != nil {
		return fmt.Sprintf("failed to stop wake: %v", err)
	}
	return "wake session stopped"
}

// handleWakeResume resumes a stopped wake session.
func handleWakeResume(s *ChatSession) string {
	if err := s.ResumeWake(); err != nil {
		return fmt.Sprintf("failed to resume wake: %v", err)
	}
	return "wake session resumed"
}
