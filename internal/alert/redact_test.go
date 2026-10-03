package alert

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	hex := strings.Repeat("abcd0123", 4)
	base64 := strings.Repeat("Ab9+/", 7) + "=="
	tests := []struct {
		name    string
		input   string
		secrets []string
		want    string
	}{
		{"empty", "", nil, ""},
		{"plain", "ordinary text", []string{""}, "ordinary text"},
		{"explicit", "a password and password", []string{"", "password", "password"}, "a [redacted] and [redacted]"},
		{"overlap", "abcdef abc", []string{"abc", "abcdef"}, "[redacted] [redacted]"},
		{"literal", "a.$b", []string{"a.$b"}, "[redacted]"},
		{"no replacement rescan", "password", []string{"password", "redacted"}, "[redacted]"},
		{"bearer", "Authorization: Bearer abc.def_123+/=", nil, "Authorization: Bearer [redacted]"},
		{"bearer case", "bearer\tsecret-token", nil, "Bearer [redacted]"},
		{"providers", "sk-proj-abc gh" + "p_ABC123 xoxb-123-abc xoxp-abc xox-abc AK" + "IA0123456789ABCDEF", nil, "[redacted] [redacted] [redacted] [redacted] [redacted] [redacted]"},
		{"assigned", "token=" + hex + " key=" + base64 + " SECRET=" + hex, nil, "token=[redacted] key=[redacted] SECRET=[redacted]"},
		{"short assigned", "token=" + strings.Repeat("a", 31), nil, "token=" + strings.Repeat("a", 31)},
		{"unassigned", hex, nil, hex},
		{"homes", `/Users/alice/project /home/bob/log "/Users/carol"`, nil, `~/project ~/log "~"`},
		{"other paths", "/usr/local/bin /home/ /Users/", nil, "/usr/local/bin /home/ /Users/"},
		{"combined", "/home/alice/file token=" + hex + " password", []string{"password"}, "~/file token=[redacted] [redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.input, tt.secrets); got != tt.want {
				t.Errorf("Redact() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	for _, tt := range []struct {
		s    string
		n    int
		want string
	}{
		{"", 3, ""},
		{"abc", -1, ""},
		{"abc", 0, ""},
		{"abc", 2, "ab"},
		{"abc", 3, "abc"},
		{"abc", 4, "abc"},
		{"a界🙂z", 3, "a界🙂"},
		{"界🙂", 1, "界"},
	} {
		if got := Truncate(tt.s, tt.n); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}
