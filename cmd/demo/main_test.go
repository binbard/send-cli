package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactKeepsRealOutputExceptRecipientIdentifiers(t *testing.T) {
	input := "CHANNEL   ALIASES  STATUS        DEFAULT RECEIVER\n" +
		"telegram  tg       ready (bot)   The One (@real_name)\n" +
		"whatsapp  wa       ready (user)  +919876543210\n" +
		"ssh       —        ready         user@host required\n"
	want := "CHANNEL   ALIASES  STATUS        DEFAULT RECEIVER\n" +
		"telegram  tg       ready (bot)   The One (@***)\n" +
		"whatsapp  wa       ready (user)  ***\n" +
		"ssh       —        ready         user@host required\n"
	if got := redact("list", input); got != want {
		t.Fatalf("redacted list = %q, want %q", got, want)
	}

	message := "Sent 1 WhatsApp message(s), including 0 file(s), to +919876543210 (@real_name).\r\n"
	if got := redact("whatsapp", message); got != "Sent 1 WhatsApp message(s), including 0 file(s), to *** (@***).\n" {
		t.Fatalf("redacted send = %q", got)
	}
}

func TestMaskPhoneNumbersDoesNotPartiallyMaskLongIdentifier(t *testing.T) {
	input := "reference 1234567890123456, receiver 15551234567"
	if got := maskPhoneNumbers(input); got != "reference 1234567890123456, receiver ***" {
		t.Fatalf("maskPhoneNumbers = %q", got)
	}
}

func TestWriteTranscriptsReplacesFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo-output.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := transcripts{List: "list\n", Telegram: "tg\n", WhatsApp: "wa\n"}
	if err := writeTranscriptsTo(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"telegram": "tg\n"`) {
		t.Fatalf("written fixture = %q", data)
	}
}
