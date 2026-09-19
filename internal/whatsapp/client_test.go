package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v26.0/12345/messages" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["to"] != "15551234567" || body["type"] != "text" {
			t.Errorf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, DefaultVersion)
	result, err := client.Send(context.Background(), Request{
		Token: "secret", PhoneNumberID: "12345", Recipient: "+1 (555) 123-4567", Message: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages != 1 || result.Files != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSendUploadsAndSendsDocument(t *testing.T) {
	file := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(file, []byte("pdf-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			if r.URL.Path != "/v26.0/12345/media" {
				t.Errorf("upload path = %q", r.URL.Path)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.FormValue("messaging_product") != "whatsapp" || r.FormValue("type") != "application/pdf" {
				t.Errorf("upload form = %#v", r.Form)
			}
			_, _ = w.Write([]byte(`{"id":"media-1"}`))
		case 2:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			document := body["document"].(map[string]any)
			if document["id"] != "media-1" || document["caption"] != "caption" || document["filename"] != "report.pdf" {
				t.Errorf("document = %#v", document)
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.2"}]}`))
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, DefaultVersion)
	result, err := client.Send(context.Background(), Request{
		Token: "secret", PhoneNumberID: "12345", Recipient: "15551234567", Message: "caption", Files: []string{file},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || result.Messages != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSendReportsMetaErrorWithoutToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"recipient is invalid","type":"OAuthException","code":100}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, DefaultVersion)
	_, err := client.Send(context.Background(), Request{
		Token: "very-secret", PhoneNumberID: "12345", Recipient: "1", Message: "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "recipient is invalid") || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("error = %v", err)
	}
}

func TestClassifyMediaLimits(t *testing.T) {
	tests := []struct {
		path      string
		mediaType string
		limitMiB  int64
	}{
		{"photo.jpg", "image", 5},
		{"clip.mp4", "video", 16},
		{"voice.ogg", "audio", 16},
		{"report.pdf", "document", 100},
	}
	for _, test := range tests {
		mediaType, _, limit, err := classifyMedia(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if mediaType != test.mediaType || limit != test.limitMiB*1024*1024 {
			t.Errorf("classifyMedia(%q) = %q, %d", test.path, mediaType, limit)
		}
	}
	if _, _, _, err := classifyMedia("archive.zip"); err == nil {
		t.Fatal("unsupported archive unexpectedly accepted")
	}
}
