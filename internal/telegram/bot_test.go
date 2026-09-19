package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

func writeBotResponse(t *testing.T, writer http.ResponseWriter, ok bool, result any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "result": result}); err != nil {
		t.Error(err)
	}
}
