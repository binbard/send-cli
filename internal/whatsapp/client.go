package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultGraphURL = "https://graph.facebook.com"
	DefaultVersion  = "v26.0"
)

type Request struct {
	Token         string
	PhoneNumberID string
	Recipient     string
	Message       string
	Files         []string
	Timeout       time.Duration
}

type Result struct {
	Messages int
	Files    int
}

type Client struct {
	BaseURL    string
	APIVersion string
	HTTP       *http.Client
}

func NewClient(baseURL, apiVersion string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultGraphURL
	}
	if strings.TrimSpace(apiVersion) == "" {
		apiVersion = DefaultVersion
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), APIVersion: strings.Trim(apiVersion, "/"),
		HTTP: &http.Client{},
	}
}

func (c *Client) Send(ctx context.Context, request Request) (Result, error) {
	request.Token = strings.TrimSpace(request.Token)
	request.PhoneNumberID = strings.TrimSpace(request.PhoneNumberID)
	request.Recipient = normalizeRecipient(request.Recipient)
	if request.Token == "" {
		return Result{}, errors.New("WhatsApp access token is required; set SEND_WHATSAPP_TOKEN or use --token")
	}
	if request.PhoneNumberID == "" {
		return Result{}, errors.New("WhatsApp phone number ID is required; set SEND_WHATSAPP_PHONE_NUMBER_ID or use --phone-number-id")
	}
	if !digitsOnly(request.PhoneNumberID) {
		return Result{}, errors.New("WhatsApp phone number ID must contain only digits")
	}
	if request.Recipient == "" {
		return Result{}, errors.New("WhatsApp recipient is required; use --to with an international number")
	}
	if !digitsOnly(request.Recipient) {
		return Result{}, errors.New("WhatsApp recipient must contain only digits after international-number formatting is removed")
	}
	if strings.TrimSpace(request.Message) == "" && len(request.Files) == 0 {
		return Result{}, errors.New("provide a message with -m or at least one file")
	}
	files, err := validateFiles(request.Files)
	if err != nil {
		return Result{}, err
	}
	requestCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	result := Result{}
	messagePending := strings.TrimSpace(request.Message) != ""
	if messagePending && (len(files) == 0 || files[0].mediaType == "audio") {
		if err := c.sendText(requestCtx, request, request.Message); err != nil {
			return result, err
		}
		result.Messages++
		messagePending = false
	}
	for index, file := range files {
		mediaID, err := c.upload(requestCtx, request, file)
		if err != nil {
			return result, partialError(result.Files, len(files), file.path, "upload", err)
		}
		caption := ""
		if index == 0 && messagePending {
			caption = request.Message
			messagePending = false
		}
		if err := c.sendMedia(requestCtx, request, file, mediaID, caption); err != nil {
			return result, partialError(result.Files, len(files), file.path, "send", err)
		}
		result.Files++
		result.Messages++
	}
	return result, nil
}

type localMedia struct {
	path      string
	mediaType string
	mimeType  string
}

func validateFiles(paths []string) ([]localMedia, error) {
	files := make([]localMedia, len(paths))
	for index, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", path, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", path)
		}
		mediaType, mimeType, limit, err := classifyMedia(absolute)
		if err != nil {
			return nil, err
		}
		if info.Size() > limit {
			return nil, fmt.Errorf("%q is %.1f MiB; WhatsApp %s uploads are limited to %.0f MiB", path, float64(info.Size())/(1024*1024), mediaType, float64(limit)/(1024*1024))
		}
		files[index] = localMedia{path: absolute, mediaType: mediaType, mimeType: mimeType}
	}
	return files, nil
}

func classifyMedia(path string) (string, string, int64, error) {
	extension := strings.ToLower(filepath.Ext(path))
	const mib = int64(1024 * 1024)
	switch extension {
	case ".jpg", ".jpeg":
		return "image", "image/jpeg", 5 * mib, nil
	case ".png":
		return "image", "image/png", 5 * mib, nil
	case ".mp4":
		return "video", "video/mp4", 16 * mib, nil
	case ".3gp", ".3gpp":
		return "video", "video/3gpp", 16 * mib, nil
	case ".aac":
		return "audio", "audio/aac", 16 * mib, nil
	case ".amr":
		return "audio", "audio/amr", 16 * mib, nil
	case ".mp3":
		return "audio", "audio/mpeg", 16 * mib, nil
	case ".ogg", ".oga":
		return "audio", "audio/ogg; codecs=opus", 16 * mib, nil
	case ".m4a":
		return "audio", "audio/mp4", 16 * mib, nil
	case ".txt", ".pdf", ".ppt", ".pptx", ".doc", ".docx", ".xls", ".xlsx", ".odt", ".odp", ".ods":
		mimeType := mime.TypeByExtension(extension)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		return "document", mimeType, 100 * mib, nil
	default:
		return "", "", 0, fmt.Errorf("%q is not a supported WhatsApp media type", path)
	}
}

func (c *Client) upload(ctx context.Context, request Request, file localMedia) (string, error) {
	input, err := os.Open(file.path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	reader, pipe := io.Pipe()
	writer := multipart.NewWriter(pipe)
	writeResult := make(chan error, 1)
	go func() {
		writeErr := writer.WriteField("messaging_product", "whatsapp")
		if writeErr == nil {
			writeErr = writer.WriteField("type", file.mimeType)
		}
		if writeErr == nil {
			var part io.Writer
			part, writeErr = writer.CreateFormFile("file", filepath.Base(file.path))
			if writeErr == nil {
				_, writeErr = io.Copy(part, input)
			}
		}
		if closeErr := writer.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = pipe.CloseWithError(writeErr)
		writeResult <- writeErr
	}()

	endpoint := c.endpoint(request.PhoneNumberID, "media")
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return "", err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.Token)
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.httpClient().Do(httpRequest)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return "", err
	}
	defer response.Body.Close()
	if err := <-writeResult; err != nil {
		return "", err
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := decodeResponse(response, &payload); err != nil {
		return "", err
	}
	if payload.ID == "" {
		return "", errors.New("WhatsApp media upload returned no media ID")
	}
	return payload.ID, nil
}

func (c *Client) sendText(ctx context.Context, request Request, message string) error {
	payload := map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": request.Recipient,
		"type": "text", "text": map[string]any{"preview_url": false, "body": message},
	}
	return c.sendJSON(ctx, request, payload)
}

func (c *Client) sendMedia(ctx context.Context, request Request, file localMedia, mediaID, caption string) error {
	media := map[string]any{"id": mediaID}
	if caption != "" && file.mediaType != "audio" {
		media["caption"] = caption
	}
	if file.mediaType == "document" {
		media["filename"] = filepath.Base(file.path)
	}
	payload := map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": request.Recipient,
		"type": file.mediaType, file.mediaType: media,
	}
	return c.sendJSON(ctx, request, payload)
}

func (c *Client) sendJSON(ctx context.Context, request Request, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(request.PhoneNumberID, "messages"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient().Do(httpRequest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponse(response, nil)
}

func (c *Client) endpoint(phoneNumberID, resource string) string {
	return fmt.Sprintf("%s/%s/%s/%s", c.BaseURL, c.APIVersion, phoneNumberID, resource)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func decodeResponse(response *http.Response, output any) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		detail := safeText(failure.Error.Message)
		if detail == "" {
			detail = safeText(strings.TrimSpace(string(data)))
		}
		if detail != "" {
			return fmt.Errorf("WhatsApp API request failed (%s): %s", response.Status, detail)
		}
		return fmt.Errorf("WhatsApp API request failed: %s", response.Status)
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return fmt.Errorf("decode WhatsApp response: %w", err)
		}
	}
	return nil
}

func partialError(sent, total int, path, action string, err error) error {
	return fmt.Errorf("sent %d of %d files; %s failed during %s; %d not attempted: %w", sent, total, filepath.Base(path), action, total-sent-1, err)
}

func normalizeRecipient(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "+")
	return strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(value)
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func safeText(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || char >= ' ' && char != '\x7f' {
			return char
		}
		return -1
	}, value)
}
