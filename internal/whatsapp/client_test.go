package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	waEvents "go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestOpenCreatesReusableSessionStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "whatsapp.db")
	client, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.Linked() {
		t.Fatal("new session unexpectedly linked")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session database missing: %v", err)
	}
	linked, err := IsLinked(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("empty session database unexpectedly linked")
	}
}

func TestClassifyMedia(t *testing.T) {
	tests := []struct {
		path string
		want whatsmeow.MediaType
	}{
		{"photo.jpg", whatsmeow.MediaImage},
		{"clip.mp4", whatsmeow.MediaVideo},
		{"voice.ogg", whatsmeow.MediaAudio},
		{"report.pdf", whatsmeow.MediaDocument},
		{"archive.zip", whatsmeow.MediaDocument},
	}
	for _, test := range tests {
		got, mimeType := classifyMedia(test.path)
		if got != test.want || mimeType == "" {
			t.Errorf("classifyMedia(%q) = %q, %q", test.path, got, mimeType)
		}
	}
}

func TestBuildMediaMessage(t *testing.T) {
	upload := whatsmeow.UploadResponse{
		URL: "https://example.test/media", DirectPath: "/media", MediaKey: []byte{1},
		FileEncSHA256: []byte{2}, FileSHA256: []byte{3}, FileLength: 42,
	}
	image := buildMediaMessage(localMedia{path: "photo.jpg", mediaType: whatsmeow.MediaImage, mimeType: "image/jpeg"}, "caption", upload)
	if image.GetImageMessage() == nil || image.GetImageMessage().GetCaption() != "caption" {
		t.Fatalf("image message = %#v", image)
	}
	document := buildMediaMessage(localMedia{path: "report.pdf", mediaType: whatsmeow.MediaDocument, mimeType: "application/pdf"}, "", upload)
	if document.GetDocumentMessage() == nil || document.GetDocumentMessage().GetFileName() != "report.pdf" {
		t.Fatalf("document message = %#v", document)
	}
}

func TestValidateFilesBeforeSending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := validateFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].mediaType != whatsmeow.MediaDocument {
		t.Fatalf("files = %#v", files)
	}
	if _, err := validateFiles([]string{"missing.file"}); err == nil {
		t.Fatal("missing file unexpectedly accepted")
	}
}

func TestNormalizeRecipient(t *testing.T) {
	if got := normalizeRecipient("+91 (98765) 43210"); got != "919876543210" {
		t.Fatalf("normalized recipient = %q", got)
	}
	if digitsOnly("12x") {
		t.Fatal("invalid recipient accepted")
	}
}

func TestWhatsAppChatMatchesAlternativeJID(t *testing.T) {
	target := types.NewJID("123456789", types.HiddenUserServer)
	info := types.MessageInfo{MessageSource: types.MessageSource{
		Chat:      types.NewJID("919876543210", types.DefaultUserServer),
		SenderAlt: target,
	}}
	if !matchesChat(info, target) {
		t.Fatal("chat did not match the sender's alternative JID")
	}
	if matchesChat(info, types.NewJID("999", types.HiddenUserServer)) {
		t.Fatal("chat matched an unrelated JID")
	}
}

func TestWhatsAppChatExtractsTextAndSafeDocumentName(t *testing.T) {
	session := &chatSession{}
	text, file := session.eventContent(&waEvents.Message{Message: &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hello")},
	}})
	if text != "hello" || file != nil {
		t.Fatalf("text=%q file=%#v", text, file)
	}

	caption, file := session.eventContent(&waEvents.Message{
		Info: types.MessageInfo{ID: "ABC123"},
		Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			FileName: proto.String("../report.pdf"), Caption: proto.String("report"),
			FileLength: proto.Uint64(42), Mimetype: proto.String("application/pdf"),
		}},
	})
	if caption != "report" || file == nil || file.Name != "report.pdf" || file.Size != 42 {
		t.Fatalf("caption=%q file=%#v", caption, file)
	}
}

func TestContactDisplayNamePreference(t *testing.T) {
	contact := types.ContactInfo{
		FullName: "Saved Name", FirstName: "Saved", BusinessName: "Business", PushName: "Push",
	}
	if got := contactDisplayName(contact); got != "Saved Name" {
		t.Fatalf("display name = %q", got)
	}
	contact.FullName = ""
	contact.FirstName = ""
	if got := contactDisplayName(contact); got != "Business" {
		t.Fatalf("fallback display name = %q", got)
	}
}
