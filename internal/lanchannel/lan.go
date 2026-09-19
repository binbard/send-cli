package lanchannel

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"send-cli/internal/httpchannel"
)

const (
	DefaultDiscoveryPort = 45873
	DefaultTransferPort  = 45874
	discoveryRequest     = "SENDCLI_DISCOVER_V1"
	protocol             = "send-cli/1"
	maxRequestSize       = int64(1024 * 1024 * 1024)
)

type Peer struct {
	Name         string `json:"name"`
	URL          string `json:"url,omitempty"`
	Protocol     string `json:"protocol"`
	Port         int    `json:"port"`
	AuthRequired bool   `json:"auth_required"`
}

type Receiver struct {
	Directory string
	Token     string
	OnMessage func(string)
	OnFiles   func([]string)
	mu        sync.Mutex
}

type ServeOptions struct {
	Name          string
	Address       string
	Port          int
	DiscoveryPort int
	Receiver      *Receiver
	Ready         func(address string)
}

func Serve(ctx context.Context, options ServeOptions) error {
	if options.Receiver == nil {
		return errors.New("LAN receiver is required")
	}
	if strings.TrimSpace(options.Receiver.Token) == "" {
		return errors.New("LAN receiver token is required")
	}
	if options.Port == 0 {
		options.Port = DefaultTransferPort
	}
	if options.DiscoveryPort == 0 {
		options.DiscoveryPort = DefaultDiscoveryPort
	}
	if strings.TrimSpace(options.Address) == "" {
		options.Address = "0.0.0.0"
	}
	if strings.TrimSpace(options.Name) == "" {
		options.Name, _ = os.Hostname()
	}
	if err := options.Receiver.prepare(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(options.Address, strconv.Itoa(options.Port)))
	if err != nil {
		return fmt.Errorf("listen for LAN transfers: %w", err)
	}
	defer listener.Close()
	actualPort := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{
		Handler: options.Receiver, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	discoveryErrors := make(chan error, 1)
	go func() {
		discoveryErrors <- advertise(ctx, options.Name, actualPort, options.DiscoveryPort)
	}()
	if options.Ready != nil {
		options.Ready(listener.Addr().String())
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-discoveryErrors:
		if err == nil {
			return nil
		}
		return fmt.Errorf("advertise LAN receiver: %w", err)
	}
}

func (r *Receiver) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/send" {
		http.NotFound(writer, request)
		return
	}
	if !tokenMatches(request.Header.Get("Authorization"), r.Token) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestSize)
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		http.Error(writer, "invalid multipart transfer: "+safeText(err.Error()), http.StatusBadRequest)
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	message := safeText(strings.TrimSpace(request.FormValue("message")))
	var fileHeaders []*multipart.FileHeader
	if request.MultipartForm != nil {
		fileHeaders = request.MultipartForm.File["files"]
	}
	if message == "" && len(fileHeaders) == 0 {
		http.Error(writer, "transfer contains no message or files", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	paths, err := r.saveFiles(fileHeaders)
	r.mu.Unlock()
	if err != nil {
		http.Error(writer, safeText(err.Error()), http.StatusInternalServerError)
		return
	}
	if message != "" && r.OnMessage != nil {
		r.OnMessage(message)
	}
	if len(paths) > 0 && r.OnFiles != nil {
		r.OnFiles(paths)
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "files": len(paths)})
}

func (r *Receiver) prepare() error {
	if strings.TrimSpace(r.Directory) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		r.Directory = cwd
	}
	absolute, err := filepath.Abs(r.Directory)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return fmt.Errorf("create LAN receive directory: %w", err)
	}
	r.Directory = absolute
	return nil
}

func (r *Receiver) saveFiles(headers []*multipart.FileHeader) ([]string, error) {
	paths := make([]string, 0, len(headers))
	for _, header := range headers {
		name := safeName(filepath.Base(strings.TrimSpace(header.Filename)))
		if name == "" || name == "." {
			name = "lan-file"
		}
		input, err := header.Open()
		if err != nil {
			return paths, err
		}
		temporary, err := os.CreateTemp(r.Directory, ".send-part-*")
		if err != nil {
			_ = input.Close()
			return paths, err
		}
		_, copyErr := io.Copy(temporary, input)
		inputErr := input.Close()
		closeErr := temporary.Close()
		if copyErr != nil || inputErr != nil || closeErr != nil {
			_ = os.Remove(temporary.Name())
			if copyErr != nil {
				return paths, copyErr
			}
			if inputErr != nil {
				return paths, inputErr
			}
			return paths, closeErr
		}
		destination, err := availablePath(r.Directory, name)
		if err != nil {
			_ = os.Remove(temporary.Name())
			return paths, err
		}
		if err := os.Rename(temporary.Name(), destination); err != nil {
			_ = os.Remove(temporary.Name())
			return paths, err
		}
		paths = append(paths, destination)
	}
	return paths, nil
}

func availablePath(directory, name string) (string, error) {
	candidate := filepath.Join(directory, name)
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	} else if err != nil {
		return "", err
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for index := 1; ; index++ {
		candidate = filepath.Join(directory, fmt.Sprintf("%s (%d)%s", base, index, extension))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

func advertise(ctx context.Context, name string, transferPort, discoveryPort int) error {
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: discoveryPort})
	if err != nil {
		return err
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()
	payload, err := json.Marshal(Peer{
		Name: name, Protocol: protocol, Port: transferPort, AuthRequired: true,
	})
	if err != nil {
		return err
	}
	buffer := make([]byte, 1024)
	for {
		count, address, err := connection.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if string(buffer[:count]) != discoveryRequest {
			continue
		}
		if _, err := connection.WriteToUDP(payload, address); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

func Discover(ctx context.Context, discoveryPort int, duration time.Duration) ([]Peer, error) {
	if discoveryPort == 0 {
		discoveryPort = DefaultDiscoveryPort
	}
	if duration <= 0 {
		duration = time.Second
	}
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if err := connection.SetWriteDeadline(time.Now().Add(duration)); err != nil {
		return nil, err
	}
	address := &net.UDPAddr{IP: net.IPv4bcast, Port: discoveryPort}
	if _, err := connection.WriteToUDP([]byte(discoveryRequest), address); err != nil {
		return nil, fmt.Errorf("broadcast LAN discovery: %w", err)
	}
	deadline := time.Now().Add(duration)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	buffer := make([]byte, 2048)
	seen := make(map[string]bool)
	var peers []Peer
	for {
		count, source, err := connection.ReadFromUDP(buffer)
		if err != nil {
			if networkErr, ok := err.(net.Error); ok && networkErr.Timeout() {
				return peers, nil
			}
			if ctx.Err() != nil {
				return peers, ctx.Err()
			}
			return nil, err
		}
		var peer Peer
		if err := json.Unmarshal(buffer[:count], &peer); err != nil || peer.Protocol != protocol || peer.Port < 1 || peer.Port > 65535 {
			continue
		}
		peer.Name = safeName(peer.Name)
		peer.URL = "http://" + net.JoinHostPort(source.IP.String(), strconv.Itoa(peer.Port))
		key := strings.ToLower(peer.Name) + "|" + peer.URL
		if !seen[key] {
			seen[key] = true
			peers = append(peers, peer)
		}
	}
}

func ResolveTarget(ctx context.Context, target string, discoveryPort int, duration time.Duration) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("LAN target is required; use --to or --list")
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		target = strings.TrimRight(target, "/")
		if strings.HasSuffix(target, "/send") {
			return target, nil
		}
		return target + "/send", nil
	}
	if strings.Contains(target, ".") || strings.Contains(target, ":") || net.ParseIP(target) != nil {
		host := target
		if _, _, err := net.SplitHostPort(target); err != nil {
			host = net.JoinHostPort(target, strconv.Itoa(DefaultTransferPort))
		}
		return "http://" + host + "/send", nil
	}
	peers, discoveryErr := Discover(ctx, discoveryPort, duration)
	for _, peer := range peers {
		if strings.EqualFold(peer.Name, target) {
			return peer.URL + "/send", nil
		}
	}
	if discoveryErr != nil {
		return "", discoveryErr
	}
	return "", fmt.Errorf("LAN peer %q was not discovered", target)
}

func Send(ctx context.Context, target, token, message string, files []string, timeout time.Duration) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("LAN receiver token is required; use --token or SEND_LAN_TOKEN")
	}
	_, err := httpchannel.Send(ctx, httpchannel.Request{
		URL: target, Method: http.MethodPost, Message: message, Files: files,
		Headers: []string{"Authorization: Bearer " + token}, Timeout: timeout, ForceMultipart: true,
	})
	return err
}

func tokenMatches(header, token string) bool {
	provided := strings.TrimPrefix(header, "Bearer ")
	return len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func safeText(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || char >= ' ' && char != '\x7f' {
			return char
		}
		return -1
	}, value)
}

func safeName(value string) string {
	return strings.Map(func(char rune) rune {
		if char >= ' ' && char != '\x7f' {
			return char
		}
		return -1
	}, value)
}
