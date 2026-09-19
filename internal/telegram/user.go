package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"send-cli/internal/config"

	"github.com/gotd/td/session"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	messagepeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// BuiltinAPIID and BuiltinAPIHash are supplied to release builds with:
// -ldflags "-X send-cli/internal/telegram.BuiltinAPIID=... -X send-cli/internal/telegram.BuiltinAPIHash=..."
// Environment overrides make local development and CI possible without committing credentials.
var (
	BuiltinAPIID   string
	BuiltinAPIHash string
)

func UserCredentials() (int, string, error) {
	idText := os.Getenv("SEND_TELEGRAM_API_ID")
	hash := os.Getenv("SEND_TELEGRAM_API_HASH")
	if idText == "" {
		idText = BuiltinAPIID
	}
	if hash == "" {
		hash = BuiltinAPIHash
	}
	id, err := strconv.Atoi(idText)
	if err != nil || id <= 0 || hash == "" {
		return 0, "", errors.New("this build has no Telegram user-login credentials; use bot mode or build with SEND_TELEGRAM_API_ID and SEND_TELEGRAM_API_HASH")
	}
	return id, hash, nil
}

type UserService struct {
	appID       int
	appHash     string
	sessionPath string
	receiver    config.Receiver
}

func NewUserService(sessionPath string, receiver config.Receiver) (*UserService, error) {
	id, hash, err := UserCredentials()
	if err != nil {
		return nil, err
	}
	return &UserService{
		appID:       id,
		appHash:     hash,
		sessionPath: sessionPath,
		receiver:    receiver,
	}, nil
}

func (s *UserService) Run(ctx context.Context, interactive bool, fn func(Session) error) error {
	dispatcher := tg.NewUpdateDispatcher()
	events := make(chan Event, 32)
	var client *gotd.Client
	if interactive {
		handler := func(ctx context.Context, entities tg.Entities, raw tg.MessageClass) error {
			msg, ok := raw.(*tg.Message)
			if !ok || msg.Out || !samePeer(msg.PeerID, s.receiver) {
				return nil
			}
			event := userEvent(client.API(), entities, msg)
			select {
			case events <- event:
			case <-ctx.Done():
			}
			return nil
		}
		dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
			return handler(ctx, e, update.Message)
		})
		dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewChannelMessage) error {
			return handler(ctx, e, update.Message)
		})
	}

	client = gotd.NewClient(s.appID, s.appHash, gotd.Options{
		SessionStorage: &session.FileStorage{Path: s.sessionPath},
		UpdateHandler:  dispatcher,
		NoUpdates:      !interactive,
	})
	return client.Run(ctx, func(ctx context.Context) error {
		if err := authenticate(ctx, client); err != nil {
			return err
		}
		current := &userSession{
			api:      client.API(),
			receiver: s.receiver,
			events:   events,
		}
		return fn(current)
	})
}

type userSession struct {
	api      *tg.Client
	receiver config.Receiver
	events   chan Event
}

func (s *userSession) Events() <-chan Event {
	return s.events
}

func (s *userSession) Send(ctx context.Context, text string, files []string) error {
	peer, err := inputPeer(s.receiver)
	if err != nil {
		return err
	}
	up := uploader.NewUploader(s.api)
	sender := message.NewSender(s.api).WithUploader(up)
	target := sender.To(peer)

	caption := text
	if len([]rune(caption)) > 1024 {
		if _, err := target.Text(ctx, caption); err != nil {
			return err
		}
		caption = ""
	}
	if len(files) == 0 {
		if strings.TrimSpace(text) == "" {
			return errors.New("message is empty")
		}
		_, err := target.Text(ctx, text)
		return err
	}
	for i, path := range files {
		uploaded, err := up.FromPath(ctx, path)
		if err != nil {
			return fmt.Errorf("upload %s: %w", filepath.Base(path), err)
		}
		fileCaption := ""
		if i == 0 {
			fileCaption = caption
		}
		var captionOptions []message.StyledTextOption
		if fileCaption != "" {
			captionOptions = append(captionOptions, styling.Plain(fileCaption))
		}
		method, _ := mediaMethod(path)
		switch method {
		case "sendPhoto":
			_, err = target.Media(ctx, message.UploadedPhoto(uploaded, captionOptions...))
		case "sendVideo":
			video := message.UploadedDocument(uploaded, captionOptions...).
				Filename(filepath.Base(path)).
				MIME(mimeType(path)).
				Video().
				SupportsStreaming()
			_, err = target.Media(ctx, video)
		default:
			document := message.UploadedDocument(uploaded, captionOptions...).
				Filename(filepath.Base(path)).
				MIME(mimeType(path))
			_, err = target.Media(ctx, document)
		}
		if err != nil {
			return fmt.Errorf("send %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func SetupUser(ctx context.Context, sessionPath, marker string, output io.Writer) (config.TelegramConfig, error) {
	id, hash, err := UserCredentials()
	if err != nil {
		return config.TelegramConfig{}, err
	}
	dispatcher := tg.NewUpdateDispatcher()
	type selection struct {
		receiver config.Receiver
		err      error
	}
	selected := make(chan selection, 1)
	handler := func(entities tg.Entities, raw tg.MessageClass) {
		msg, ok := raw.(*tg.Message)
		if !ok || !msg.Out || strings.TrimSpace(msg.Message) != marker {
			return
		}
		receiver, err := receiverFromPeer(entities, msg.PeerID)
		select {
		case selected <- selection{receiver: receiver, err: err}:
		default:
		}
	}
	dispatcher.OnNewMessage(func(_ context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		handler(e, update.Message)
		return nil
	})
	dispatcher.OnNewChannelMessage(func(_ context.Context, e tg.Entities, update *tg.UpdateNewChannelMessage) error {
		handler(e, update.Message)
		return nil
	})

	client := gotd.NewClient(id, hash, gotd.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		UpdateHandler:  dispatcher,
	})
	var chosen config.Receiver
	err = client.Run(ctx, func(ctx context.Context) error {
		if err := authenticate(ctx, client); err != nil {
			return err
		}
		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("read Telegram profile: %w", err)
		}
		fmt.Fprintf(output, "Logged in as %s.\n", displayUser(self))
		fmt.Fprintf(output, "From Telegram on any device, send this exact message to the desired chat:\n\n  %s\n\nWaiting for that outgoing message...\n", marker)
		select {
		case result := <-selected:
			if result.err != nil {
				return result.err
			}
			fmt.Fprintf(output, "Selected %s.\n", result.receiver.Title)
			chosen = result.receiver
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		return config.TelegramConfig{}, err
	}
	return config.TelegramConfig{
		Mode:     "user",
		Receiver: chosen,
	}, nil
}

func authenticate(ctx context.Context, client *gotd.Client) error {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("check Telegram login: %w", err)
	}
	if status.Authorized {
		return nil
	}
	flow := auth.NewFlow(terminalAuthenticator{}, auth.SendCodeOptions{})
	if err := client.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("Telegram login: %w", err)
	}
	return nil
}

type terminalAuthenticator struct{}

func (terminalAuthenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("new Telegram account sign-up is not supported; create the account in Telegram first")
}

func (terminalAuthenticator) AcceptTermsOfService(_ context.Context, terms tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: terms}
}

func (terminalAuthenticator) Code(context.Context, *tg.AuthSentCode) (string, error) {
	return promptLine("Login code: ")
}

func (terminalAuthenticator) Phone(context.Context) (string, error) {
	return promptLine("Phone number (international format): ")
}

func (terminalAuthenticator) Password(context.Context) (string, error) {
	fmt.Fprint(os.Stderr, "Two-factor password: ")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(value)), err
	}
	return promptLine("")
}

func promptLine(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	value, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(value), err
}

func inputPeer(receiver config.Receiver) (tg.InputPeerClass, error) {
	switch receiver.Kind {
	case "user":
		return &tg.InputPeerUser{UserID: receiver.ID, AccessHash: receiver.AccessHash}, nil
	case "chat":
		return &tg.InputPeerChat{ChatID: receiver.ID}, nil
	case "channel":
		return &tg.InputPeerChannel{ChannelID: receiver.ID, AccessHash: receiver.AccessHash}, nil
	default:
		return nil, fmt.Errorf("unsupported Telegram receiver kind %q; run setup again", receiver.Kind)
	}
}

func receiverFromPeer(entities tg.Entities, peerID tg.PeerClass) (config.Receiver, error) {
	peer, err := messagepeer.EntitiesFromUpdate(entities).ExtractPeer(peerID)
	if err != nil {
		return config.Receiver{}, fmt.Errorf("resolve selected chat: %w; send the setup marker once more", err)
	}
	switch value := peer.(type) {
	case *tg.InputPeerUser:
		user := entities.Users[value.UserID]
		receiver := config.Receiver{Kind: "user", ID: value.UserID, AccessHash: value.AccessHash}
		if user != nil {
			receiver.Title = displayUser(user)
			receiver.Username = user.Username
		}
		return receiver, nil
	case *tg.InputPeerChat:
		receiver := config.Receiver{Kind: "chat", ID: value.ChatID}
		if chat := entities.Chats[value.ChatID]; chat != nil {
			receiver.Title = chat.Title
		}
		return receiver, nil
	case *tg.InputPeerChannel:
		receiver := config.Receiver{Kind: "channel", ID: value.ChannelID, AccessHash: value.AccessHash}
		if channel := entities.Channels[value.ChannelID]; channel != nil {
			receiver.Title = channel.Title
			receiver.Username = channel.Username
		}
		return receiver, nil
	default:
		return config.Receiver{}, fmt.Errorf("unsupported selected peer %T", peer)
	}
}

func samePeer(peer tg.PeerClass, receiver config.Receiver) bool {
	switch value := peer.(type) {
	case *tg.PeerUser:
		return receiver.Kind == "user" && receiver.ID == value.UserID
	case *tg.PeerChat:
		return receiver.Kind == "chat" && receiver.ID == value.ChatID
	case *tg.PeerChannel:
		return receiver.Kind == "channel" && receiver.ID == value.ChannelID
	default:
		return false
	}
}

func displayUser(user *tg.User) string {
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" && user.Username != "" {
		return "@" + user.Username
	}
	if name == "" {
		return strconv.FormatInt(user.ID, 10)
	}
	return name
}

func senderName(entities tg.Entities, peer tg.PeerClass) string {
	switch value := peer.(type) {
	case *tg.PeerUser:
		if user := entities.Users[value.UserID]; user != nil {
			return displayUser(user)
		}
	case *tg.PeerChat:
		if chat := entities.Chats[value.ChatID]; chat != nil {
			return chat.Title
		}
	case *tg.PeerChannel:
		if channel := entities.Channels[value.ChannelID]; channel != nil {
			return channel.Title
		}
	}
	return "Telegram"
}

func userEvent(api *tg.Client, entities tg.Entities, msg *tg.Message) Event {
	sender := "Telegram"
	if from, ok := msg.GetFromID(); ok {
		sender = senderName(entities, from)
	}
	event := Event{Sender: sender, Text: msg.Message}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		if media.Document == nil {
			break
		}
		doc, ok := media.Document.AsNotEmpty()
		if !ok {
			break
		}
		name := documentName(doc)
		location := &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		}
		event.File = &RemoteFile{
			Name: name,
			Size: doc.Size,
			Download: func(ctx context.Context, destination string) error {
				_, err := downloader.NewDownloader().Download(api, location).ToPath(ctx, destination)
				return err
			},
		}
	case *tg.MessageMediaPhoto:
		if media.Photo == nil {
			break
		}
		photo, ok := media.Photo.AsNotEmpty()
		if !ok || len(photo.Sizes) == 0 {
			break
		}
		size := photo.Sizes[len(photo.Sizes)-1]
		location := &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     size.GetType(),
		}
		event.File = &RemoteFile{
			Name: fmt.Sprintf("photo-%s.jpg", time.Unix(int64(msg.Date), 0).Format("20060102-150405")),
			Download: func(ctx context.Context, destination string) error {
				_, err := downloader.NewDownloader().Download(api, location).ToPath(ctx, destination)
				return err
			},
		}
	}
	return event
}

func documentName(doc *tg.Document) string {
	for _, attribute := range doc.Attributes {
		if filename, ok := attribute.(*tg.DocumentAttributeFilename); ok && filename.FileName != "" {
			return filepath.Base(filename.FileName)
		}
	}
	return fmt.Sprintf("telegram-%d", doc.ID)
}
