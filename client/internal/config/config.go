// Package config manages the on-disk client configuration.
//
// The configuration lives in the user's config directory:
//
//	<UserConfigDir>/sdvc/config.json
//
// It holds global settings (server URL, poll interval, web UI port) and the
// list of repositories the user has defined locally. Per-repo sync state is
// persisted here as well so the client survives restarts.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RepoConfig is a single repository the user tracks on this machine.
type RepoConfig struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	User      string   `json:"user"`
	Repo      string   `json:"repo"`
	Folder    string   `json:"folder"`
	Processes []string `json:"processes"`
	Enabled   bool     `json:"enabled"`

	// Sync state, managed by the engine.
	LastSyncedContentHash string    `json:"lastSyncedContentHash"`
	LastSyncedVersion     int       `json:"lastSyncedVersion"`
	LastSyncTime          time.Time `json:"lastSyncTime"`
	LastError             string    `json:"lastError"`
}

// Config is the full persisted configuration.
type Config struct {
	ServerURL           string       `json:"serverURL"`
	PollIntervalSeconds int          `json:"pollIntervalSeconds"`
	WebPort             int          `json:"webPort"`
	Repos               []RepoConfig `json:"repos"`
}

func defaultConfig() Config {
	return Config{
		ServerURL:           "http://localhost:8080",
		PollIntervalSeconds: 60,
		WebPort:             8477,
		Repos:               []RepoConfig{},
	}
}

// Store provides concurrency-safe access to the configuration file.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// Path returns the configuration file path for this OS.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sdvc", "config.json"), nil
}

// Load reads the configuration, creating a default one if none exists.
func Load() (*Store, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return Open(path)
}

// Open reads (or creates) the configuration at an explicit path.
func Open(path string) (*Store, error) {
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cfg = defaultConfig()
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	s.applyDefaults()
	return s, nil
}

func (s *Store) applyDefaults() {
	d := defaultConfig()
	if s.cfg.ServerURL == "" {
		s.cfg.ServerURL = d.ServerURL
	}
	if s.cfg.PollIntervalSeconds <= 0 {
		s.cfg.PollIntervalSeconds = d.PollIntervalSeconds
	}
	if s.cfg.WebPort <= 0 {
		s.cfg.WebPort = d.WebPort
	}
	if s.cfg.Repos == nil {
		s.cfg.Repos = []RepoConfig{}
	}
}

// Get returns a deep copy of the current configuration.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneConfig(s.cfg)
}

// Update mutates the configuration under lock and persists it.
func (s *Store) Update(fn func(cfg *Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	s.applyDefaults()
	return s.saveLocked()
}

// UpdateRepo mutates a single repo (matched by ID) and persists it.
// It returns false if no repo with the given ID exists.
func (s *Store) UpdateRepo(id string, fn func(r *RepoConfig)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Repos {
		if s.cfg.Repos[i].ID == id {
			fn(&s.cfg.Repos[i])
			return true, s.saveLocked()
		}
	}
	return false, nil
}

// AddRepo appends a new repo, assigning it an ID, and persists it.
func (s *Store) AddRepo(r RepoConfig) (RepoConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	s.cfg.Repos = append(s.cfg.Repos, r)
	return r, s.saveLocked()
}

// RemoveRepo deletes a repo from the local config (never touches server data).
func (s *Store) RemoveRepo(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Repos {
		if s.cfg.Repos[i].ID == id {
			s.cfg.Repos = append(s.cfg.Repos[:i], s.cfg.Repos[i+1:]...)
			return true, s.saveLocked()
		}
	}
	return false, nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func cloneConfig(c Config) Config {
	out := c
	out.Repos = make([]RepoConfig, len(c.Repos))
	copy(out.Repos, c.Repos)
	for i := range out.Repos {
		procs := make([]string, len(c.Repos[i].Processes))
		copy(procs, c.Repos[i].Processes)
		out.Repos[i].Processes = procs
	}
	return out
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
