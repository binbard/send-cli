package adbchannel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseDevices(t *testing.T) {
	devices := parseDevices("List of devices attached\r\nabc\tdevice product:test model:Phone\r\ndef\tunauthorized\r\n")
	want := []Device{
		{Serial: "abc", State: "device", Info: "product:test model:Phone"},
		{Serial: "def", State: "unauthorized"},
	}
	if !reflect.DeepEqual(devices, want) {
		t.Fatalf("devices = %#v, want %#v", devices, want)
	}
}

func TestSendAutoSelectsDeviceAndPushesInOrder(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "one.txt")
	second := filepath.Join(directory, "two.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var calls [][]string
	client := &Client{Executable: "adb", Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return []byte("List of devices attached\nabc\tdevice model:Phone\n"), nil
		}
		return []byte("1 file pushed"), nil
	}}
	result, err := client.Send(context.Background(), "", "/sdcard/Test/", []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Serial != "abc" || result.Sent != 2 {
		t.Fatalf("result = %#v", result)
	}
	wantFirstPush := []string{"-s", "abc", "push", first, "/sdcard/Test/"}
	if !reflect.DeepEqual(calls[1], wantFirstPush) {
		t.Fatalf("first push = %#v, want %#v", calls[1], wantFirstPush)
	}
}

func TestSendRequiresSerialForMultipleDevices(t *testing.T) {
	file := filepath.Join(t.TempDir(), "one.txt")
	if err := os.WriteFile(file, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{Executable: "adb", Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("List of devices attached\na\tdevice\nb\tdevice\n"), nil
	}}
	_, err := client.Send(context.Background(), "", "", []string{file})
	if err == nil || !strings.Contains(err.Error(), "--serial (a, b)") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendReportsPartialFailure(t *testing.T) {
	directory := t.TempDir()
	files := []string{filepath.Join(directory, "one.txt"), filepath.Join(directory, "two.txt")}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	client := &Client{Executable: "adb", Run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 2 {
			return []byte("device offline"), errors.New("exit status 1")
		}
		return nil, nil
	}}
	_, err := client.Send(context.Background(), "chosen", "", files)
	if err == nil || !strings.Contains(err.Error(), "sent 1 of 2 files") || !strings.Contains(err.Error(), "device offline") {
		t.Fatalf("error = %v", err)
	}
}
