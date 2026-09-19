package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	waEvents "go.mau.fi/whatsmeow/types/events"

	tgservice "send-cli/internal/telegram"
)

// Chat runs an interactive session scoped to one WhatsApp conversation. The
// callback receives a display label resolved from the local WhatsApp contacts.
func (c *Client) Chat(ctx context.Context, recipient string, fn func(tgservice.Session, string) error) error {
	if fn == nil {
		return errors.New("WhatsApp chat callback is required")
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	target, err := c.resolveRecipient(ctx, recipient)
	if err != nil {
		return err
	}
	session := &chatSession{
		ctx:       ctx,
		client:    c,
		recipient: strings.TrimSpace(recipient),
		target:    target,
		events:    make(chan tgservice.Event, 64),
	}
	handlerID := c.wa.AddEventHandler(session.handleEvent)
	defer c.wa.RemoveEventHandler(handlerID)
	return fn(session, c.chatLabel(ctx, target, recipient))
}

func (c *Client) chatLabel(ctx context.Context, target types.JID, recipient string) string {
	recipient = strings.TrimSpace(recipient)
	name := ""
	candidates := []types.JID{target}
	if alternate, err := c.wa.Store.GetAltJID(ctx, target); err == nil && !alternate.IsEmpty() {
		candidates = append(candidates, alternate)
	}
	for _, candidate := range candidates {
		contact, err := c.wa.Store.Contacts.GetContact(ctx, candidate)
		if err == nil {
			name = contactDisplayName(contact)
			if name != "" {
				break
			}
		}
	}
	if name == "" {
		if users, err := c.wa.GetUserInfo(ctx, []types.JID{target}); err == nil {
			if user, ok := users[target]; ok && user.VerifiedName != nil {
				name = strings.TrimSpace(user.VerifiedName.Details.GetVerifiedName())
			}
		}
	}
	if name == "" || strings.EqualFold(name, recipient) {
		return recipient
	}
	return fmt.Sprintf("%s (%s)", name, recipient)
}

func contactDisplayName(contact types.ContactInfo) string {
	for _, name := range []string{contact.FullName, contact.FirstName, contact.BusinessName, contact.PushName} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return ""
}

type chatSession struct {
	ctx       context.Context
	client    *Client
	recipient string
	target    types.JID
	events    chan tgservice.Event
}

func (s *chatSession) Events() <-chan tgservice.Event {
	return s.events
}

func (s *chatSession) Send(ctx context.Context, text string, files []string) error {
	_, err := s.client.Send(ctx, Request{Recipient: s.recipient, Message: text, Files: files})
	return err
}

func (s *chatSession) handleEvent(rawEvent any) {
	switch event := rawEvent.(type) {
	case *waEvents.Message:
		if event.Message == nil || !matchesChat(event.Info, s.target) {
			return
		}
		text, file := s.eventContent(event)
		if text == "" && file == nil {
			return
		}
		sender := event.Info.PushName
		if event.Info.IsFromMe {
			sender = "You"
		} else if strings.TrimSpace(sender) == "" {
			sender = event.Info.Sender.User
		}
		s.deliver(tgservice.Event{Sender: sender, Text: text, File: file})
	case waEvents.PermanentDisconnect:
		s.deliver(tgservice.Event{Err: errors.New(event.PermanentDisconnectDescription())})
	}
}

func (s *chatSession) deliver(event tgservice.Event) {
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}

func matchesChat(info types.MessageInfo, target types.JID) bool {
	for _, candidate := range []types.JID{info.Chat, info.Sender, info.SenderAlt, info.RecipientAlt} {
		if sameJID(candidate, target) {
			return true
		}
	}
	if info.DeviceSentMeta != nil {
		if destination, err := types.ParseJID(info.DeviceSentMeta.DestinationJID); err == nil {
			return sameJID(destination, target)
		}
	}
	return false
}

func sameJID(left, right types.JID) bool {
	return !left.IsEmpty() && !right.IsEmpty() && left.ToNonAD() == right.ToNonAD()
}

func (s *chatSession) eventContent(event *waEvents.Message) (string, *tgservice.RemoteFile) {
	message := event.Message
	if text := message.GetConversation(); text != "" {
		return text, nil
	}
	if extended := message.GetExtendedTextMessage(); extended != nil {
		return extended.GetText(), nil
	}
	if image := message.GetImageMessage(); image != nil {
		return image.GetCaption(), s.remoteFile(event.Info, image, "image", ".jpg", "")
	}
	if video := message.GetVideoMessage(); video != nil {
		return video.GetCaption(), s.remoteFile(event.Info, video, "video", ".mp4", "")
	}
	if audio := message.GetAudioMessage(); audio != nil {
		return "", s.remoteFile(event.Info, audio, "audio", ".ogg", "")
	}
	if document := message.GetDocumentMessage(); document != nil {
		return document.GetCaption(), s.remoteFile(event.Info, document, "document", "", document.GetFileName())
	}
	if sticker := message.GetStickerMessage(); sticker != nil {
		return "", s.remoteFile(event.Info, sticker, "sticker", ".webp", "")
	}
	if contact := message.GetContactMessage(); contact != nil {
		return "Contact: " + contact.GetDisplayName(), nil
	}
	if location := message.GetLocationMessage(); location != nil {
		return fmt.Sprintf("Location: %.6f, %.6f", location.GetDegreesLatitude(), location.GetDegreesLongitude()), nil
	}
	return "", nil
}

type incomingMedia interface {
	whatsmeow.DownloadableMessage
	GetMimetype() string
	GetFileLength() uint64
}

func (s *chatSession) remoteFile(info types.MessageInfo, media incomingMedia, kind, fallbackExtension, preferredName string) *tgservice.RemoteFile {
	name := filepath.Base(strings.TrimSpace(preferredName))
	if name == "" || name == "." {
		extension := mediaExtension(media.GetMimetype(), fallbackExtension)
		name = fmt.Sprintf("whatsapp-%s-%s%s", kind, info.ID, extension)
	}
	return &tgservice.RemoteFile{
		Name: name,
		Size: int64(media.GetFileLength()),
		Download: func(ctx context.Context, destination string) error {
			output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			downloadErr := s.client.wa.DownloadToFile(ctx, media, output)
			closeErr := output.Close()
			if downloadErr != nil {
				return fmt.Errorf("download WhatsApp media: %w", downloadErr)
			}
			return closeErr
		},
	}
}

func mediaExtension(mimeType, fallback string) string {
	mimeType = strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	if extensions, _ := mime.ExtensionsByType(mimeType); len(extensions) > 0 {
		return extensions[0]
	}
	return fallback
}

var _ tgservice.Session = (*chatSession)(nil)
var _ incomingMedia = (*waE2E.ImageMessage)(nil)
