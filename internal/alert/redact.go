package alert

import (
	"regexp"
	"sort"
	"strings"
)

var (
	bearerPattern        = regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]+`)
	tokenPattern         = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]+|ghp_[A-Za-z0-9_]+|xox[baprs]?-[A-Za-z0-9-]+|AKIA[A-Z0-9]+)`)
	assignedTokenPattern = regexp.MustCompile(`(?i)\b(token|key|secret)=([A-Za-z0-9+/=_-]{32,})`)
	homePattern          = regexp.MustCompile(`/(?:Users|home)/[^/\s"'<>]+`)
)

// Redact masks explicit secrets and common credential patterns, and replaces
// absolute home directory prefixes with ~. It is a best-effort sanitizer, not
// a guarantee that arbitrary sensitive data will be detected.
func Redact(s string, secrets []string) string {
	// Match overlapping secrets longest-first, and replace in one pass so a
	// secret cannot accidentally match text inserted by an earlier replacement.
	values := make([]string, 0, len(secrets))
	seen := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		if secret != "" && !seen[secret] {
			values = append(values, secret)
			seen[secret] = true
		}
	}
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	if len(values) > 0 {
		pairs := make([]string, 0, 2*len(values))
		for _, value := range values {
			pairs = append(pairs, value, "[redacted]")
		}
		s = strings.NewReplacer(pairs...).Replace(s)
	}
	s = bearerPattern.ReplaceAllString(s, "Bearer [redacted]")
	s = tokenPattern.ReplaceAllString(s, "[redacted]")
	s = assignedTokenPattern.ReplaceAllString(s, "${1}=[redacted]")
	return homePattern.ReplaceAllString(s, "~")
}

// Truncate returns at most n runes of s, without adding an ellipsis.
// Non-positive limits return an empty string.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for offset := range s {
		if count == n {
			return s[:offset]
		}
		count++
	}
	return s
}
