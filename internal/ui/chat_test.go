package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgservice "send-cli/internal/telegram"
)

func TestRendererUsesOnePromptForMultilineDraft(t *testing.T) {
	var output bytes.Buffer
	renderer := &renderer{output: &output, width: 80}
	renderer.draw(&chatConsole{draft: []rune("one\ntwo\nthree")})

	if got := strings.Count(output.String(), "U> "); got != 1 {
		t.Fatalf("rendered %d prompts, want 1: %q", got, output.String())
	}
	if strings.Contains(output.String(), "1 one") || strings.Contains(output.String(), "2 two") {
		t.Fatalf("renderer added line numbers: %q", output.String())
	}
}

func TestRendererLeavesCursorAtPromptAfterStatus(t *testing.T) {
	var output bytes.Buffer
	renderer := &renderer{output: &output, width: 80}
	renderer.draw(&chatConsole{status: "Sending..."})

	if got := output.String(); !strings.HasSuffix(got, "U> ") {
		t.Fatalf("renderer left cursor away from prompt: %q", got)
	}
}

func TestPastedFiles(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first.txt")
	second := filepath.Join(t.TempDir(), "second.txt")
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, ok := pastedFiles(first + "\n" + second)
	if !ok || len(files) != 2 {
		t.Fatalf("got files=%v ok=%v", files, ok)
	}
	if _, ok := pastedFiles("ordinary pasted text"); ok {
		t.Fatal("ordinary text must not be treated as files")
	}
}

func TestFormatBytes(t *testing.T) {
	if got := formatBytes(1536); got != "1.5 KiB" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatTransferProgress(t *testing.T) {
	progress := tgservice.TransferProgress{
		Direction:   tgservice.TransferUpload,
		Name:        "report.zip",
		Transferred: 512,
		Total:       1024,
		Elapsed:     time.Second,
	}
	if got := formatTransferProgress(progress); got != "Uploading report.zip: 512 B / 1.0 KiB (50%) at 512 B/s" {
		t.Fatalf("got %q", got)
	}
}

func TestInputParserControls(t *testing.T) {
	parser := inputParser{}
	events := parser.Feed([]byte{'h', 'i', 0x08, 0x0a, 0x1c, 0x0f, 0x16, 0x0d, 0x03})
	kinds := []inputKind{
		inputText, inputText, inputBackspace, inputNewline, inputNewline,
		inputPick, inputClipboard, inputEnter, inputQuit,
	}
	if len(events) != len(kinds) {
		t.Fatalf("got %d events, want %d: %#v", len(events), len(kinds), events)
	}
	for index, want := range kinds {
		if events[index].kind != want {
			t.Errorf("event %d kind = %d, want %d", index, events[index].kind, want)
		}
	}
}

func TestInputParserMultilineSequences(t *testing.T) {
	for _, sequence := range newlineSequences {
		parser := inputParser{}
		events := parser.Feed(sequence)
		if len(events) != 1 || events[0].kind != inputNewline {
			t.Errorf("sequence %q produced %#v", sequence, events)
		}
	}
}

func TestInputParserEditingSequences(t *testing.T) {
	parser := inputParser{}
	events := parser.Feed([]byte("\x1b[D\x1b[C\x1b[H\x1b[F\x1b[3~\x01\x05\x15"))
	want := []inputKind{
		inputLeft, inputRight, inputHome, inputEnd, inputDelete, inputHome, inputEnd, inputClear,
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %#v", len(events), len(want), events)
	}
	for index := range want {
		if events[index].kind != want[index] {
			t.Errorf("event %d kind = %d, want %d", index, events[index].kind, want[index])
		}
	}
}

func TestComposerEditsAtCursor(t *testing.T) {
	console := &chatConsole{}
	console.handleInput(inputEvent{kind: inputText, text: "hello"})
	console.handleInput(inputEvent{kind: inputLeft})
	console.handleInput(inputEvent{kind: inputLeft})
	console.handleInput(inputEvent{kind: inputText, text: "X"})
	if got := string(console.draft); got != "helXlo" {
		t.Fatalf("inserted draft = %q", got)
	}
	console.handleInput(inputEvent{kind: inputBackspace})
	console.handleInput(inputEvent{kind: inputHome})
	console.handleInput(inputEvent{kind: inputDelete})
	if got := string(console.draft); got != "ello" {
		t.Fatalf("edited draft = %q", got)
	}
}

func TestBackspaceRemovesLastAttachmentFromEmptyDraft(t *testing.T) {
	console := &chatConsole{attachments: []selectedFile{{Path: "one.txt"}, {Path: "two.txt"}}}
	console.handleInput(inputEvent{kind: inputBackspace})
	if len(console.attachments) != 1 || console.attachments[0].Path != "one.txt" {
		t.Fatalf("attachments = %#v", console.attachments)
	}
}

func TestInputParserBracketedPasteAcrossReads(t *testing.T) {
	parser := inputParser{}
	if events := parser.Feed([]byte("\x1b[20")); len(events) != 0 {
		t.Fatalf("partial start produced %#v", events)
	}
	if events := parser.Feed([]byte("0~some\ntext\x1b[20")); len(events) != 0 {
		t.Fatalf("partial paste produced %#v", events)
	}
	events := parser.Feed([]byte("1~"))
	if len(events) != 1 || events[0].kind != inputPaste || events[0].text != "some\ntext" {
		t.Fatalf("unexpected paste events: %#v", events)
	}
}

func TestSafeTerminalTextRemovesControlSequences(t *testing.T) {
	if got := safeTerminalText("hello\x1b[2J\r\nworld"); got != "hello[2J\nworld" {
		t.Fatalf("got %q", got)
	}
}
