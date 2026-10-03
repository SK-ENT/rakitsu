package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSessionsDir(t *testing.T) {
	t.Setenv("RK_TEST_BASE", "/var/rk")
	t.Setenv("HOME", "/home/u")
	// t.Setenv records the original value so cleanup restores it; then unset for the "unset" case.
	t.Setenv("RK_TEST_UNSET", "")
	os.Unsetenv("RK_TEST_UNSET")
	cases := []struct {
		in string
		ok bool
	}{
		{"", true},
		{"/abs/dir", true},
		{"~/rk/sessions", true},
		{"${RK_TEST_BASE}/s", true},
		{"${RK_TEST_UNSET:-/fallback}/s", true},
		{"   ", false},
		{"relative/dir", false},
		{"${RK_TEST_UNSET}", false},
		{"${RK_TEST_UNSET}/s", false},
	}
	for _, c := range cases {
		c := c
		cfg := &Config{Settings: Settings{SessionsDir: c.in}}
		var got []string
		for _, e := range cfg.Validate() {
			if e.Field == "settings.sessions_dir" {
				got = append(got, e.Message)
			}
		}
		if (len(got) == 0) != c.ok {
			t.Errorf("%q: ok=%v, errors=%v", c.in, c.ok, got)
		}
	}
}

func TestResolveSessionsDir_Precedence(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	cfg := &Config{Settings: Settings{SessionsDir: "~/from-yaml"}}

	if d, err := ResolveSessionsDir("/from/flag", cfg); err != nil || d != "/from/flag" {
		t.Fatalf("flag: %q %v", d, err)
	}
	if d, err := ResolveSessionsDir("", cfg); err != nil || d != filepath.Join("/home/u", "from-yaml") {
		t.Fatalf("yaml: %q %v", d, err)
	}
	if d, err := ResolveSessionsDir("", &Config{}); err != nil || d != "" {
		t.Fatalf("default: %q %v", d, err)
	}
	if d, err := ResolveSessionsDir("", nil); err != nil || d != "" {
		t.Fatalf("nil cfg: %q %v", d, err)
	}
	if _, err := ResolveSessionsDir("", &Config{Settings: Settings{SessionsDir: "rel"}}); err == nil {
		t.Fatal("relative yaml value must fail")
	}
}

func TestResolveSessionsDirDefaultFallbackIsNotUnresolved(t *testing.T) {
	os.Unsetenv("RK_TEST_UNSET")
	for _, setEmpty := range []bool{false, true} {
		if setEmpty {
			t.Setenv("RK_TEST_UNSET", "")
		}
		cfg := &Config{Settings: Settings{SessionsDir: "${RK_TEST_UNSET:-/fallback}/s"}}
		got, err := ResolveSessionsDir("", cfg)
		if err != nil || got != "/fallback/s" {
			t.Fatalf("setEmpty=%v: got %q, err %v; want /fallback/s", setEmpty, got, err)
		}
	}
	cfg := &Config{Settings: Settings{SessionsDir: "${RK_TEST_UNSET:-}/s"}}
	if _, err := ResolveSessionsDir("", cfg); err == nil {
		t.Fatal("empty default must still be rejected")
	}
}
