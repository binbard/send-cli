package lanchannel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiverAcceptsAuthenticatedTransfer(t *testing.T) {
	directory := t.TempDir()
	var gotMessage string
	var gotFiles []string
	receiver := &Receiver{
		Directory: directory, Token: "secret",
		OnMessage: func(message string) { gotMessage = message },
		OnFiles:   func(files []string) { gotFiles = append(gotFiles, files...) },
	}
	if err := receiver.prepare(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(receiver)
	defer server.Close()
	file := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(file, []byte("report-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Send(context.Background(), server.URL+"/send", "secret", "hello", []string{file}, time.Second); err != nil {
		t.Fatal(err)
	}
	if gotMessage != "hello" || len(gotFiles) != 1 {
		t.Fatalf("message=%q files=%#v", gotMessage, gotFiles)
	}
	data, err := os.ReadFile(gotFiles[0])
	if err != nil || string(data) != "report-data" {
		t.Fatalf("saved data=%q err=%v", data, err)
	}
}

func TestReceiverRejectsWrongToken(t *testing.T) {
	receiver := &Receiver{Directory: t.TempDir(), Token: "secret"}
	if err := receiver.prepare(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader("body"))
	request.Header.Set("Authorization", "Bearer wrong")
	response := httptest.NewRecorder()
	receiver.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestReceiverAvoidsFilenameCollisions(t *testing.T) {
	directory := t.TempDir()
	receiver := &Receiver{Directory: directory, Token: "secret"}
	if err := receiver.prepare(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(receiver)
	defer server.Close()
	file := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(file, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Send(context.Background(), server.URL+"/send", "secret", "", []string{file}, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"same.txt", "same (1).txt"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

func TestReceiverAcceptsMessageOnlyTransfer(t *testing.T) {
	var got string
	receiver := &Receiver{Directory: t.TempDir(), Token: "secret", OnMessage: func(message string) { got = message }}
	if err := receiver.prepare(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(receiver)
	defer server.Close()
	if err := Send(context.Background(), server.URL+"/send", "secret", "hello", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("message = %q", got)
	}
}

func TestResolveDirectTarget(t *testing.T) {
	got, err := ResolveTarget(context.Background(), "192.0.2.5:9000", 0, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.0.2.5:9000/send" {
		t.Fatalf("target = %q", got)
	}
}
