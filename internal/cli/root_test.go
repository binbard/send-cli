package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"send-cli/internal/config"
)

func TestHelpIncludesAllChannels(t *testing.T) {
	command := newRootCommand()
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram", "whatsapp", "adb", "ssh", "http", "lan", "doctor"} {
		if !strings.Contains(output.String(), name) {
			t.Errorf("help does not mention %q", name)
		}
	}
}

func TestHTTPCommand(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	command := newRootCommand()
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"http", server.URL, "-m", "hello"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotBody != "hello" || !strings.Contains(output.String(), "202 Accepted") {
		t.Fatalf("body=%q output=%q", gotBody, output.String())
	}
}

func TestTelegramAliases(t *testing.T) {
	command := newRootCommand()
	found, _, err := command.Find([]string{"tg"})
	if err != nil {
		t.Fatal(err)
	}
	if found.Name() != "telegram" {
		t.Fatalf("tg resolved to %q", found.Name())
	}
}

func TestWhatsAppAliases(t *testing.T) {
	command := newRootCommand()
	found, _, err := command.Find([]string{"wa"})
	if err != nil {
		t.Fatal(err)
	}
	if found.Name() != "whatsapp" {
		t.Fatalf("wa resolved to %q", found.Name())
	}
	for _, name := range []string{"setup", "logout", "to", "message", "ask-downloads"} {
		if found.Flags().Lookup(name) == nil {
			t.Errorf("WhatsApp command missing --%s", name)
		}
	}
	for _, removed := range []string{"token", "phone-number-id", "graph-url", "api-version"} {
		if found.Flags().Lookup(removed) != nil {
			t.Errorf("WhatsApp command still exposes removed Cloud API flag --%s", removed)
		}
	}
}

func TestWhatsAppHelpExplainsInteractiveAndRememberedRecipient(t *testing.T) {
	command := newRootCommand()
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"wa", "--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"opens the interactive chat", "recipient is remembered", "last successful recipient", "SEND_WHATSAPP_TO"} {
		if !strings.Contains(output.String(), phrase) {
			t.Errorf("WhatsApp help does not mention %q:\n%s", phrase, output.String())
		}
	}
}

func TestWhatsAppRecipientUsesAndUpdatesDefault(t *testing.T) {
	store := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	if err := rememberWhatsAppRecipient(store, "+919876543210"); err != nil {
		t.Fatal(err)
	}
	got, err := whatsappRecipient(store, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "+919876543210" {
		t.Fatalf("remembered recipient = %q", got)
	}
	got, err = whatsappRecipient(store, " +14155550100 ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "+14155550100" {
		t.Fatalf("supplied recipient = %q", got)
	}
}
