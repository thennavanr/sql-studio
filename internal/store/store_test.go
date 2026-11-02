package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memSecrets map[string]string

func (m memSecrets) Get(k string) (string, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (m memSecrets) Set(k, v string) error { m[k] = v; return nil }
func (m memSecrets) Delete(k string) error { delete(m, k); return nil }

func TestStore(t *testing.T) {
	dir := t.TempDir()
	sec := memSecrets{}
	s, err := Open(dir, sec)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Save(Connection{Server: "srv.database.windows.net", Database: "db", Auth: AuthSQL, User: "u", Password: "secret", SavePassword: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Name != "srv.database.windows.net / db" || c.Password != "" {
		t.Errorf("saved: %+v", c)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "connections.json"))
	if strings.Contains(string(b), "secret") {
		t.Fatal("password written to disk")
	}
	if sec[c.ID] != "secret" {
		t.Fatal("password not in keychain")
	}

	// Re-open and read back with the secret.
	s2, _ := Open(dir, sec)
	got, err := s2.Get(c.ID)
	if err != nil || got.Password != "secret" {
		t.Fatalf("get: %+v %v", got, err)
	}
	// Updating without a password keeps the stored one.
	got.Password = ""
	got.Name = "renamed"
	if _, err := s2.Save(got); err != nil || sec[c.ID] != "secret" {
		t.Fatal("password lost on update")
	}
	// Turning off SavePassword removes it.
	got.SavePassword = false
	_, _ = s2.Save(got)
	if _, ok := sec[c.ID]; ok {
		t.Fatal("password not removed")
	}
	if err := s2.Delete(c.ID); err != nil || len(s2.List()) != 0 {
		t.Fatal("delete failed")
	}

	if err := s2.AddHistory(HistoryEntry{Query: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	h, _ := s2.History()
	if len(h) != 1 || h[0].Query != "SELECT 1" {
		t.Fatalf("history %+v", h)
	}
	if s2.Settings().MaxRows != DefaultSettings().MaxRows {
		t.Fatal("default settings")
	}
}
