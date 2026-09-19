package cli

import (
	"strings"
	"testing"
)

func TestHelpIncludesAllChannels(t *testing.T) {
	command := newRootCommand()
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram", "whatsapp", "adb", "ssh", "http", "lan"} {
		if !strings.Contains(output.String(), name) {
			t.Errorf("help does not mention %q", name)
		}
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
