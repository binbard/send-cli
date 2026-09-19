//go:build windows

package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDPAPITokenRoundTrip(t *testing.T) {
	token := []byte("123456:secret-token")
	protected, err := protectToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(protected, token) {
		t.Fatal("protected value contains the plaintext token")
	}
	got, err := unprotectToken(protected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, token) {
		t.Fatalf("unprotected token = %q", got)
	}
}

func TestPlaintextTokenMigratesOnNextSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := []byte(`{"version":1,"telegram":{"bot_token":"legacy-secret","receiver":{"id":42}}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStoreAt(path)
	if err := store.Update(func(cfg *Config) error {
		cfg.Telegram.AutoDownload = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("legacy-secret")) || !bytes.Contains(data, []byte("bot_token_protected")) {
		t.Fatalf("plaintext token was not migrated: %s", data)
	}
}
