package chat

import (
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

const (
	// historyMaxTurns is the max number of Q/A turns kept in a composed
	// history prefix. Older turns are dropped.
	historyMaxTurns = 10

	// historyMaxAssistantChars truncates each assistant response in the
	// composed prefix so enormous answers don't blow the context window.
	historyMaxAssistantChars = 2000
)

// buildQueryWithHistory prepends prior Q/A turns to a new user query so
// stateless orchestrators feel continuous across turns. Returns just
// `query` when history is empty.
//
// Format:
//
//	Previous conversation:
//	User: <q1>
//	Assistant: <a1>
//	User: <q2>
//	Assistant: <a2>
//
//	Current question:
//	<query>
//
// Keeps the last historyMaxTurns turns; truncates long assistant
// responses to historyMaxAssistantChars characters.
func buildQueryWithHistory(history []llm.Message, query string) string {
	if len(history) == 0 {
		return query
	}

	// Collect role-tagged pairs in order, keeping only the last N turns.
	type turn struct {
		user, assistant string
	}
	var turns []turn
	var pending turn
	havePending := false
	for _, m := range history {
		switch m.Role {
		case "user":
			if havePending {
				turns = append(turns, pending)
			}
			pending = turn{user: m.AsText()}
			havePending = true
		case "assistant":
			if havePending {
				pending.assistant = m.AsText()
				turns = append(turns, pending)
				pending = turn{}
				havePending = false
			}
		}
	}
	if havePending {
		turns = append(turns, pending)
	}

	if len(turns) > historyMaxTurns {
		turns = turns[len(turns)-historyMaxTurns:]
	}

	var sb strings.Builder
	sb.WriteString("Previous conversation:\n")
	for _, t := range turns {
		if t.user != "" {
			sb.WriteString("User: ")
			sb.WriteString(t.user)
			sb.WriteString("\n")
		}
		if t.assistant != "" {
			a := t.assistant
			if len(a) > historyMaxAssistantChars {
				a = a[:historyMaxAssistantChars] + "...[truncated]"
			}
			sb.WriteString("Assistant: ")
			sb.WriteString(a)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nCurrent question:\n")
	sb.WriteString(query)
	return sb.String()
}
