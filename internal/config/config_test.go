package config

import (
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "nested", "config.json"))
	want := Config{
		Telegram: TelegramConfig{
			Mode:     "bot",
			BotToken: "secret",
			Receiver: Receiver{Kind: "bot_chat", ID: 42, Title: "Test"},
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != currentVersion || got.Telegram.Receiver.ID != 42 || got.Telegram.BotToken != "secret" {
		t.Fatalf("unexpected config: %#v", got)
	}
}

func TestReady(t *testing.T) {
	if (TelegramConfig{Mode: "bot", BotToken: "x", Receiver: Receiver{ID: 1}}).Ready() != true {
		t.Fatal("bot config should be ready")
	}
	if (TelegramConfig{Mode: "user", Receiver: Receiver{ID: 1, Kind: "user"}}).Ready() != true {
		t.Fatal("user config should be ready")
	}
	if (TelegramConfig{Mode: "bot", Receiver: Receiver{ID: 1}}).Ready() {
		t.Fatal("bot without token must not be ready")
	}
}
