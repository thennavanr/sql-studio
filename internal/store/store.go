// Package store persists saved connection profiles and query history.
//
// Profiles are stored as JSON in the user's config directory
// (~/Library/Application Support/SQL Studio on macOS). Secrets (passwords,
// client secrets) are never written to that file; they live in the macOS
// Keychain via go-keyring.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const keychainService = "SQL Studio"

// AuthType enumerates the supported authentication methods.
type AuthType string

const (
	AuthSQL              AuthType = "sql"              // SQL Server login + password
	AuthEntraInteractive AuthType = "entraInteractive" // Browser sign-in (MFA friendly)
	AuthEntraAzCli       AuthType = "entraAzCli"       // Reuse `az login`
	AuthEntraDefault     AuthType = "entraDefault"     // DefaultAzureCredential chain
	AuthEntraSP          AuthType = "entraServicePrincipal"
	AuthEntraDeviceCode  AuthType = "entraDeviceCode"
)

// Connection is a saved connection profile.
type Connection struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Server   string   `json:"server"`
	Port     int      `json:"port,omitempty"`
	Database string   `json:"database,omitempty"`
	Auth     AuthType `json:"auth"`
	// User is the SQL login, Entra UPN (login hint), or service principal client ID.
	User     string `json:"user,omitempty"`
	TenantID string `json:"tenantId,omitempty"`
	// ClientID optionally overrides the public client app used for interactive / device-code sign-in.
	ClientID               string `json:"clientId,omitempty"`
	Encrypt                string `json:"encrypt,omitempty"` // "true" (default), "strict", "false"
	TrustServerCertificate bool   `json:"trustServerCertificate,omitempty"`
	ConnectTimeout         int    `json:"connectTimeout,omitempty"` // seconds
	ApplicationIntent      string `json:"applicationIntent,omitempty"`
	Color                  string `json:"color,omitempty"`
	Group                  string `json:"group,omitempty"`
	SavePassword           bool   `json:"savePassword"`

	// Password is only populated in transit (UI <-> backend); never persisted to disk.
	Password string `json:"password,omitempty"`
}

// HistoryEntry records an executed query.
type HistoryEntry struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connectionId"`
	Database     string    `json:"database"`
	Query        string    `json:"query"`
	DurationMs   int64     `json:"durationMs"`
	Error        string    `json:"error,omitempty"`
}

type fileData struct {
	Connections []Connection `json:"connections"`
	Settings    Settings     `json:"settings"`
}

// Settings are user preferences.
type Settings struct {
	Theme          string `json:"theme"` // "system", "light", "dark"
	FontSize       int    `json:"fontSize"`
	MaxRows        int    `json:"maxRows"`
	QueryTimeout   int    `json:"queryTimeout"` // seconds, 0 = none
	HistoryEnabled bool   `json:"historyEnabled"`
}

// DefaultSettings returns the settings used on first launch.
func DefaultSettings() Settings {
	return Settings{Theme: "system", FontSize: 13, MaxRows: 100000, QueryTimeout: 0, HistoryEnabled: true}
}

// Store is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	dir     string
	data    fileData
	secrets SecretStore
}

// SecretStore abstracts the keychain so tests can use an in-memory fake.
type SecretStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// Keychain stores secrets in the OS credential store (macOS Keychain).
type Keychain struct{}

func (Keychain) Get(key string) (string, error) { return keyring.Get(keychainService, key) }
func (Keychain) Set(key, v string) error        { return keyring.Set(keychainService, key, v) }
func (Keychain) Delete(key string) error {
	err := keyring.Delete(keychainService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// DefaultDir returns the application's config directory.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "SQL Studio"), nil
}

// Open loads (or initialises) the store in dir.
func Open(dir string, secrets SecretStore) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, secrets: secrets, data: fileData{Settings: DefaultSettings()}}
	b, err := os.ReadFile(s.path())
	if err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.data.Settings.MaxRows == 0 {
		s.data.Settings = DefaultSettings()
	}
	return s, nil
}

func (s *Store) path() string        { return filepath.Join(s.dir, "connections.json") }
func (s *Store) historyPath() string { return filepath.Join(s.dir, "history.jsonl") }

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// List returns all saved connections (without secrets), sorted by group then name.
func (s *Store) List() []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Connection, len(s.data.Connections))
	copy(out, s.data.Connections)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Get returns a connection with its secret (if saved) filled in.
func (s *Store) Get(id string) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data.Connections {
		if c.ID == id {
			if c.SavePassword && s.secrets != nil {
				if pw, err := s.secrets.Get(id); err == nil {
					c.Password = pw
				}
			}
			return c, nil
		}
	}
	return Connection{}, errors.New("connection not found")
}

// Save creates or updates a connection. The secret is stored in the keychain
// when SavePassword is set, and removed otherwise.
func (s *Store) Save(c Connection) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(c.Server) == "" {
		return c, errors.New("server is required")
	}
	if c.ID == "" {
		c.ID = newID()
	}
	if c.Name == "" {
		c.Name = c.Server
		if c.Database != "" {
			c.Name += " / " + c.Database
		}
	}
	if s.secrets != nil {
		if c.SavePassword && c.Password != "" {
			if err := s.secrets.Set(c.ID, c.Password); err != nil {
				return c, err
			}
		} else if !c.SavePassword {
			_ = s.secrets.Delete(c.ID)
		}
	}
	persisted := c
	persisted.Password = ""
	replaced := false
	for i := range s.data.Connections {
		if s.data.Connections[i].ID == c.ID {
			s.data.Connections[i] = persisted
			replaced = true
			break
		}
	}
	if !replaced {
		s.data.Connections = append(s.data.Connections, persisted)
	}
	return persisted, s.saveLocked()
}

// Delete removes a connection and its secret.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Connections[:0]
	for _, c := range s.data.Connections {
		if c.ID != id {
			out = append(out, c)
		}
	}
	s.data.Connections = out
	if s.secrets != nil {
		_ = s.secrets.Delete(id)
	}
	return s.saveLocked()
}

// Settings returns the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Settings
}

// SaveSettings persists settings.
func (s *Store) SaveSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.MaxRows <= 0 {
		st.MaxRows = DefaultSettings().MaxRows
	}
	if st.FontSize <= 0 {
		st.FontSize = DefaultSettings().FontSize
	}
	s.data.Settings = st
	return s.saveLocked()
}

const maxHistory = 500

// AddHistory appends an entry to the history log.
func (s *Store) AddHistory(h HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(h)
	_, err = f.Write(append(b, '\n'))
	return err
}

// History returns the most recent entries, newest first.
func (s *Store) History() ([]HistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.historyPath())
	if errors.Is(err, os.ErrNotExist) {
		return []HistoryEntry{}, nil
	} else if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	out := make([]HistoryEntry, 0, len(lines))
	for i := len(lines) - 1; i >= 0 && len(out) < maxHistory; i-- {
		var h HistoryEntry
		if json.Unmarshal([]byte(lines[i]), &h) == nil {
			out = append(out, h)
		}
	}
	// Compact the file occasionally so it does not grow without bound.
	if len(lines) > maxHistory*2 {
		var sb strings.Builder
		for i := len(out) - 1; i >= 0; i-- {
			b, _ := json.Marshal(out[i])
			sb.Write(b)
			sb.WriteByte('\n')
		}
		_ = os.WriteFile(s.historyPath(), []byte(sb.String()), 0o600)
	}
	return out, nil
}

// ClearHistory deletes all history.
func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.historyPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
