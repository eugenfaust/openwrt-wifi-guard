package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Real libuci can omit empty string options and report exit 1 for `get`.
func TestDisabledConfigAllowsAbsentCredentialOptions(t *testing.T) {
	s, _ := fixture(t, "")
	if err := os.WriteFile(filepath.Join(s.Dir, "wifi-guard"), []byte("config wifi-guard 'main'\n option telegram_enabled '0'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background())
	if err != nil || got != (Settings{}) {
		t.Fatal("empty UCI credentials must be supported", err)
	}
}
func TestClearEmptyCredentialsWithLibUCISemantics(t *testing.T) {
	s, _ := fixture(t, "empty-is-missing")
	seed(t, s)
	if err := s.Save(context.Background(), Settings{}); err != nil {
		t.Fatal("clear rejected", err)
	}
	got, err := s.Load(context.Background())
	if err != nil || got != (Settings{}) {
		t.Fatal("clear not reloaded", err)
	}
}
