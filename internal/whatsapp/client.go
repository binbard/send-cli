package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waEvents "go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const sessionFileName = "whatsapp.db"

type Request struct {
	Recipient string
	Message   string
	Files     []string
	Timeout   time.Duration
}

type Result struct {
	Messages int
	Files    int
}

type Client struct {
	wa        *whatsmeow.Client
	container *sqlstore.Container
	path      string
	output    io.Writer
	closed    bool
}

func SessionPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(root, "send-cli", sessionFileName), nil
}

func Open(ctx context.Context, path string, output io.Writer) (*Client, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("WhatsApp session path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve WhatsApp session path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("create WhatsApp session directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(absolute) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	container, err := sqlstore.New(ctx, "sqlite", dsn, nil)
	if err != nil {
		return nil, fmt.Errorf("open WhatsApp session: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("load WhatsApp device: %w", err)
	}
	if err := os.Chmod(absolute, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = container.Close()
		return nil, fmt.Errorf("protect WhatsApp session: %w", err)
	}
	if output == nil {
		output = io.Discard
	}
	waStore.SetOSInfo("send-cli", [3]uint32{0, 1, 0})
	return &Client{
		wa:        whatsmeow.NewClient(device, nil),
		container: container,
		path:      absolute,
		output:    output,
	}, nil
}

func IsLinked(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	client, err := Open(ctx, path, io.Discard)
	if err != nil {
		return false, err
	}
	defer client.Close()
	return client.Linked(), nil
}

func (c *Client) Linked() bool {
	return c != nil && c.wa != nil && c.wa.Store.ID != nil && !c.wa.Store.Deleted
}

func (c *Client) Connect(ctx context.Context) error {
	if c == nil || c.wa == nil {
		return errors.New("WhatsApp client is not open")
	}
	if c.wa.IsLoggedIn() {
		return nil
	}
	if c.Linked() {
		return c.connectLinked(ctx)
	}

	qrCtx, cancelQR := context.WithCancel(ctx)
	defer cancelQR()
	loginEvents, stopLoginWatch := c.watchLogin()
	defer stopLoginWatch()
	qrChannel, err := c.wa.GetQRChannel(qrCtx)
	if err != nil {
		return fmt.Errorf("start WhatsApp QR login: %w", err)
	}
	if err := c.wa.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect to WhatsApp for pairing: %w", err)
	}
	fmt.Fprintln(c.output, "Link WhatsApp: open WhatsApp on your phone, then use Settings > Linked devices > Link a device.")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-qrChannel:
			if !ok {
				return errors.New("WhatsApp QR login ended before pairing completed")
			}
			switch event.Event {
			case whatsmeow.QRChannelEventCode:
				qrterminal.GenerateHalfBlock(event.Code, qrterminal.L, c.output)
				fmt.Fprintln(c.output, "Scan this QR code before it refreshes.")
			case whatsmeow.QRChannelSuccess.Event:
				// WhatsApp normally reconnects the socket after pairing. Wait for
				// the authenticated event instead of treating that handoff as a
				// failed connection or starting a competing reconnect ourselves.
				if err := c.waitForLogin(ctx, loginEvents); err != nil {
					return fmt.Errorf("WhatsApp paired, but linked-device login failed: %w", err)
				}
				fmt.Fprintln(c.output, "WhatsApp linked successfully.")
				return nil
			case whatsmeow.QRChannelTimeout.Event:
				return errors.New("WhatsApp QR login timed out; run the command again for a new code")
			case whatsmeow.QRChannelEventError:
				if event.Error != nil {
					return fmt.Errorf("WhatsApp QR login failed: %w", event.Error)
				}
				return errors.New("WhatsApp QR login failed")
			case whatsmeow.QRChannelEventPasskeyResponse:
				if err := c.wa.SendPasskeyConfirmation(ctx); err != nil {
					return fmt.Errorf("confirm WhatsApp pairing: %w", err)
				}
			case whatsmeow.QRChannelEventPasskeyRequest:
				return errors.New("WhatsApp requested passkey pairing; cancel and scan the QR code from Linked devices instead")
			default:
				return fmt.Errorf("WhatsApp QR login failed: %s", event.Event)
			}
		}
	}
}

func (c *Client) connectLinked(ctx context.Context) error {
	loginEvents, stopLoginWatch := c.watchLogin()
	defer stopLoginWatch()
	if err := c.wa.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}
	return c.waitForLogin(ctx, loginEvents)
}

func (c *Client) watchLogin() (<-chan error, func()) {
	result := make(chan error, 1)
	handlerID := c.wa.AddEventHandler(func(rawEvent any) {
		report := func(err error) {
			select {
			case result <- err:
			default:
			}
		}
		var loginErr error
		switch event := rawEvent.(type) {
		case *waEvents.Connected:
		case *waEvents.LoggedOut:
			loginErr = errors.New(event.PermanentDisconnectDescription())
			// WhatsMeow dispatches LoggedOut immediately before deleting the
			// rejected local device. Give that database cleanup time to finish
			// before the caller closes the session store.
			time.AfterFunc(250*time.Millisecond, func() { report(loginErr) })
			return
		case waEvents.PermanentDisconnect:
			loginErr = errors.New(event.PermanentDisconnectDescription())
		default:
			return
		}
		report(loginErr)
	})
	return result, func() { c.wa.RemoveEventHandler(handlerID) }
}

func (c *Client) waitForLogin(ctx context.Context, loginEvents <-chan error) error {
	if c.wa.IsLoggedIn() {
		return nil
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case err := <-loginEvents:
		if err != nil {
			return fmt.Errorf("WhatsApp login failed: %w", err)
		}
		return nil
	case <-timer.C:
		if c.wa.IsLoggedIn() {
			return nil
		}
		return errors.New("WhatsApp linked-device login timed out; check your connection or relink if this device was removed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Send(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.Recipient) == "" {
		return Result{}, errors.New("WhatsApp recipient is required; use --to with an international phone number")
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
	if err := c.Connect(requestCtx); err != nil {
		return Result{}, err
	}
	recipient, err := c.resolveRecipient(requestCtx, request.Recipient)
	if err != nil {
		return Result{}, err
	}

	result := Result{}
	messagePending := strings.TrimSpace(request.Message) != ""
	if messagePending && (len(files) == 0 || files[0].mediaType == whatsmeow.MediaAudio) {
		if _, err := c.wa.SendMessage(requestCtx, recipient, &waE2E.Message{Conversation: proto.String(request.Message)}); err != nil {
			return result, fmt.Errorf("send WhatsApp message: %w", err)
		}
		result.Messages++
		messagePending = false
	}
	for index, file := range files {
		caption := ""
		if index == 0 && messagePending {
			caption = request.Message
			messagePending = false
		}
		if err := c.sendFile(requestCtx, recipient, file, caption); err != nil {
			return result, partialError(result.Files, len(files), file.path, err)
		}
		result.Files++
		result.Messages++
	}
	return result, nil
}

func (c *Client) Logout(ctx context.Context) error {
	if !c.Linked() {
		return errors.New("WhatsApp is not linked on this computer")
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	if err := c.wa.Logout(ctx); err != nil {
		return fmt.Errorf("unlink WhatsApp device: %w", err)
	}
	if err := c.closeStore(); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-shm", "-wal"} {
		if err := os.Remove(c.path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove WhatsApp session: %w", err)
		}
	}
	return nil
}

func (c *Client) Close() error {
	if c == nil || c.closed {
		return nil
	}
	if c.wa != nil {
		c.wa.Disconnect()
	}
	return c.closeStore()
}

func (c *Client) closeStore() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.container != nil {
		if err := c.container.Close(); err != nil {
			return fmt.Errorf("close WhatsApp session: %w", err)
		}
	}
	return nil
}

func (c *Client) resolveRecipient(ctx context.Context, value string) (types.JID, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "@") {
		jid, err := types.ParseJID(value)
		if err != nil || jid.User == "" || jid.Server == "" {
			return types.EmptyJID, fmt.Errorf("invalid WhatsApp JID %q", value)
		}
		return jid.ToNonAD(), nil
	}
	phone := normalizeRecipient(value)
	if !digitsOnly(phone) {
		return types.EmptyJID, errors.New("WhatsApp recipient must be an international phone number containing only digits")
	}
	responses, err := c.wa.IsOnWhatsApp(ctx, []string{phone})
	if err != nil {
		return types.EmptyJID, fmt.Errorf("look up WhatsApp recipient: %w", err)
	}
	if len(responses) == 0 || !responses[0].IsIn {
		return types.EmptyJID, fmt.Errorf("%s is not registered on WhatsApp", phone)
	}
	return responses[0].JID.ToNonAD(), nil
}

type localMedia struct {
	path      string
	mediaType whatsmeow.MediaType
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
		mediaType, mimeType := classifyMedia(absolute)
		files[index] = localMedia{path: absolute, mediaType: mediaType, mimeType: mimeType}
	}
	return files, nil
}

func classifyMedia(path string) (whatsmeow.MediaType, string) {
	extension := strings.ToLower(filepath.Ext(path))
	mimeType := mime.TypeByExtension(extension)
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		return whatsmeow.MediaImage, mimeType
	case ".mp4", ".3gp", ".3gpp", ".mov", ".m4v":
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		return whatsmeow.MediaVideo, mimeType
	case ".aac", ".amr", ".mp3", ".ogg", ".oga", ".opus", ".m4a", ".wav", ".flac":
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		return whatsmeow.MediaAudio, mimeType
	default:
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		return whatsmeow.MediaDocument, mimeType
	}
}

func (c *Client) sendFile(ctx context.Context, recipient types.JID, file localMedia, caption string) error {
	input, err := os.Open(file.path)
	if err != nil {
		return err
	}
	upload, uploadErr := c.wa.UploadReader(ctx, input, nil, file.mediaType)
	closeErr := input.Close()
	if uploadErr != nil {
		return fmt.Errorf("upload %s: %w", filepath.Base(file.path), uploadErr)
	}
	if closeErr != nil {
		return closeErr
	}
	message := buildMediaMessage(file, caption, upload)
	if _, err := c.wa.SendMessage(ctx, recipient, message); err != nil {
		return fmt.Errorf("send %s: %w", filepath.Base(file.path), err)
	}
	return nil
}

func buildMediaMessage(file localMedia, caption string, upload whatsmeow.UploadResponse) *waE2E.Message {
	commonURL := proto.String(upload.URL)
	directPath := proto.String(upload.DirectPath)
	fileLength := proto.Uint64(upload.FileLength)
	mimeType := proto.String(file.mimeType)
	switch file.mediaType {
	case whatsmeow.MediaImage:
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: commonURL, DirectPath: directPath, MediaKey: upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256,
			FileLength: fileLength, Mimetype: mimeType, Caption: optionalString(caption),
		}}
	case whatsmeow.MediaVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: commonURL, DirectPath: directPath, MediaKey: upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256,
			FileLength: fileLength, Mimetype: mimeType, Caption: optionalString(caption),
		}}
	case whatsmeow.MediaAudio:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: commonURL, DirectPath: directPath, MediaKey: upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256,
			FileLength: fileLength, Mimetype: mimeType,
		}}
	default:
		name := filepath.Base(file.path)
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: commonURL, DirectPath: directPath, MediaKey: upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256,
			FileLength: fileLength, Mimetype: mimeType, FileName: proto.String(name),
			Title: proto.String(name), Caption: optionalString(caption),
		}}
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return proto.String(value)
}

func partialError(sent, total int, path string, err error) error {
	return fmt.Errorf("sent %d of %d files; %s failed; %d not attempted: %w", sent, total, filepath.Base(path), total-sent-1, err)
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
