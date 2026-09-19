package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"send-cli/internal/config"
)

type BotService struct {
	client   *botClient
	receiver config.Receiver
}

func NewBotService(token string, receiver config.Receiver) *BotService {
	return &BotService{
		client:   newBotClient(token),
		receiver: receiver,
	}
}

func (s *BotService) Run(ctx context.Context, interactive bool, fn func(Session) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := &botSession{
		client: s.client,
		chatID: s.receiver.ID,
		events: make(chan Event, 32),
	}
	if interactive {
		go session.poll(runCtx)
	}
	return fn(session)
}

type botSession struct {
	client *botClient
	chatID int64
	events chan Event
}

func (s *botSession) Events() <-chan Event {
	return s.events
}

func (s *botSession) Send(ctx context.Context, text string, files []string) error {
	caption := text
	if len([]rune(caption)) > 1024 {
		if err := s.client.sendMessage(ctx, s.chatID, caption); err != nil {
			return err
		}
		caption = ""
	}
	if len(files) == 0 {
		if strings.TrimSpace(text) == "" {
			return errors.New("message is empty")
		}
		return s.client.sendMessage(ctx, s.chatID, text)
	}
	for i, path := range files {
		fileCaption := ""
		if i == 0 {
			fileCaption = caption
		}
		if err := s.client.sendFile(ctx, s.chatID, path, fileCaption); err != nil {
			return fmt.Errorf("send %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func (s *botSession) poll(ctx context.Context) {
	offset := int64(0)
	initial, err := s.client.getUpdates(ctx, 0, 0)
	if err == nil {
		for _, update := range initial {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
		}
	}
	for ctx.Err() == nil {
		updates, err := s.client.getUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case s.events <- Event{Err: err}:
			case <-ctx.Done():
				return
			}
			time.Sleep(time.Second)
			continue
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			message := update.message()
			if message == nil || message.Chat.ID != s.chatID {
				continue
			}
			event := s.client.eventFromMessage(message)
			select {
			case s.events <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}

func SetupBot(ctx context.Context, token, marker string, output io.Writer) (config.TelegramConfig, error) {
	client := newBotClient(strings.TrimSpace(token))
	me, err := client.getMe(ctx)
	if err != nil {
		return config.TelegramConfig{}, fmt.Errorf("verify bot token: %w", err)
	}
	if err := client.deleteWebhook(ctx); err != nil {
		return config.TelegramConfig{}, err
	}
	fmt.Fprintf(output, "Logged in as @%s.\n", me.Username)
	fmt.Fprintf(output, "Open a DM with this bot (or add it to a group) and send:\n\n  %s\n\nWaiting for that message...\n", marker)

	offset := int64(0)
	for {
		updates, err := client.getUpdates(ctx, offset, 25)
		if err != nil {
			return config.TelegramConfig{}, err
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			msg := update.message()
			if msg == nil || !matchesBotMarker(msg.Text, marker) {
				continue
			}
			title := msg.Chat.displayName()
			fmt.Fprintf(output, "Selected %s (chat %d).\n", title, msg.Chat.ID)
			return config.TelegramConfig{
				Mode:     "bot",
				BotToken: token,
				Receiver: config.Receiver{
					Kind:     "bot_chat",
					ID:       msg.Chat.ID,
					Title:    title,
					Username: msg.Chat.Username,
				},
			}, nil
		}
	}
}

type botClient struct {
	token   string
	baseURL string
	http    *http.Client
}

func newBotClient(token string) *botClient {
	return &botClient{
		token:   token,
		baseURL: "https://api.telegram.org",
		http:    &http.Client{},
	}
}

type botResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
}

type botUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type botChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (c botChat) displayName() string {
	if c.Title != "" {
		return c.Title
	}
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if name != "" {
		return name
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return strconv.FormatInt(c.ID, 10)
}

type botFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type botMessage struct {
	MessageID int64     `json:"message_id"`
	Date      int64     `json:"date"`
	From      *botUser  `json:"from"`
	Chat      botChat   `json:"chat"`
	Text      string    `json:"text"`
	Caption   string    `json:"caption"`
	Document  *botFile  `json:"document"`
	Video     *botFile  `json:"video"`
	Audio     *botFile  `json:"audio"`
	Voice     *botFile  `json:"voice"`
	Animation *botFile  `json:"animation"`
	Photo     []botFile `json:"photo"`
}

type botUpdate struct {
	UpdateID    int64       `json:"update_id"`
	Message     *botMessage `json:"message"`
	ChannelPost *botMessage `json:"channel_post"`
}

func (u botUpdate) message() *botMessage {
	if u.Message != nil {
		return u.Message
	}
	return u.ChannelPost
}

func matchesBotMarker(text, marker string) bool {
	text = strings.TrimSpace(text)
	if at := strings.IndexByte(text, '@'); at >= 0 {
		text = text[:at]
	}
	return text == marker
}

func (c *botClient) endpoint(method string) string {
	return c.baseURL + "/bot" + c.token + "/" + method
}

func (c *botClient) doForm(ctx context.Context, method string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	return nil
}

func (c *botClient) getMe(ctx context.Context) (botUser, error) {
	var response botResponse[botUser]
	if err := c.doForm(ctx, "getMe", nil, &response); err != nil {
		return botUser{}, err
	}
	if !response.OK {
		return botUser{}, errors.New(response.Description)
	}
	return response.Result, nil
}

func (c *botClient) deleteWebhook(ctx context.Context) error {
	var response botResponse[bool]
	if err := c.doForm(ctx, "deleteWebhook", url.Values{"drop_pending_updates": {"false"}}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Description)
	}
	return nil
}

func (c *botClient) getUpdates(ctx context.Context, offset int64, timeout int) ([]botUpdate, error) {
	values := url.Values{
		"offset":          {strconv.FormatInt(offset, 10)},
		"timeout":         {strconv.Itoa(timeout)},
		"allowed_updates": {`["message","channel_post"]`},
	}
	var response botResponse[[]botUpdate]
	if err := c.doForm(ctx, "getUpdates", values, &response); err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, errors.New(response.Description)
	}
	return response.Result, nil
}

func (c *botClient) sendMessage(ctx context.Context, chatID int64, text string) error {
	var response botResponse[json.RawMessage]
	err := c.doForm(ctx, "sendMessage", url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {text},
	}, &response)
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Description)
	}
	return nil
}

func (c *botClient) sendFile(ctx context.Context, chatID int64, path, caption string) error {
	method, field := mediaMethod(path)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	reader, pipe := io.Pipe()
	writer := multipart.NewWriter(pipe)
	writeResult := make(chan error, 1)
	go func() {
		var writeErr error
		if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
			writeErr = err
		} else if caption != "" {
			writeErr = writer.WriteField("caption", caption)
		}
		if writeErr == nil {
			var part io.Writer
			part, writeErr = writer.CreateFormFile(field, filepath.Base(path))
			if writeErr == nil {
				_, writeErr = io.Copy(part, file)
			}
		}
		if closeErr := writer.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = pipe.CloseWithError(writeErr)
		writeResult <- writeErr
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), reader)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return err
	}
	defer resp.Body.Close()
	if err := <-writeResult; err != nil {
		return err
	}
	var response botResponse[json.RawMessage]
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Description)
	}
	return nil
}

func (c *botClient) eventFromMessage(msg *botMessage) Event {
	sender := msg.Chat.displayName()
	if msg.From != nil {
		sender = strings.TrimSpace(msg.From.FirstName)
		if msg.From.Username != "" {
			sender = "@" + msg.From.Username
		}
	}
	event := Event{Sender: sender, Text: msg.Text}
	if event.Text == "" {
		event.Text = msg.Caption
	}
	file := msg.Document
	if file == nil {
		file = msg.Video
	}
	if file == nil {
		file = msg.Audio
	}
	if file == nil {
		file = msg.Voice
	}
	if file == nil {
		file = msg.Animation
	}
	if file == nil && len(msg.Photo) > 0 {
		copy := msg.Photo[len(msg.Photo)-1]
		copy.FileName = fmt.Sprintf("photo-%s.jpg", time.Unix(msg.Date, 0).Format("20060102-150405"))
		file = &copy
	}
	if file != nil {
		name := file.FileName
		if name == "" {
			name = fmt.Sprintf("telegram-%s", time.Unix(msg.Date, 0).Format("20060102-150405"))
		}
		fileID := file.FileID
		event.File = &RemoteFile{
			Name: name,
			Size: file.FileSize,
			Download: func(ctx context.Context, path string) error {
				return c.download(ctx, fileID, path)
			},
		}
	}
	return event
}

func (c *botClient) download(ctx context.Context, fileID, destination string) error {
	var response botResponse[botFile]
	if err := c.doForm(ctx, "getFile", url.Values{"file_id": {fileID}}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Description)
	}
	downloadURL := c.baseURL + "/file/bot" + c.token + "/" + response.Result.FilePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, resp.Body)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
