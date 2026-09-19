package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "nested", "config.json"))
	want := Config{
		Telegram: TelegramConfig{
			BotToken:                "secret",
			Receiver:                Receiver{Kind: "bot_chat", ID: 42, Title: "Test"},
			UpdateOffset:            123,
			UpdateOffsetInitialized: true,
		},
		WhatsApp: WhatsAppConfig{DefaultRecipient: "+919876543210"},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != currentVersion || got.Telegram.Receiver.ID != 42 || got.Telegram.BotToken != "secret" || got.Telegram.UpdateOffset != 123 || !got.Telegram.UpdateOffsetInitialized || got.WhatsApp.DefaultRecipient != "+919876543210" {
		t.Fatalf("unexpected config: %#v", got)
	}
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("secret")) || !bytes.Contains(data, []byte("bot_token_protected")) {
			t.Fatalf("Windows config did not protect token: %s", data)
		}
	}
}

func TestReady(t *testing.T) {
	if (TelegramConfig{BotToken: "x", Receiver: Receiver{ID: 1}}).Ready() != true {
		t.Fatal("bot config should be ready")
	}
	if (TelegramConfig{Receiver: Receiver{ID: 1}}).Ready() {
		t.Fatal("config without bot token must not be ready")
	}
	if (TelegramConfig{BotToken: "x"}).Ready() {
		t.Fatal("config without receiver must not be ready")
	}
}

func TestStoreUpdatePreservesOtherTelegramSettings(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	if err := store.Save(Config{Telegram: TelegramConfig{
		BotToken: "secret",
		Receiver: Receiver{ID: 42},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *Config) error {
		cfg.Telegram.UpdateOffset = 99
		cfg.Telegram.UpdateOffsetInitialized = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Telegram.BotToken != "secret" || got.Telegram.Receiver.ID != 42 || got.Telegram.UpdateOffset != 99 {
		t.Fatalf("unexpected config after update: %#v", got.Telegram)
	}
}
