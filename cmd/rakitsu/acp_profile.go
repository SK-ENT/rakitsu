package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const acpOpenAIBaseURL = "https://api.openai.com/v1"
const acpAnthropicBaseURL = "https://api.anthropic.com/v1"
const acpGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
const acpOllamaBaseURL = "http://127.0.0.1:11434"

type acpProfile struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Credential     string `json:"credential,omitempty"`
	PendingCleanup string `json:"pending_cleanup,omitempty"`
}
type acpSetup struct {
	profilePath  string
	getKey       func(string) (string, error)
	setKey       func(string, string) error
	deleteKey    func(string) error
	writeProfile func(string, acpProfile) error
	verify       func(context.Context, acpProfile, string) error
}

func acpProfilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".rakitsu", "acp-profile.json")
}
func acpDeleteKey(account string) error        { return keyring.Delete(acpKeyringService, account) }
func acpGetKey(account string) (string, error) { return keyring.Get(acpKeyringService, account) }
func acpSetKey(account, key string) error      { return keyring.Set(acpKeyringService, account, key) }
func defaultACPSetup() acpSetup {
	return acpSetup{profilePath: acpProfilePath(), getKey: acpGetKey, setKey: acpSetKey, deleteKey: acpDeleteKey, verify: verifyACPProfile}
}
func validACPProfile(p acpProfile) bool {
	switch p.Provider {
	case "openai", "anthropic", "gemini", "ollama":
	default:
		return false
	}
	if len(p.Model) == 0 || len(p.Model) > 200 || strings.TrimSpace(p.Model) != p.Model {
		return false
	}
	for _, r := range p.Model {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/", r)) {
			return false
		}
	}
	return true
}
func acpConfigForProfile(p acpProfile, key string) *config.Config {
	cfg := acpDefaultConfig(key)
	cfg.Settings.ManagedACP = true
	cfg.Settings.DefaultProvider = p.Provider
	cfg.Settings.APIKeys = map[string]string{p.Provider: key}
	cfg.Settings.BaseURLs = map[string]string{}
	switch p.Provider {
	case "openai":
		cfg.Settings.BaseURLs[p.Provider] = acpOpenAIBaseURL
	case "anthropic":
		cfg.Settings.BaseURLs[p.Provider] = strings.TrimSuffix(acpAnthropicBaseURL, "/v1")
	case "gemini":
		cfg.Settings.BaseURLs[p.Provider] = strings.TrimSuffix(acpGeminiBaseURL, "/v1beta")
	case "ollama":
		cfg.Settings.BaseURLs[p.Provider] = acpOllamaBaseURL + "/v1"
	}
	cfg.Agents[0].Provider = p.Provider
	cfg.Agents[0].Model = p.Model
	if p.Provider != "openai" || p.Model != "gpt-5.6-luna" {
		cfg.Agents[0].ModelConfig = nil
	}
	return cfg
}
func managedACPAccount(p acpProfile) string {
	if p.Provider == "ollama" || p.Credential == "" {
		return ""
	}
	return p.Provider + ":" + p.Credential
}

func validACPCleanupAccount(account string) bool {
	provider, id, _ := strings.Cut(account, ":")
	switch provider {
	case "openai", "anthropic", "gemini":
		parsed, err := uuid.Parse(id)
		return err == nil && parsed.String() == id
	default:
		return false
	}
}

func readACPProfile(path string) (acpProfile, error) {
	var p acpProfile
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if len(data) > 4096 || json.Unmarshal(data, &p) != nil || !validACPProfile(p) {
		return p, errors.New("invalid profile")
	}
	if p.Credential != "" && !validACPCleanupAccount(managedACPAccount(p)) {
		return p, errors.New("invalid profile")
	}
	if p.PendingCleanup != "" && (!validACPCleanupAccount(p.PendingCleanup) || p.PendingCleanup == managedACPAccount(p)) {
		return p, errors.New("invalid profile")
	}
	return p, nil
}

func (s acpSetup) lockProfile() (func(), error) {
	if s.profilePath == "" {
		return nil, errors.New("profile unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(s.profilePath), 0700); err != nil {
		return nil, errors.New("could not open provider profile")
	}
	unlock, err := lockACPProfileFile(s.profilePath + ".lock")
	if err != nil {
		return nil, errors.New("could not lock provider profile")
	}
	return unlock, nil
}

func (s acpSetup) load() (*config.Config, error) {
	unlock, err := s.lockProfile()
	if err != nil {
		return nil, err
	}
	defer unlock()

	p, err := readACPProfile(s.profilePath)
	if os.IsNotExist(err) {
		key, e := s.getKey("openai")
		if e != nil || key == "" {
			return nil, errors.New("setup required")
		}
		return acpConfigForProfile(acpProfile{Provider: "openai", Model: "gpt-5.6-luna"}, key), nil
	}
	if err != nil {
		return nil, errors.New("profile unavailable")
	}
	var key string
	if p.Provider != "ollama" {
		account := p.Provider
		if p.Credential != "" {
			account += ":" + p.Credential
		}
		key, err = s.getKey(account)
		if err != nil || key == "" {
			return nil, errors.New("setup required")
		}
	}
	return acpConfigForProfile(p, key), nil
}
func (s acpSetup) save(ctx context.Context, p acpProfile, key string) error {
	if p.Provider == "gemini" {
		p.Model = strings.TrimPrefix(p.Model, "models/")
	}
	if !validACPProfile(p) || s.profilePath == "" {
		return errors.New("invalid provider or model")
	}
	key = strings.TrimSpace(key)
	if p.Provider != "ollama" && key == "" {
		return errors.New("API key cannot be empty")
	}
	if err := s.verify(ctx, p, key); err != nil {
		return errors.New("could not verify provider and model; check your selection and connection")
	}
	unlock, err := s.lockProfile()
	if err != nil {
		return err
	}
	defer unlock()

	previous, previousErr := readACPProfile(s.profilePath)
	p.PendingCleanup = ""
	if previousErr == nil {
		if err := s.removeSupersededKey(previous.PendingCleanup); err != nil {
			return errors.New("could not remove the old credential; restore access to the system credential store and retry setup")
		}
		// This reference is only staged in memory. If the replacement write
		// fails, the prior profile still points to the already deleted key.
		p.PendingCleanup = managedACPAccount(previous)
	}
	account := ""
	if p.Provider != "ollama" {
		p.Credential = uuid.NewString()
		account = p.Provider + ":" + p.Credential
		if err := s.setKey(account, key); err != nil {
			return errors.New("could not save API key to the system credential store")
		}
	} else {
		p.Credential = ""
	}
	writeProfile := s.writeProfile
	if writeProfile == nil {
		writeProfile = writeACPProfile
	}
	if err := writeProfile(s.profilePath, p); err != nil {
		if account != "" && s.deleteKey != nil {
			_ = s.deleteKey(account)
		}
		return errors.New("could not save provider profile")
	}
	if p.PendingCleanup != "" {
		if err := s.removeSupersededKey(p.PendingCleanup); err != nil {
			fmt.Fprintln(os.Stderr, "Provider saved. Run setup again to retry removing the old credential.")
		} else {
			p.PendingCleanup = ""
			// If this write fails, the saved reference is safe to retry.
			_ = writeProfile(s.profilePath, p)
		}
	}
	return nil
}

func (s acpSetup) removeSupersededKey(account string) error {
	if account == "" {
		return nil
	}
	if s.deleteKey == nil {
		return errors.New("credential store unavailable")
	}
	err := s.deleteKey(account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func writeACPProfile(path string, p acpProfile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".acp-profile-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(p)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func acpHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func ollamaACPModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, acpOllamaBaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := acpHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("ollama unavailable")
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	names := []string{}
	for _, m := range body.Models {
		if validACPProfile(acpProfile{Provider: "ollama", Model: m.Name}) {
			names = append(names, m.Name)
		}
	}
	return names, nil
}
func verifyACPProfile(ctx context.Context, p acpProfile, key string) error {
	if p.Provider == "ollama" {
		names, err := ollamaACPModels(ctx)
		if err != nil {
			return err
		}
		for _, name := range names {
			if name == p.Model || name == p.Model+":latest" {
				return nil
			}
		}
		return errors.New("model not installed")
	}
	var endpoint string
	switch p.Provider {
	case "openai":
		endpoint = acpOpenAIBaseURL
	case "anthropic":
		endpoint = acpAnthropicBaseURL
	case "gemini":
		endpoint = acpGeminiBaseURL
	default:
		return errors.New("invalid provider")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/models/"+url.PathEscape(p.Model), nil)
	if err != nil {
		return err
	}
	switch p.Provider {
	case "openai":
		req.Header.Set("Authorization", "Bearer "+key)
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		req.Header.Set("x-goog-api-key", key)
	}
	resp, err := acpHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("verification failed")
	}
	return nil
}
func acpLogin() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("login requires an interactive terminal")
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Fprintln(os.Stderr, "Choose a provider: 1) OpenAI  2) Anthropic  3) Gemini  4) Ollama")
	fmt.Fprint(os.Stderr, "Provider [1]: ")
	choice, err := in.ReadString('\n')
	if err != nil {
		return errors.New("could not read provider")
	}
	var p acpProfile
	switch strings.TrimSpace(choice) {
	case "", "1", "openai":
		p = acpProfile{Provider: "openai", Model: "gpt-5.6-luna"}
	case "2", "anthropic":
		p = acpProfile{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	case "3", "gemini":
		p = acpProfile{Provider: "gemini", Model: "gemini-2.5-flash"}
	case "4", "ollama":
		p = acpProfile{Provider: "ollama"}
	default:
		return errors.New("unknown provider")
	}
	if p.Provider == "ollama" {
		names, e := ollamaACPModels(context.Background())
		if e != nil || len(names) == 0 {
			return errors.New("could not find installed Ollama models; start Ollama and install a model")
		}
		fmt.Fprintln(os.Stderr, "Installed models:")
		for _, n := range names {
			fmt.Fprintln(os.Stderr, "  "+n)
		}
		p.Model = names[0]
	}
	fmt.Fprintf(os.Stderr, "Model [%s]: ", p.Model)
	model, err := in.ReadString('\n')
	if err != nil {
		return errors.New("could not read model")
	}
	if model = strings.TrimSpace(model); model != "" {
		p.Model = model
	}
	var key []byte
	if p.Provider != "ollama" {
		fmt.Fprint(os.Stderr, "API key: ")
		key, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return errors.New("could not read API key")
		}
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if err = defaultACPSetup().save(context.Background(), p, string(key)); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Provider setup saved.")
	return nil
}
