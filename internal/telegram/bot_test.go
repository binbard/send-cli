package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBotSendAndDownload(t *testing.T) {
	const token = "test-token"
	var gotText, gotCaption, gotFile string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + token + "/sendMessage":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			gotText = r.FormValue("text")
			writeBotResponse(t, w, true, map[string]any{"message_id": 1})
		case "/bot" + token + "/sendDocument":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			gotCaption = r.FormValue("caption")
			file, _, err := r.FormFile("document")
			if err != nil {
				t.Error(err)
			} else {
				data, readErr := io.ReadAll(file)
				if readErr != nil {
					t.Error(readErr)
				}
				gotFile = string(data)
				_ = file.Close()
			}
			writeBotResponse(t, w, true, map[string]any{"message_id": 2})
		case "/bot" + token + "/getFile":
			writeBotResponse(t, w, true, map[string]any{"file_path": "docs/report.txt"})
		case "/file/bot" + token + "/docs/report.txt":
			_, _ = io.WriteString(w, "downloaded")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newBotClient(token)
	client.baseURL = server.URL
	client.http = server.Client()

	if err := client.sendMessage(context.Background(), 42, "hello"); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(source, []byte("uploaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.sendFile(context.Background(), 42, source, "caption"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "download.txt")
	if err := client.download(context.Background(), "file-id", destination); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}

	if gotText != "hello" || gotCaption != "caption" || gotFile != "uploaded" || string(downloaded) != "downloaded" {
		t.Fatalf("text=%q caption=%q upload=%q download=%q", gotText, gotCaption, gotFile, downloaded)
	}
}

func TestBotRetriesTelegramRateLimit(t *testing.T) {
	const token = "test-token"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  http.StatusTooManyRequests,
				"description": "Too Many Requests",
				"parameters":  map[string]any{"retry_after": 0},
			})
			return
		}
		writeBotResponse(t, w, true, map[string]any{"message_id": 1})
	}))
	defer server.Close()

	client := newBotClient(token)
	client.baseURL = server.URL
	client.http = server.Client()
	client.retryBase = time.Millisecond

	if err := client.sendMessage(context.Background(), 42, "hello"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestBotRetriesTemporaryServerFailure(t *testing.T) {
	const token = "test-token"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		writeBotResponse(t, w, true, map[string]any{"message_id": 1})
	}))
	defer server.Close()

	client := newBotClient(token)
	client.baseURL = server.URL
	client.http = server.Client()
	client.retryBase = time.Millisecond

	if err := client.sendMessage(context.Background(), 42, "hello"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestBotAPIErrorMessages(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{http.StatusUnauthorized, "invalid or revoked"},
		{http.StatusForbidden, "blocked or removed"},
		{http.StatusConflict, "another process or webhook"},
		{http.StatusTooManyRequests, "rate limit"},
	}
	for _, test := range tests {
		err := (&botAPIError{code: test.code, description: "details"}).Error()
		if !strings.Contains(err, test.want) {
			t.Errorf("error for %d = %q, want it to contain %q", test.code, err, test.want)
		}
	}
}

func TestBotRetriesInterruptedDownload(t *testing.T) {
	const token = "test-token"
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + token + "/getFile":
			writeBotResponse(t, w, true, map[string]any{"file_path": "docs/report.txt"})
		case "/file/bot" + token + "/docs/report.txt":
			if downloads.Add(1) == 1 {
				w.Header().Set("Content-Length", "20")
				_, _ = io.WriteString(w, "short")
				return
			}
			_, _ = io.WriteString(w, "complete")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newBotClient(token)
	client.baseURL = server.URL
	client.http = server.Client()
	client.retryBase = time.Millisecond
	destination := filepath.Join(t.TempDir(), "download.txt")
	if err := client.download(context.Background(), "file-id", destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete" || downloads.Load() != 2 {
		t.Fatalf("download = %q after %d requests", data, downloads.Load())
	}
}

func TestBotSessionReportsPartialFileSend(t *testing.T) {
	const token = "test-token"
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := uploads.Add(1)
		if count == 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "error_code": http.StatusBadRequest, "description": "bad file",
			})
			return
		}
		writeBotResponse(t, w, true, map[string]any{"message_id": count})
	}))
	defer server.Close()

	client := newBotClient(token)
	client.baseURL = server.URL
	client.http = server.Client()
	directory := t.TempDir()
	files := make([]string, 3)
	for index := range files {
		files[index] = filepath.Join(directory, fmt.Sprintf("file-%d.txt", index+1))
		if err := os.WriteFile(files[index], []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	session := &botSession{client: client, chatID: 42, events: make(chan Event)}
	err := session.Send(context.Background(), "", files)
	if err == nil || !strings.Contains(err.Error(), "sent 1 of 3 files; file-2.txt failed; 1 not attempted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeBotResponse(t *testing.T, writer http.ResponseWriter, ok bool, result any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "result": result}); err != nil {
		t.Error(err)
	}
}
