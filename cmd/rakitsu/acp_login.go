package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const acpKeyringService = "rakitsu-acp"
const acpKeyringAccount = "openai"
const acpProviderBaseURL = "https://api.openai.com/v1"
const acpValidationURL = acpProviderBaseURL + "/models"

func acpDefaultConfig(key string) *config.Config {
	return &config.Config{
		Name:     "Rakitsu ACP",
		Settings: config.Settings{DefaultProvider: "openai", APIKeys: map[string]string{"openai": key}, BaseURLs: map[string]string{"openai": acpProviderBaseURL}},
		Agents:   []config.AgentDefinition{{Name: "rakitsu", Role: "worker", Provider: "openai", Model: "gpt-4o-mini", SystemPrompt: "You are a helpful assistant."}},
	}
}

func saveACPKey(ctx context.Context, key string, validate func(context.Context, string) error, store func(string) error) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key cannot be empty")
	}
	if err := validate(ctx, key); err != nil {
		return errors.New("could not validate API key; check the key and network connection")
	}
	if err := store(key); err != nil {
		return errors.New("could not save API key to the system credential store")
	}
	return nil
}

func validateACPKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, acpValidationURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("validation failed")
	}
	return nil
}

func acpLogin() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("login requires an interactive terminal")
	}
	fmt.Fprint(os.Stderr, "OpenAI API key: ")
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return errors.New("could not read API key")
	}
	err = saveACPKey(context.Background(), string(key), validateACPKey, func(value string) error { return keyring.Set(acpKeyringService, acpKeyringAccount, value) })
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "API key saved to your system credential store.")
	return nil
}
