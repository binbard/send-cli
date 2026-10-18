package provision

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"send-cli/internal/config"
	"send-cli/internal/whatsapp"
)

const (
	path          = "/v1/provision"
	codeBytes     = 16
	requestBytes  = 32
	maxBundleSize = whatsapp.MaxTransferSize*4/3 + (1 << 20)
)

type Bundle struct {
	Telegram        config.TelegramConfig `json:"telegram"`
	WhatsApp        config.WhatsAppConfig `json:"whatsapp"`
	WhatsAppSession []byte                `json:"whatsapp_session,omitempty"`
}

type OfferOptions struct {
	Bind  string
	Port  int
	Ready func(address, code string)
}

type request struct {
	Nonce string `json:"nonce"`
	Proof string `json:"proof"`
}

type response struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// Offer makes one encrypted bundle available until it is claimed or ctx ends.
// The pairing code is never sent over the network.
func Offer(ctx context.Context, bundle Bundle, options OfferOptions) error {
	if err := validateBundle(bundle); err != nil {
		return err
	}
	if options.Port < 0 || options.Port > 65535 {
		return errors.New("provisioning port must be between 0 and 65535")
	}
	bind := strings.TrimSpace(options.Bind)
	if bind == "" {
		bind = "0.0.0.0"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(options.Port)))
	if err != nil {
		return fmt.Errorf("listen for provisioning: %w", err)
	}
	defer listener.Close()
	secret := make([]byte, codeBytes)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	defer clear(secret)
	code := base64.RawURLEncoding.EncodeToString(secret)
	address := net.JoinHostPort(advertisedHost(bind), strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	payload, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if len(payload) > maxBundleSize {
		return errors.New("provisioning bundle is too large")
	}
	completed := make(chan error, 1)
	var claimed atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		clientNonce, err := base64.RawURLEncoding.DecodeString(req.Nonce)
		if err != nil || len(clientNonce) != requestBytes {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		proof, err := base64.RawURLEncoding.DecodeString(req.Proof)
		if err != nil || !hmac.Equal(proof, requestProof(secret, clientNonce)) {
			http.Error(w, "pairing code rejected", http.StatusForbidden)
			return
		}
		if !claimed.CompareAndSwap(false, true) {
			http.Error(w, "pairing code already used", http.StatusGone)
			return
		}
		sealed, err := encrypt(secret, clientNonce, payload)
		if err != nil {
			http.Error(w, "cannot encrypt provisioning data", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		completed <- json.NewEncoder(w).Encode(sealed)
	})
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	if options.Ready != nil {
		options.Ready(address, code)
	}
	var result error
	select {
	case err := <-completed:
		result = err
	case <-ctx.Done():
		result = ctx.Err()
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	return result
}

// Join retrieves and authenticates a one-time bundle from a configured device.
func Join(ctx context.Context, address, code string) (Bundle, error) {
	if _, _, err := net.SplitHostPort(address); err != nil || strings.ContainsAny(address, "/@?#") {
		return Bundle{}, errors.New("use the host:port shown by the configured device")
	}
	secret, err := decodeCode(strings.TrimSpace(code))
	if err != nil {
		return Bundle{}, errors.New("invalid pairing code")
	}
	defer clear(secret)
	clientNonce := make([]byte, requestBytes)
	if _, err := rand.Read(clientNonce); err != nil {
		return Bundle{}, err
	}
	reqBody, _ := json.Marshal(request{
		Nonce: base64.RawURLEncoding.EncodeToString(clientNonce),
		Proof: base64.RawURLEncoding.EncodeToString(requestProof(secret, clientNonce)),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+path, bytes.NewReader(reqBody))
	if err != nil {
		return Bundle{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return Bundle{}, fmt.Errorf("contact configured device: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Bundle{}, fmt.Errorf("provisioning request failed: %s", resp.Status)
	}
	var sealed response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*maxBundleSize)).Decode(&sealed); err != nil {
		return Bundle{}, fmt.Errorf("read encrypted provisioning data: %w", err)
	}
	payload, err := decrypt(secret, clientNonce, sealed)
	if err != nil {
		return Bundle{}, fmt.Errorf("authenticate provisioning data: %w", err)
	}
	if len(payload) > maxBundleSize {
		return Bundle{}, errors.New("provisioning bundle is too large")
	}
	var bundle Bundle
	if err := json.Unmarshal(payload, &bundle); err != nil {
		return Bundle{}, err
	}
	if err := validateBundle(bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func decodeCode(code string) ([]byte, error) {
	// Accept codes printed by earlier releases while newly offered codes stay short.
	if len(code) == 48 {
		legacy, err := hex.DecodeString(code)
		if err == nil && len(legacy) == 24 {
			return legacy, nil
		}
	}
	secret, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(secret) != codeBytes {
		return nil, errors.New("invalid pairing code")
	}
	return secret, nil
}

func validateBundle(bundle Bundle) error {
	if !bundle.Telegram.Ready() && len(bundle.WhatsAppSession) == 0 {
		return errors.New("configured device has no Telegram or WhatsApp credentials to share")
	}
	if len(bundle.WhatsAppSession) > whatsapp.MaxTransferSize {
		return errors.New("WhatsApp session exceeds the provisioning limit")
	}
	return nil
}

func requestProof(secret, nonce []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("send-cli-provision-request-v1"))
	_, _ = mac.Write(nonce)
	return mac.Sum(nil)
}

func key(secret []byte) [32]byte {
	return sha256.Sum256(append([]byte("send-cli-provision-encryption-v1"), secret...))
}

func encrypt(secret, clientNonce, payload []byte) (response, error) {
	k := key(secret)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return response{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return response{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return response{}, err
	}
	sealed := aead.Seal(nil, nonce, payload, clientNonce)
	return response{base64.RawURLEncoding.EncodeToString(nonce), base64.RawURLEncoding.EncodeToString(sealed)}, nil
}

func decrypt(secret, clientNonce []byte, sealed response) ([]byte, error) {
	nonce, err := base64.RawURLEncoding.DecodeString(sealed.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(sealed.Ciphertext)
	if err != nil {
		return nil, err
	}
	k := key(secret)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() || len(ciphertext) > maxBundleSize+256 {
		return nil, errors.New("invalid encrypted provisioning data")
	}
	return aead.Open(nil, nonce, ciphertext, clientNonce)
}

func advertisedHost(bind string) string {
	if bind != "0.0.0.0" && bind != "::" {
		return bind
	}
	interfaces, _ := net.Interfaces()
	var fallback string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip = ip.To4(); ip != nil && !ip.IsLoopback() {
				if ip.IsPrivate() {
					return ip.String()
				}
				if fallback == "" {
					fallback = ip.String()
				}
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}
