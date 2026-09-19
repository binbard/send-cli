package httpchannel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgservice "send-cli/internal/telegram"
)

func TestHTTPShareServesInitialFileAndReceivesMessage(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.txt")
	if err := os.WriteFile(path, []byte("shared report"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Serve(ctx, ShareOptions{Address: "127.0.0.1", Files: []string{path}}, func(_ context.Context, session tgservice.Session, info ShareInfo) error {
		parsedURL, err := url.Parse(info.URL)
		if err != nil || parsedURL.Path != "" {
			return fmt.Errorf("share URL must use the server root: %q", info.URL)
		}
		response, err := http.Get(info.URL)
		if err != nil {
			return err
		}
		page, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("No messages yet")) {
			return fmt.Errorf("status=%s page=%q", response.Status, page)
		}
		if bytes.Contains(page, []byte(`id="sender"`)) || bytes.Contains(page, []byte("Your name")) {
			return errors.New("web interface must not ask for a sender name")
		}
		if !bytes.Contains(page, []byte(`id="show-qr"`)) {
			return errors.New("web interface is missing the QR button")
		}

		base := strings.TrimSuffix(info.URL, "/")
		response, err = http.Get(base + "/api/qr")
		if err != nil {
			return err
		}
		qrImage, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.Header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(qrImage, []byte("\x89PNG\r\n\x1a\n")) {
			return fmt.Errorf("invalid QR response: content-type=%q bytes=%d err=%v", response.Header.Get("Content-Type"), len(qrImage), readErr)
		}

		sseCtx, stopSSE := context.WithTimeout(context.Background(), time.Second)
		defer stopSSE()
		sseRequest, err := http.NewRequestWithContext(sseCtx, http.MethodGet, base+"/api/events", nil)
		if err != nil {
			return err
		}
		sseResponse, err := http.DefaultClient.Do(sseRequest)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(sseResponse.Body)
		foundFileEvent := false
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				foundFileEvent = strings.Contains(scanner.Text(), `"kind":"file"`)
				break
			}
		}
		_ = sseResponse.Body.Close()
		if !foundFileEvent {
			return errors.New("initial file was not replayed through the live event stream")
		}

		response, err = http.Get(base + "/files/1/report.txt")
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || string(data) != "shared report" {
			return fmt.Errorf("download=%q err=%v", data, readErr)
		}

		response, err = http.PostForm(base+"/api/message", url.Values{"message": {"hello"}})
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		select {
		case event := <-session.Events():
			if event.Sender != "Browser" || event.Text != "hello" {
				return fmt.Errorf("event = %#v", event)
			}
		case <-time.After(time.Second):
			return errors.New("timed out waiting for browser message")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPShareReceivesFileForTerminalDownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Serve(ctx, ShareOptions{Address: "127.0.0.1"}, func(_ context.Context, session tgservice.Session, info ShareInfo) error {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("files", "notes.txt")
		if err != nil {
			return err
		}
		if _, err := io.WriteString(part, "browser upload"); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		request, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(info.URL, "/")+"/api/upload", &body)
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("upload status = %s", response.Status)
		}
		select {
		case event := <-session.Events():
			if event.Sender != "Browser" || event.File == nil || event.File.Name != "notes.txt" {
				return fmt.Errorf("event = %#v", event)
			}
			destination := filepath.Join(t.TempDir(), "download.txt")
			if err := event.File.Download(context.Background(), destination); err != nil {
				return err
			}
			data, err := os.ReadFile(destination)
			if err != nil || string(data) != "browser upload" {
				return fmt.Errorf("download=%q err=%v", data, err)
			}
		case <-time.After(time.Second):
			return errors.New("timed out waiting for browser file")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPShareRejectsUnknownRootRoute(t *testing.T) {
	handler := &shareHandler{hub: newShareHub("")}
	request, _ := http.NewRequest(http.MethodGet, "http://example.test/unknown", nil)
	recorder := &responseRecorder{header: make(http.Header)}
	handler.ServeHTTP(recorder, request)
	if recorder.status != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.status)
	}
}

func TestListenForShareIncrementsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	if port == 65535 {
		t.Skip("allocated port has no successor")
	}
	probe, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port+1)))
	if err != nil {
		t.Skip("next port is already occupied")
	}
	_ = probe.Close()

	listener, got, err := listenForShare("127.0.0.1", port, true)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if got != port+1 {
		t.Fatalf("selected port = %d, want %d", got, port+1)
	}
}

func TestHTTPShareStopsQuicklyWithConnectedBrowser(t *testing.T) {
	var body io.ReadCloser
	started := time.Now()
	err := Serve(context.Background(), ShareOptions{Address: "127.0.0.1"}, func(_ context.Context, _ tgservice.Session, info ShareInfo) error {
		response, err := http.Get(info.URL + "/api/events")
		if err != nil {
			return err
		}
		body = response.Body
		return nil
	})
	if body != nil {
		_ = body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("HTTP share took %s to stop with a connected browser", elapsed)
	}
}

type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header { return r.header }
func (r *responseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}
func (r *responseRecorder) WriteHeader(status int) { r.status = status }
