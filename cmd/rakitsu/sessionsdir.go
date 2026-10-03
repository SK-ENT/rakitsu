package main

import (
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/store"
)

// sessionsDirFlag is the value of --sessions-dir (serve, run, sessions).
var sessionsDirFlag string

const sessionsDirFlagHelp = "directory for session files (default: settings.sessions_dir, else ~/.rakitsu/sessions); give each concurrent instance its own"

// openSessionStore opens the session store honouring flag > settings > default.
func openSessionStore(cfg *config.Config) (*store.SessionStore, error) {
	dir, err := config.ResolveSessionsDir(sessionsDirFlag, cfg)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return store.NewSessionStore()
	}
	return store.NewSessionStoreAt(dir)
}
