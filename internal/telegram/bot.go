package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
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
	client                  *botClient
	receiver                config.Receiver
	updateOffset            int64
	updateOffsetInitialized bool
	saveUpdateOffset        func(int64) error
}

func (s *BotService) ResumeUpdates(offset int64, initialized bool, save func(int64) error) {
	s.updateOffset = offset
	s.updateOffsetInitialized = initialized
	s.saveUpdateOffset = save
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
		client:                  s.client,
		chatID:                  s.receiver.ID,
		events:                  make(chan Event, 32),
		updateOffset:            s.updateOffset,
		updateOffsetInitialized: s.updateOffsetInitialized,
		saveUpdateOffset:        s.saveUpdateOffset,
	}
	if interactive {
		go session.poll(runCtx)
	}
	return fn(session)
}

type botSession struct {
	client                  *botClient
	chatID                  int64
	events                  chan Event
	updateOffset            int64
	updateOffsetInitialized bool
	saveUpdateOffset        func(int64) error
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
	if mediaGroupCompatible(files) {
		return s.sendMediaGroups(ctx, files, caption)
	}
	for i, path := range files {
		fileCaption := ""
		if i == 0 {
			fileCaption = caption
		}
		if err := s.client.sendFile(ctx, s.chatID, path, fileCaption); err != nil {
			return fmt.Errorf(
				"sent %d of %d files; %s failed; %d not attempted: %w",
				i,
				len(files),
				filepath.Base(path),
				len(files)-i-1,
				err,
			)
		}
	}
	return nil
}

func (s *botSession) sendMediaGroups(ctx context.Context, files []string, caption string) error {
	sent := 0
	for sent < len(files) {
		remaining := len(files) - sent
		groupSize := min(remaining, 10)
		group := files[sent : sent+groupSize]
		groupCaption := ""
		if sent == 0 {
			groupCaption = caption
		}

		var err error
		if len(group) == 1 {
			err = s.client.sendFile(ctx, s.chatID, group[0], groupCaption)
		} else {
			err = s.client.sendMediaGroup(ctx, s.chatID, group, groupCaption)
		}
		if err != nil {
			return fmt.Errorf(
				"sent %d of %d files; media beginning with %s failed; %d not attempted: %w",
				sent,
				len(files),
				filepath.Base(group[0]),
				len(files)-sent,
				err,
			)
		}
		sent += len(group)
	}
	return nil
}

func (s *botSession) poll(ctx context.Context) {
	offset := s.updateOffset
	failures := 0
	if !s.updateOffsetInitialized {
		for ctx.Err() == nil {
			initial, err := s.client.getUpdates(ctx, -1, 0)
			if err != nil {
				select {
				case s.events <- Event{Err: err}:
				case <-ctx.Done():
					return
				}
				if !waitForRetry(ctx, pollRetryDelay(failures)) {
					return
				}
				failures++
				continue
			}
			for _, update := range initial {
				if update.UpdateID >= offset {
					offset = update.UpdateID + 1
				}
			}
			s.persistUpdateOffset(ctx, offset)
			failures = 0
			break
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
			if !waitForRetry(ctx, pollRetryDelay(failures)) {
				return
			}
			failures++
			continue
		}
		failures = 0
		nextOffset := offset
		for _, update := range updates {
			if update.UpdateID >= nextOffset {
				nextOffset = update.UpdateID + 1
			}
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
		if nextOffset != offset {
			offset = nextOffset
			s.persistUpdateOffset(ctx, offset)
		}
	}
}

func (s *botSession) persistUpdateOffset(ctx context.Context, offset int64) {
	if s.saveUpdateOffset == nil {
		return
	}
	if err := s.saveUpdateOffset(offset); err != nil {
		select {
		case s.events <- Event{Err: fmt.Errorf("save Telegram receive position: %w", err)}:
		case <-ctx.Done():
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
	fmt.Fprintf(output, "Connected as @%s.\n", me.Username)
	fmt.Fprintf(output, "Open a DM with this bot (or add it to a group or channel) and send:\n\n  %s\n\nWaiting for that message...\n", marker)

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
			if msg.Chat.Type == "group" || msg.Chat.Type == "supergroup" {
				fmt.Fprintln(output, "Group privacy note: to receive ordinary group messages, make the bot an administrator or disable privacy for it with BotFather /setprivacy.")
			}
			return config.TelegramConfig{
				BotToken:                token,
				UpdateOffset:            offset,
				UpdateOffsetInitialized: true,
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
	token         string
	baseURL       string
	http          *http.Client
	retryAttempts int
	retryBase     time.Duration
}

func newBotClient(token string) *botClient {
	return &botClient{
		token:         token,
		baseURL:       "https://api.telegram.org",
		http:          &http.Client{},
		retryAttempts: 4,
		retryBase:     500 * time.Millisecond,
	}
}

type botResponse[T any] struct {
	OK          bool                  `json:"ok"`
	Result      T                     `json:"result"`
	ErrorCode   int                   `json:"error_code"`
	Description string                `json:"description"`
	Parameters  botResponseParameters `json:"parameters"`
}

type botResponseParameters struct {
	RetryAfter int `json:"retry_after"`
}

type botAPIError struct {
	code        int
	description string
	retryAfter  time.Duration
}

func (e *botAPIError) Error() string {
	switch e.code {
	case http.StatusUnauthorized:
		return "Telegram bot token is invalid or revoked: " + e.description
	case http.StatusForbidden:
		return "Telegram denied access; the bot may be blocked or removed from the selected chat: " + e.description
	case http.StatusConflict:
		return "Telegram update polling conflict; another process or webhook may be receiving this bot's updates: " + e.description
	case http.StatusTooManyRequests:
		return "Telegram rate limit: " + e.description
	default:
		if e.description != "" {
			return e.description
		}
		return fmt.Sprintf("Telegram API error %d", e.code)
	}
}

func (r *botResponse[T]) responseError() error {
	if r.OK {
		return nil
	}
	return &botAPIError{
		code:        r.ErrorCode,
		description: r.Description,
		retryAfter:  time.Duration(r.Parameters.RetryAfter) * time.Second,
	}
}

type responseWithError interface {
	responseError() error
}

type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("Telegram returned HTTP %d", e.code)
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

type redactedRequestError struct {
	cause error
	token string
}

func (e redactedRequestError) Error() string {
	if e.token == "" {
		return e.cause.Error()
	}
	return strings.ReplaceAll(e.cause.Error(), e.token, "[redacted]")
}

func (e redactedRequestError) Unwrap() error { return e.cause }

func (c *botClient) do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redactedRequestError{cause: err, token: c.token}
	}
	return resp, nil
}

func (c *botClient) doForm(ctx context.Context, method string, values url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		err := c.doFormOnce(ctx, method, values, out)
		if err == nil {
			return nil
		}
		delay, retry := c.retryDelay(err, attempt)
		if !retry || attempt+1 >= c.retryAttemptLimit() {
			return err
		}
		if !waitForRetry(ctx, delay) {
			return ctx.Err()
		}
	}
}

func (c *botClient) doFormOnce(ctx context.Context, method string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &httpStatusError{code: resp.StatusCode}
		}
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if response, ok := out.(responseWithError); ok {
		if err := response.responseError(); err != nil {
			return err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{code: resp.StatusCode}
	}
	return nil
}

func (c *botClient) retryAttemptLimit() int {
	if c.retryAttempts < 1 {
		return 1
	}
	return c.retryAttempts
}

func (c *botClient) retryDelay(err error, attempt int) (time.Duration, bool) {
	var apiErr *botAPIError
	if errors.As(err, &apiErr) {
		if apiErr.code == http.StatusTooManyRequests {
			if apiErr.retryAfter > 0 {
				return apiErr.retryAfter, true
			}
			return exponentialDelay(c.retryBase, attempt, 30*time.Second), true
		}
		if apiErr.code >= 500 {
			return exponentialDelay(c.retryBase, attempt, 30*time.Second), true
		}
		return 0, false
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.code >= 500 {
		return exponentialDelay(c.retryBase, attempt, 30*time.Second), true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return exponentialDelay(c.retryBase, attempt, 30*time.Second), true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return exponentialDelay(c.retryBase, attempt, 30*time.Second), true
	}
	return 0, false
}

func exponentialDelay(base time.Duration, attempt int, maximum time.Duration) time.Duration {
	if base <= 0 {
		base = time.Millisecond
	}
	delay := base
	for index := 0; index < attempt && delay < maximum/2; index++ {
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func pollRetryDelay(failures int) time.Duration {
	return exponentialDelay(time.Second, failures, 30*time.Second)
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *botClient) getMe(ctx context.Context) (botUser, error) {
	var response botResponse[botUser]
	if err := c.doForm(ctx, "getMe", nil, &response); err != nil {
		return botUser{}, err
	}
	return response.Result, nil
}

func (c *botClient) deleteWebhook(ctx context.Context) error {
	var response botResponse[bool]
	if err := c.doForm(ctx, "deleteWebhook", url.Values{"drop_pending_updates": {"false"}}, &response); err != nil {
		return err
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
	return nil
}

func (c *botClient) sendFile(ctx context.Context, chatID int64, path, caption string) error {
	for attempt := 0; ; attempt++ {
		err := c.sendFileOnce(ctx, chatID, path, caption)
		if err == nil {
			return nil
		}
		delay, retry := c.retryDelay(err, attempt)
		if !retry || attempt+1 >= c.retryAttemptLimit() {
			return err
		}
		if !waitForRetry(ctx, delay) {
			return ctx.Err()
		}
	}
}

func (c *botClient) sendMediaGroup(ctx context.Context, chatID int64, paths []string, caption string) error {
	for attempt := 0; ; attempt++ {
		err := c.sendMediaGroupOnce(ctx, chatID, paths, caption)
		if err == nil {
			return nil
		}
		delay, retry := c.retryDelay(err, attempt)
		if !retry || attempt+1 >= c.retryAttemptLimit() {
			return err
		}
		if !waitForRetry(ctx, delay) {
			return ctx.Err()
		}
	}
}

func (c *botClient) sendMediaGroupOnce(ctx context.Context, chatID int64, paths []string, caption string) error {
	type inputMedia struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption,omitempty"`
	}

	files := make([]*os.File, 0, len(paths))
	media := make([]inputMedia, len(paths))
	totals := make([]int64, len(paths))
	for index, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			closeFiles(files)
			return err
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			closeFiles(files)
			return err
		}
		if err := validateUploadSize(path, info.Size()); err != nil {
			_ = file.Close()
			closeFiles(files)
			return err
		}
		files = append(files, file)
		totals[index] = info.Size()
		method, _ := mediaMethod(path)
		mediaType := "photo"
		if method == "sendVideo" {
			mediaType = "video"
		}
		media[index] = inputMedia{Type: mediaType, Media: fmt.Sprintf("attach://file%d", index)}
		if index == 0 {
			media[index].Caption = caption
		}
	}
	defer closeFiles(files)

	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return err
	}
	reader, pipe := io.Pipe()
	writer := multipart.NewWriter(pipe)
	writeResult := make(chan error, 1)
	go func() {
		writeErr := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
		if writeErr == nil {
			writeErr = writer.WriteField("media", string(mediaJSON))
		}
		for index, file := range files {
			if writeErr != nil {
				break
			}
			var part io.Writer
			part, writeErr = writer.CreateFormFile(fmt.Sprintf("file%d", index), filepath.Base(paths[index]))
			if writeErr == nil {
				upload := newProgressReader(ctx, file, TransferUpload, filepath.Base(paths[index]), totals[index])
				_, writeErr = io.Copy(part, upload)
			}
		}
		if closeErr := writer.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = pipe.CloseWithError(writeErr)
		writeResult <- writeErr
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("sendMediaGroup"), reader)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.do(req)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return err
	}
	defer resp.Body.Close()
	if err := <-writeResult; err != nil {
		return err
	}
	return decodeBotResponse(resp)
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func (c *botClient) sendFileOnce(ctx context.Context, chatID int64, path, caption string) error {
	method, field := mediaMethod(path)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := validateUploadSize(path, info.Size()); err != nil {
		return err
	}
	upload := newProgressReader(ctx, file, TransferUpload, filepath.Base(path), info.Size())

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
				_, writeErr = io.Copy(part, upload)
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
	resp, err := c.do(req)
	if err != nil {
		_ = reader.Close()
		<-writeResult
		return err
	}
	defer resp.Body.Close()
	if err := <-writeResult; err != nil {
		return err
	}
	return decodeBotResponse(resp)
}

func decodeBotResponse(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var response botResponse[json.RawMessage]
	if err := json.Unmarshal(body, &response); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &httpStatusError{code: resp.StatusCode}
		}
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if err := response.responseError(); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{code: resp.StatusCode}
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
		fileSize := file.FileSize
		event.File = &RemoteFile{
			Name: name,
			Size: file.FileSize,
			Download: func(ctx context.Context, path string) error {
				if fileSize > officialDownloadLimit {
					return fmt.Errorf(
						"%s is %.1f MiB; the official Telegram Bot API can download files only up to 20 MiB (a self-hosted local Bot API server has different limits)",
						filepath.Base(name),
						float64(fileSize)/(1024*1024),
					)
				}
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
	downloadURL := c.baseURL + "/file/bot" + c.token + "/" + response.Result.FilePath
	for attempt := 0; ; attempt++ {
		err := c.downloadOnce(ctx, downloadURL, destination, response.Result.FileSize)
		if err == nil {
			return nil
		}
		delay, retry := c.retryDelay(err, attempt)
		if !retry || attempt+1 >= c.retryAttemptLimit() {
			return err
		}
		if !waitForRetry(ctx, delay) {
			return ctx.Err()
		}
	}
}

func (c *botClient) downloadOnce(ctx context.Context, downloadURL, destination string, total int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{code: resp.StatusCode}
	}
	if total <= 0 {
		total = resp.ContentLength
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	name := filepath.Base(destination)
	download := newProgressReader(ctx, resp.Body, TransferDownload, name, total)
	_, copyErr := io.Copy(output, download)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type progressReader struct {
	ctx         context.Context
	reader      io.Reader
	direction   TransferDirection
	name        string
	total       int64
	transferred int64
	started     time.Time
	lastReport  time.Time
}

func newProgressReader(ctx context.Context, reader io.Reader, direction TransferDirection, name string, total int64) *progressReader {
	now := time.Now()
	progress := &progressReader{
		ctx:        ctx,
		reader:     reader,
		direction:  direction,
		name:       name,
		total:      total,
		started:    now,
		lastReport: now,
	}
	progress.report(now)
	return progress
}

func (r *progressReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.transferred += int64(count)
	now := time.Now()
	if err != nil || now.Sub(r.lastReport) >= 200*time.Millisecond || r.total > 0 && r.transferred >= r.total {
		r.report(now)
		r.lastReport = now
	}
	return count, err
}

func (r *progressReader) report(now time.Time) {
	reportTransferProgress(r.ctx, TransferProgress{
		Direction:   r.direction,
		Name:        r.name,
		Transferred: r.transferred,
		Total:       r.total,
		Elapsed:     now.Sub(r.started),
	})
}
