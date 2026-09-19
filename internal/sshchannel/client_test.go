package sshchannel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSendBuildsSafeSCPArguments(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "report file.txt")
	identity := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(file, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotExecutable string
	var gotArgs []string
	client := &Client{Executable: "custom-scp", Run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
		gotExecutable = executable
		gotArgs = append([]string(nil), args...)
		return nil, nil
	}}
	result, err := client.Send(context.Background(), Request{
		Target: "user@example.com", Destination: "/srv/drop/", Files: []string{file},
		Port: 2222, Identity: identity, Options: []string{"StrictHostKeyChecking=accept-new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 1 || gotExecutable != "custom-scp" {
		t.Fatalf("result=%#v executable=%q", result, gotExecutable)
	}
	want := []string{
		"-o", "BatchMode=yes", "-P", "2222", "-i", identity,
		"-o", "StrictHostKeyChecking=accept-new", "--", file, "user@example.com:/srv/drop/",
	}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestSendRejectsUnsafeRemoteValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(file, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{}
	for _, request := range []Request{
		{Target: "", Files: []string{file}},
		{Target: "-evil", Files: []string{file}},
		{Target: "host;command", Files: []string{file}},
		{Target: "host", Destination: "/tmp/a b", Files: []string{file}},
	} {
		if _, err := client.Send(context.Background(), request); err == nil {
			t.Errorf("request %#v unexpectedly succeeded", request)
		}
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
	client := &Client{Executable: "scp", Run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 2 {
			return []byte("connection lost"), errors.New("exit status 1")
		}
		return nil, nil
	}}
	_, err := client.Send(context.Background(), Request{Target: "host", Files: files})
	if err == nil || !strings.Contains(err.Error(), "sent 1 of 2 files") || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("error = %v", err)
	}
}
