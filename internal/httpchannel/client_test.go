package httpchannel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSendMultipartFiles(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.txt")
	second := filepath.Join(directory, "second.txt")
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("method=%q authorization=%q", r.Method, r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("message") != "hello" {
			t.Errorf("message = %q", r.FormValue("message"))
		}
		parts := r.MultipartForm.File["files"]
		if len(parts) != 2 {
			t.Fatalf("files = %d, want 2", len(parts))
		}
		for index, want := range []string{"one", "two"} {
			file, err := parts[index].Open()
			if err != nil {
				t.Error(err)
				continue
			}
			data, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil || string(data) != want {
				t.Errorf("file %d = %q, err=%v", index, data, err)
			}
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	result, err := Send(context.Background(), Request{
		URL: server.URL, Method: http.MethodPatch, Message: "hello",
		Files: []string{first, second}, Headers: []string{"Authorization: Bearer test"},
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("status = %#v", result)
	}
}

func TestSendTextBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(data) != "hello" || r.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("body=%q content-type=%q", data, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if _, err := Send(context.Background(), Request{URL: server.URL, Message: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestSendReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "try later", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := Send(context.Background(), Request{URL: server.URL, Message: "hello"})
	if err == nil || !strings.Contains(err.Error(), "503 Service Unavailable: try later") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendValidatesInputBeforeRequest(t *testing.T) {
	tests := []Request{
		{URL: "ftp://example.com", Message: "hello"},
		{URL: "https://example.com"},
		{URL: "https://example.com", Message: "hello", Headers: []string{"broken"}},
		{URL: "https://example.com", Files: []string{"missing.txt"}},
	}
	for _, request := range tests {
		if _, err := Send(context.Background(), request); err == nil {
			t.Errorf("Send(%#v) unexpectedly succeeded", request)
		}
	}
}
