package cli

import (
	"errors"
	"strings"
	"testing"

	"send-cli/internal/config"
)

func TestDoctorChecksReadinessWithoutExposingSecrets(t *testing.T) {
	cfg := config.Config{Telegram: config.TelegramConfig{
		BotToken: "telegram-secret",
		Receiver: config.Receiver{ID: 42, Title: "Team"},
	}}
	environment := map[string]string{
		"SEND_WHATSAPP_TOKEN":           "whatsapp-secret",
		"SEND_WHATSAPP_PHONE_NUMBER_ID": "12345",
	}
	checks := doctorChecks(
		cfg,
		func(name string) string { return environment[name] },
		func(name string) (string, error) { return "/tools/" + name, nil },
	)
	if len(checks) != 6 {
		t.Fatalf("checks = %d, want 6", len(checks))
	}
	for _, check := range checks {
		if !check.Ready {
			t.Errorf("%s unexpectedly not ready: %#v", check.Channel, check)
		}
		text := check.Status + " " + check.Detail
		if strings.Contains(text, "telegram-secret") || strings.Contains(text, "whatsapp-secret") {
			t.Fatalf("%s check exposed a secret: %q", check.Channel, text)
		}
	}
}

func TestDoctorChecksReportMissingSetup(t *testing.T) {
	checks := doctorChecks(
		config.Config{},
		func(string) string { return "" },
		func(string) (string, error) { return "", errors.New("missing") },
	)
	for _, channel := range []string{"telegram", "whatsapp", "adb", "ssh"} {
		check := findDoctorCheck(t, checks, channel)
		if check.Ready {
			t.Errorf("%s unexpectedly ready: %#v", channel, check)
		}
	}
	for _, channel := range []string{"http", "lan"} {
		if check := findDoctorCheck(t, checks, channel); !check.Ready {
			t.Errorf("%s unexpectedly unavailable: %#v", channel, check)
		}
	}
}

func findDoctorCheck(t *testing.T, checks []doctorCheck, channel string) doctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.Channel == channel {
			return check
		}
	}
	t.Fatalf("missing %s check", channel)
	return doctorCheck{}
}
