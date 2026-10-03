package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestOpenSessionStore_Precedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(func() { sessionsDirFlag = "" })
	yamlDir := filepath.Join(t.TempDir(), "yaml")
	flagDir := filepath.Join(t.TempDir(), "flag")
	cfg := &config.Config{Settings: config.Settings{SessionsDir: yamlDir}}

	sessionsDirFlag = flagDir
	ss, err := openSessionStore(cfg)
	if err != nil || ss.Dir() != flagDir {
		t.Fatalf("flag wins: %v %v", ss, err)
	}
	sessionsDirFlag = ""
	if ss, err = openSessionStore(cfg); err != nil || ss.Dir() != yamlDir {
		t.Fatalf("yaml next: %v %v", ss, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".rakitsu")); !os.IsNotExist(err) {
		t.Fatalf("default dir created despite overrides: %v", err)
	}
	if ss, err = openSessionStore(nil); err != nil || ss.Dir() != filepath.Join(home, ".rakitsu", "sessions") {
		t.Fatalf("default: %v %v", ss, err)
	}
}
