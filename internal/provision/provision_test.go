package provision

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"send-cli/internal/config"
)

func TestOneTimeEncryptedProvisioning(t *testing.T) {
	bundle := Bundle{Telegram: config.TelegramConfig{
		BotToken: "top-secret-token", Receiver: config.Receiver{Kind: "private", ID: 42},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type offerInfo struct{ address, code string }
	ready := make(chan offerInfo, 1)
	done := make(chan error, 1)
	go func() {
		done <- Offer(ctx, bundle, OfferOptions{Bind: "127.0.0.1", Ready: func(address, code string) {
			ready <- offerInfo{address, code}
		}})
	}()
	info := <-ready
	if len(info.code) != 22 {
		t.Fatalf("pairing code length = %d", len(info.code))
	}
	if _, err := Join(ctx, info.address, strings.Repeat("A", len(info.code))); err == nil {
		t.Fatal("wrong code was accepted")
	}
	got, err := Join(ctx, info.address, info.code)
	if err != nil {
		t.Fatal(err)
	}
	if got.Telegram.BotToken != bundle.Telegram.BotToken || got.Telegram.Receiver.ID != 42 {
		t.Fatalf("wrong bundle: %#v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := Join(ctx, info.address, info.code); err == nil {
		t.Fatal("pairing code was reusable")
	}
}

func TestProvisioningRejectsUnauthenticatedRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Offer(ctx, Bundle{Telegram: config.TelegramConfig{BotToken: "secret", Receiver: config.Receiver{ID: 1}}},
			OfferOptions{Bind: "127.0.0.1", Ready: func(address, _ string) { ready <- address }})
	}()
	address := <-ready
	resp, err := http.Post("http://"+address+path, "application/json", strings.NewReader(`{"nonce":"bad","proof":"bad"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("unauthenticated request succeeded")
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
		if _, exists := payload["telegram"]; exists {
			t.Fatal("unauthenticated response leaked credentials")
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("offer returned %v", err)
	}
}

func TestEncryptedPayloadCannotBeTamperedWith(t *testing.T) {
	secret := []byte("012345678901234567890123")
	clientNonce := []byte("01234567890123456789012345678901")
	sealed, err := encrypt(secret, clientNonce, []byte(`{"telegram":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(secret, []byte("different-nonce-0123456789012345"), sealed); err == nil {
		t.Fatal("modified request nonce was accepted")
	}
	if _, err := decrypt([]byte("another-secret-0123456789"), clientNonce, sealed); err == nil {
		t.Fatal("wrong secret decrypted the bundle")
	}
}

func TestPairingCodeFormats(t *testing.T) {
	secret, err := decodeCode("AAAAAAAAAAAAAAAAAAAAAA")
	if err != nil || len(secret) != 16 {
		t.Fatalf("short code decoded to %d bytes: %v", len(secret), err)
	}
	legacy := hex.EncodeToString([]byte("012345678901234567890123"))
	secret, err = decodeCode(legacy)
	if err != nil || len(secret) != 24 {
		t.Fatalf("legacy code decoded to %d bytes: %v", len(secret), err)
	}
	if _, err := decodeCode("too-short"); err == nil {
		t.Fatal("short weak code was accepted")
	}
}
