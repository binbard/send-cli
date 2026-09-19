package httpchannel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"rsc.io/qr"

	tgservice "send-cli/internal/telegram"
)

const (
	DefaultPort         = 8080
	maxShareRequestSize = int64(1 << 30)
)

type ShareOptions struct {
	Address        string
	Port           int
	IncrementPort  bool
	InitialMessage string
	Files          []string
	Ready          func(ShareInfo)
}

type ShareInfo struct {
	URL     string
	Address string
	Port    int
}

// Serve starts a session-scoped HTTP share and runs the terminal client until
// the context is cancelled or the client exits.
func Serve(ctx context.Context, options ShareOptions, client func(context.Context, tgservice.Session, ShareInfo) error) error {
	if client == nil {
		return errors.New("HTTP share client is required")
	}
	files, err := validateFiles(options.Files)
	if err != nil {
		return err
	}
	if options.Port < 0 || options.Port > 65535 {
		return errors.New("HTTP share port must be between 0 and 65535")
	}
	address := strings.TrimSpace(options.Address)
	if address == "" {
		address = "0.0.0.0"
	}
	uploadDirectory, err := os.MkdirTemp("", "send-cli-http-")
	if err != nil {
		return fmt.Errorf("create HTTP share temporary directory: %w", err)
	}
	defer os.RemoveAll(uploadDirectory)

	listener, port, err := listenForShare(address, options.Port, options.IncrementPort)
	if err != nil {
		return fmt.Errorf("start HTTP share: %w", err)
	}
	defer listener.Close()
	basePath := ""
	info := ShareInfo{
		URL:     "http://" + net.JoinHostPort(shareHost(address), strconv.Itoa(port)),
		Address: listener.Addr().String(),
		Port:    port,
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	hub := newShareHub(basePath)
	session := &shareSession{ctx: runCtx, hub: hub, events: make(chan tgservice.Event, 64)}
	for _, path := range files {
		if _, err := hub.addFile("Host", path, filepath.Base(path)); err != nil {
			return err
		}
	}
	if message := strings.TrimSpace(options.InitialMessage); message != "" {
		hub.addMessage("Host", message)
	}

	handler := &shareHandler{
		basePath: basePath, publicURL: info.URL, uploadDirectory: uploadDirectory, hub: hub, session: session,
	}
	server := &http.Server{
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return runCtx },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	if options.Ready != nil {
		options.Ready(info)
	}
	clientErrors := make(chan error, 1)
	go func() { clientErrors <- client(runCtx, session, info) }()

	var (
		result     error
		clientDone bool
	)
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve HTTP share: %w", err)
		}
	case err := <-clientErrors:
		result = err
		clientDone = true
	}
	cancel()
	if err := server.Close(); result == nil && err != nil && !errors.Is(err, net.ErrClosed) {
		result = fmt.Errorf("stop HTTP share: %w", err)
	}
	if !clientDone {
		select {
		case err := <-clientErrors:
			if result == nil {
				result = err
			}
		case <-time.After(2 * time.Second):
		}
	}
	return result
}

func listenForShare(address string, firstPort int, increment bool) (net.Listener, int, error) {
	network := "tcp"
	if ip := net.ParseIP(strings.Trim(address, "[]")); ip != nil {
		if ip.To4() != nil {
			network = "tcp4"
		} else {
			network = "tcp6"
		}
	}
	portFallbackValidated := false
	for port := firstPort; ; port++ {
		listener, err := net.Listen(network, net.JoinHostPort(address, strconv.Itoa(port)))
		if err == nil {
			return listener, listener.Addr().(*net.TCPAddr).Port, nil
		}
		if !increment || port == 0 || port == 65535 {
			return nil, 0, err
		}
		if !portFallbackValidated {
			probe, probeErr := net.Listen(network, net.JoinHostPort(address, "0"))
			if probeErr != nil {
				return nil, 0, err
			}
			_ = probe.Close()
			portFallbackValidated = true
		}
	}
}

type sharedFile struct {
	ID   int64
	Name string
	Size int64
	Path string
	URL  string
}

type shareEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Sender    string          `json:"sender,omitempty"`
	Text      string          `json:"text,omitempty"`
	File      *sharedFileView `json:"file,omitempty"`
	Timestamp string          `json:"timestamp"`
}

type sharedFileView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type shareHub struct {
	mu          sync.Mutex
	basePath    string
	nextID      int64
	history     []shareEvent
	files       map[int64]sharedFile
	subscribers map[chan shareEvent]struct{}
}

func newShareHub(basePath string) *shareHub {
	return &shareHub{
		basePath: basePath, files: make(map[int64]sharedFile), subscribers: make(map[chan shareEvent]struct{}),
	}
}

func (h *shareHub) addMessage(sender, text string) {
	text = cleanShareText(text, 64<<10)
	if text == "" {
		return
	}
	h.publish(shareEvent{Kind: "message", Sender: cleanShareText(sender, 80), Text: text})
}

func (h *shareHub) addFile(sender, path, name string) (sharedFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sharedFile{}, err
	}
	if !info.Mode().IsRegular() {
		return sharedFile{}, fmt.Errorf("%q is not a regular file", path)
	}
	name = safeShareName(name)
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	file := sharedFile{
		ID: id, Name: name, Size: info.Size(), Path: path,
		URL: fmt.Sprintf("%s/files/%d/%s", h.basePath, id, url.PathEscape(name)),
	}
	h.files[id] = file
	event := shareEvent{
		ID: id, Kind: "file", Sender: cleanShareText(sender, 80), Timestamp: time.Now().Format(time.RFC3339),
		File: &sharedFileView{ID: id, Name: file.Name, Size: file.Size, URL: file.URL},
	}
	h.history = append(h.history, event)
	h.broadcastLocked(event)
	h.mu.Unlock()
	return file, nil
}

func (h *shareHub) publish(event shareEvent) {
	h.mu.Lock()
	h.nextID++
	event.ID = h.nextID
	event.Timestamp = time.Now().Format(time.RFC3339)
	h.history = append(h.history, event)
	h.broadcastLocked(event)
	h.mu.Unlock()
}

func (h *shareHub) broadcastLocked(event shareEvent) {
	for subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (h *shareHub) subscribe(after int64) (<-chan shareEvent, func()) {
	h.mu.Lock()
	channel := make(chan shareEvent, len(h.history)+64)
	for _, event := range h.history {
		if event.ID > after {
			channel <- event
		}
	}
	h.subscribers[channel] = struct{}{}
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		delete(h.subscribers, channel)
		h.mu.Unlock()
	}
}

func (h *shareHub) file(id int64) (sharedFile, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	file, ok := h.files[id]
	return file, ok
}

type shareSession struct {
	ctx    context.Context
	hub    *shareHub
	events chan tgservice.Event
}

func (s *shareSession) Events() <-chan tgservice.Event {
	return s.events
}

func (s *shareSession) Send(_ context.Context, text string, paths []string) error {
	files, err := validateFiles(paths)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return errors.New("message is empty")
	}
	if strings.TrimSpace(text) != "" {
		s.hub.addMessage("Host", text)
	}
	for _, path := range files {
		if _, err := s.hub.addFile("Host", path, filepath.Base(path)); err != nil {
			return err
		}
	}
	return nil
}

func (s *shareSession) incomingMessage(sender, text string) {
	s.hub.addMessage(sender, text)
	s.deliver(tgservice.Event{Sender: sender, Text: text})
}

func (s *shareSession) incomingFile(sender string, file sharedFile) {
	remote := &tgservice.RemoteFile{
		Name: file.Name,
		Size: file.Size,
		Download: func(ctx context.Context, destination string) error {
			input, err := os.Open(file.Path)
			if err != nil {
				return err
			}
			defer input.Close()
			output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := copyWithContext(ctx, output, input)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		},
	}
	s.deliver(tgservice.Event{Sender: sender, File: remote})
}

func (s *shareSession) deliver(event tgservice.Event) {
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}

type shareHandler struct {
	basePath        string
	publicURL       string
	uploadDirectory string
	hub             *shareHub
	session         *shareSession
}

func (h *shareHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	if request.URL.Path == h.basePath {
		http.Redirect(writer, request, h.basePath+"/", http.StatusTemporaryRedirect)
		return
	}
	if request.URL.Path == h.basePath+"/" {
		h.serveApp(writer, request)
		return
	}
	suffix := strings.TrimPrefix(request.URL.Path, h.basePath)
	if h.basePath != "" && suffix == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	switch {
	case suffix == "/api/events":
		h.serveEvents(writer, request)
	case suffix == "/api/qr":
		h.serveQR(writer, request)
	case suffix == "/api/message":
		h.receiveMessage(writer, request)
	case suffix == "/api/upload":
		h.receiveUpload(writer, request)
	case strings.HasPrefix(suffix, "/files/"):
		h.serveFile(writer, request, strings.TrimPrefix(suffix, "/files/"))
	default:
		http.NotFound(writer, request)
	}
}

func (h *shareHandler) serveQR(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	code, err := qr.Encode(h.publicURL, qr.M)
	if err != nil {
		http.Error(writer, "could not create QR code", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "image/png")
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		_, _ = writer.Write(code.PNG())
	}
}

func (h *shareHandler) serveApp(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
	_, _ = io.WriteString(writer, shareWebApp)
}

func (h *shareHandler) serveEvents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	after, _ := strconv.ParseInt(request.Header.Get("Last-Event-ID"), 10, 64)
	events, unsubscribe := h.hub.subscribe(after)
	defer unsubscribe()
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.WriteString(writer, ": connected\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-events:
			data, _ := json.Marshal(event)
			fmt.Fprintf(writer, "id: %d\ndata: %s\n\n", event.ID, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(writer, ": keepalive\n\n")
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (h *shareHandler) receiveMessage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 128<<10)
	if err := request.ParseForm(); err != nil {
		http.Error(writer, "invalid message", http.StatusBadRequest)
		return
	}
	sender := browserSender(request.FormValue("sender"))
	message := cleanShareText(request.FormValue("message"), 64<<10)
	if message == "" {
		http.Error(writer, "message is empty", http.StatusBadRequest)
		return
	}
	h.session.incomingMessage(sender, message)
	writeJSON(writer, map[string]any{"ok": true})
}

func (h *shareHandler) receiveUpload(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxShareRequestSize)
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		http.Error(writer, "invalid upload: "+safeText(err.Error()), http.StatusBadRequest)
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	sender := browserSender(request.FormValue("sender"))
	message := cleanShareText(request.FormValue("message"), 64<<10)
	var headers []*multipart.FileHeader
	if request.MultipartForm != nil {
		headers = request.MultipartForm.File["files"]
	}
	if message == "" && len(headers) == 0 {
		http.Error(writer, "upload contains no message or files", http.StatusBadRequest)
		return
	}
	if message != "" {
		h.session.incomingMessage(sender, message)
	}
	count := 0
	for _, header := range headers {
		path, name, err := h.saveUpload(request.Context(), header)
		if err != nil {
			http.Error(writer, safeText(err.Error()), http.StatusInternalServerError)
			return
		}
		file, err := h.hub.addFile(sender, path, name)
		if err != nil {
			http.Error(writer, safeText(err.Error()), http.StatusInternalServerError)
			return
		}
		h.session.incomingFile(sender, file)
		count++
	}
	writeJSON(writer, map[string]any{"ok": true, "files": count})
}

func (h *shareHandler) saveUpload(ctx context.Context, header *multipart.FileHeader) (string, string, error) {
	name := safeShareName(header.Filename)
	input, err := header.Open()
	if err != nil {
		return "", "", err
	}
	defer input.Close()
	output, err := os.CreateTemp(h.uploadDirectory, ".upload-*")
	if err != nil {
		return "", "", err
	}
	path := output.Name()
	_, copyErr := copyWithContext(ctx, output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if copyErr != nil {
			return "", "", copyErr
		}
		return "", "", closeErr
	}
	return path, name, nil
}

func (h *shareHandler) serveFile(writer http.ResponseWriter, request *http.Request, suffix string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idText, _, _ := strings.Cut(suffix, "/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	file, ok := h.hub.file(id)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	http.ServeFile(writer, request, file.Path)
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func shareHost(bound string) string {
	if ip := net.ParseIP(bound); ip != nil {
		if !ip.IsUnspecified() {
			return bound
		}
	} else if bound != "" {
		return strings.Trim(bound, "[]")
	}
	var fallback string
	interfaces, _ := net.Interfaces()
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := networkInterface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || ip.IsLoopback() {
				continue
			}
			if ip.IsPrivate() {
				return ip.String()
			}
			if fallback == "" {
				fallback = ip.String()
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}

func browserSender(value string) string {
	value = cleanShareText(value, 80)
	if value == "" {
		return "Browser"
	}
	return value
}

func cleanShareText(value string, maxBytes int) string {
	value = strings.TrimSpace(safeText(value))
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func safeShareName(value string) string {
	name := filepath.Base(strings.TrimSpace(value))
	name = strings.Map(func(char rune) rune {
		if char < ' ' || char == '\x7f' || strings.ContainsRune(`<>:"/\\|?*`, char) {
			return '_'
		}
		return char
	}, name)
	if name == "" || name == "." {
		return "shared-file"
	}
	return name
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

var _ tgservice.Session = (*shareSession)(nil)
