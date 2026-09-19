package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"

	tgservice "send-cli/internal/telegram"
)

const maxDraftRunes = 4096

type Options struct {
	Channel         string
	Receiver        string
	QRCode          string
	AutoDownload    bool
	SetAutoDownload func(bool) error
}

func Run(ctx context.Context, session tgservice.Session, options Options) error {
	channel := strings.TrimSpace(options.Channel)
	if channel == "" {
		channel = "Telegram"
	}
	restoreOutput, err := prepareTerminalOutput(os.Stdout)
	if err != nil {
		return fmt.Errorf("prepare terminal output: %w", err)
	}
	defer restoreOutput()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enable terminal input: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	keys := make(chan inputEvent, 32)
	results := make(chan any, 16)
	console := &chatConsole{
		ctx:          runCtx,
		session:      session,
		options:      options,
		autoDownload: options.AutoDownload,
		results:      results,
		renderer:     newRenderer(os.Stdout),
	}
	if value := strings.TrimSpace(options.QRCode); value != "" {
		console.qrCode = terminalQRCode(value)
	}

	fmt.Fprint(os.Stdout, "\x1b[?2004h")
	fmt.Fprintf(os.Stdout, "%s · %s · connected\r\n", safeTerminalText(channel), safeTerminalText(options.Receiver))
	fmt.Fprint(os.Stdout, "Enter send · Shift+Enter/Ctrl+J/Ctrl+\\/Alt+Enter newline · Ctrl+O attach")
	if console.qrCode != "" {
		fmt.Fprint(os.Stdout, " · r QR")
	}
	fmt.Fprint(os.Stdout, " · Ctrl+C exit\r\n\r\n")
	console.renderer.draw(console)
	go readInput(runCtx, os.Stdin, keys)
	events := session.Events()

	defer func() {
		cancel()
		console.renderer.clear()
		fmt.Fprint(os.Stdout, "\r\n\x1b[?2004l")
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		console.removeTemporaryAttachments()
	}()

	for {
		select {
		case <-runCtx.Done():
			return nil
		case key := <-keys:
			if key.kind == inputQuit {
				return nil
			}
			if key.kind == inputError {
				if errors.Is(key.err, io.EOF) {
					return nil
				}
				return fmt.Errorf("read terminal input: %w", key.err)
			}
			console.handleInput(key)
		case event, ok := <-events:
			if !ok {
				console.status = channel + " connection closed"
				events = nil
			} else {
				console.handleTelegramEvent(event)
			}
		case result := <-results:
			console.handleResult(result)
		}
		console.renderer.draw(console)
	}
}

type chatConsole struct {
	ctx           context.Context
	session       tgservice.Session
	options       Options
	renderer      *renderer
	results       chan any
	draft         []rune
	cursor        int
	attachments   []selectedFile
	status        string
	sending       bool
	picking       bool
	autoDownload  bool
	pending       *tgservice.RemoteFile
	downloadQueue []*tgservice.RemoteFile
	downloading   bool
	collision     bool
	renaming      bool
	rename        []rune
	qrCode        string
	showQR        bool
}

type sendResult struct{ err error }
type pickResult struct {
	files []selectedFile
	err   error
}
type downloadResult struct {
	path string
	err  error
}
type autoDownloadResult struct{ err error }
type transferProgressResult struct{ progress tgservice.TransferProgress }

func (c *chatConsole) handleInput(key inputEvent) {
	if c.showQR {
		if key.kind == inputText && strings.EqualFold(key.text, "r") {
			c.showQR = false
		}
		return
	}
	if c.pending != nil {
		c.handleDownloadInput(key)
		return
	}
	if c.sending {
		return
	}
	if c.qrCode != "" && key.kind == inputText && strings.EqualFold(key.text, "r") && len(c.draft) == 0 && len(c.attachments) == 0 {
		c.status = ""
		c.showQR = true
		return
	}
	switch key.kind {
	case inputText, inputPaste, inputBackspace, inputDelete, inputLeft, inputRight, inputHome, inputEnd, inputNewline:
		c.status = ""
	}

	switch key.kind {
	case inputText:
		c.appendDraft(key.text)
	case inputPaste:
		if files, ok := pastedFiles(key.text); ok {
			for _, path := range files {
				c.attachments = append(c.attachments, selectedFile{Path: path})
			}
			c.status = fmt.Sprintf("Attached %d pasted file(s)", len(files))
		} else {
			c.appendDraft(key.text)
		}
	case inputBackspace:
		if c.cursor > 0 {
			c.draft = append(c.draft[:c.cursor-1], c.draft[c.cursor:]...)
			c.cursor--
		} else if len(c.draft) == 0 && len(c.attachments) > 0 {
			removed := c.attachments[len(c.attachments)-1]
			c.attachments = c.attachments[:len(c.attachments)-1]
			if removed.Temporary {
				_ = os.Remove(removed.Path)
			}
			c.status = "Removed " + filepath.Base(removed.Path)
		}
	case inputDelete:
		if c.cursor < len(c.draft) {
			c.draft = append(c.draft[:c.cursor], c.draft[c.cursor+1:]...)
		}
	case inputLeft:
		if c.cursor > 0 {
			c.cursor--
		}
	case inputRight:
		if c.cursor < len(c.draft) {
			c.cursor++
		}
	case inputHome:
		c.cursor = 0
	case inputEnd:
		c.cursor = len(c.draft)
	case inputClear:
		c.removeTemporaryAttachments()
		c.draft = nil
		c.cursor = 0
		c.attachments = nil
		c.status = "Draft cleared"
	case inputNewline:
		c.appendDraft("\n")
	case inputEnter:
		c.startSend()
	case inputPick:
		if c.picking {
			return
		}
		c.picking = true
		c.status = "Opening file picker..."
		go func() {
			files, err := pickFiles()
			c.deliver(pickResult{files: files, err: err})
		}()
	case inputClipboard:
		if c.picking {
			return
		}
		c.picking = true
		c.status = "Reading clipboard..."
		go func() {
			files, err := clipboardFiles()
			if err == nil && len(files) == 0 {
				err = errors.New("clipboard does not contain files or an image")
			}
			c.deliver(pickResult{files: files, err: err})
		}()
	}
}

func (c *chatConsole) appendDraft(value string) {
	value = safeTerminalText(value)
	available := maxDraftRunes - len(c.draft)
	if available <= 0 {
		c.status = "Message limit reached"
		return
	}
	runes := []rune(value)
	if len(runes) > available {
		runes = runes[:available]
		c.status = "Message truncated to 4096 characters"
	}
	originalLength := len(c.draft)
	c.draft = append(c.draft, make([]rune, len(runes))...)
	copy(c.draft[c.cursor+len(runes):], c.draft[c.cursor:originalLength])
	copy(c.draft[c.cursor:], runes)
	c.cursor += len(runes)
}

func (c *chatConsole) startSend() {
	text := string(c.draft)
	if strings.TrimSpace(text) == "" && len(c.attachments) == 0 {
		return
	}

	files := make([]string, len(c.attachments))
	for i, file := range c.attachments {
		files[i] = file.Path
	}
	c.sending = true
	c.status = "Sending..."
	go func() {
		progressCtx := tgservice.WithTransferProgress(c.ctx, func(progress tgservice.TransferProgress) {
			c.deliver(transferProgressResult{progress: progress})
		})
		c.deliver(sendResult{err: c.session.Send(progressCtx, text, files)})
	}()
}

func (c *chatConsole) handleTelegramEvent(event tgservice.Event) {
	if event.Err != nil {
		c.status = "Connection error: " + event.Err.Error()
		return
	}
	line := incomingLine(event)
	if event.File != nil {
		if c.pending == nil {
			c.offerDownload(event.File)
		} else {
			c.downloadQueue = append(c.downloadQueue, event.File)
		}
	}
	c.renderer.printLine(line)
}

func incomingLine(event tgservice.Event) string {
	line := "R>"
	if event.Sender != "" {
		line += " [" + event.Sender + "]"
	}
	if event.Text != "" {
		line += " " + event.Text
	}
	if event.File != nil {
		line += fmt.Sprintf(" [%s, %s]", filepath.Base(event.File.Name), formatBytes(event.File.Size))
	}
	return line
}

func (c *chatConsole) handleResult(result any) {
	switch result := result.(type) {
	case sendResult:
		c.sending = false
		if result.err != nil {
			c.status = "Send failed: " + result.err.Error()
			return
		}
		c.renderer.printLine(c.outgoingLine())
		c.removeTemporaryAttachments()
		c.draft = nil
		c.cursor = 0
		c.attachments = nil
		c.status = ""
	case pickResult:
		c.picking = false
		if result.err != nil {
			c.status = result.err.Error()
			return
		}
		added := 0
		for _, file := range result.files {
			if info, err := os.Stat(file.Path); err == nil && info.Mode().IsRegular() {
				c.attachments = append(c.attachments, file)
				added++
			}
		}
		if added == 0 {
			c.status = "No file selected"
		} else {
			c.status = fmt.Sprintf("Attached %d file(s)", added)
		}
	case downloadResult:
		c.downloading = false
		if result.err != nil {
			c.status = "Download failed: " + result.err.Error()
		} else {
			c.status = "Saved " + result.path
		}
		c.advanceDownload()
	case autoDownloadResult:
		if result.err != nil {
			c.autoDownload = false
			c.status = "Could not save always-download preference: " + result.err.Error()
		}
	case transferProgressResult:
		c.status = formatTransferProgress(result.progress)
	}
}

func (c *chatConsole) outgoingLine() string {
	line := "U>"
	if text := strings.TrimSpace(string(c.draft)); text != "" {
		line += " " + text
	}
	if len(c.attachments) > 0 {
		labels := make([]string, len(c.attachments))
		for index, file := range c.attachments {
			labels[index] = "[" + filepath.Base(file.Path) + "]"
		}
		line += " " + strings.Join(labels, " ")
	}
	return line
}

func (c *chatConsole) handleDownloadInput(key inputEvent) {
	if c.downloading {
		return
	}
	if c.renaming {
		switch key.kind {
		case inputEscape:
			c.status = "Download skipped"
			c.advanceDownload()
		case inputBackspace:
			if len(c.rename) > 0 {
				c.rename = c.rename[:len(c.rename)-1]
			}
			c.status = "Save as: " + string(c.rename)
		case inputEnter:
			name := filepath.Base(strings.TrimSpace(string(c.rename)))
			if name == "" || name == "." {
				c.status = "Enter a valid filename"
				return
			}
			path := c.downloadPath(name)
			if pathExists(path) {
				c.status = name + " also exists; enter another filename"
				return
			}
			c.status = "Downloading " + name + "..."
			c.startDownload(false, path)
		case inputText, inputPaste:
			c.rename = append(c.rename, []rune(safeTerminalText(key.text))...)
			c.status = "Save as: " + string(c.rename)
		}
		return
	}

	if c.collision {
		if key.kind == inputEnter || key.kind == inputEscape {
			c.status = "Download skipped"
			c.advanceDownload()
			return
		}
		if key.kind != inputText || len(key.text) != 1 {
			return
		}
		switch strings.ToLower(key.text) {
		case "y":
			path := c.downloadPath(c.pending.Name)
			c.status = "Downloading " + filepath.Base(path) + "..."
			c.startDownload(true, path)
		case "r":
			c.renaming = true
			c.rename = nil
			c.status = "Save as: "
		case "n":
			c.status = "Download skipped"
			c.advanceDownload()
		}
		return
	}

	if key.kind == inputEnter || key.kind == inputEscape {
		c.status = "Download skipped"
		c.advanceDownload()
		return
	}
	if key.kind != inputText || len(key.text) != 1 {
		return
	}
	switch key.text {
	case "y", "Y":
		path := c.downloadPath(c.pending.Name)
		if pathExists(path) {
			c.collision = true
			c.status = filepath.Base(path) + " already exists: [y] overwrite, [r] rename, [N] skip"
			return
		}
		c.status = "Downloading " + filepath.Base(path) + "..."
		c.startDownload(false, path)
	case "A":
		c.autoDownload = true
		if c.options.SetAutoDownload != nil {
			go func() {
				c.deliver(autoDownloadResult{err: c.options.SetAutoDownload(true)})
			}()
		}
		path := c.downloadPath(c.pending.Name)
		if pathExists(path) {
			c.collision = true
			c.status = filepath.Base(path) + " already exists: [y] overwrite, [r] rename, [N] skip"
		} else {
			c.status = "Downloading " + filepath.Base(path) + "..."
			c.startDownload(false, path)
		}
	case "n", "N":
		c.status = "Download skipped"
		c.advanceDownload()
	}
}

func (c *chatConsole) offerDownload(file *tgservice.RemoteFile) {
	c.pending = file
	c.downloading = false
	c.collision = false
	c.renaming = false
	c.rename = nil
	path := c.downloadPath(file.Name)
	if c.autoDownload {
		if pathExists(path) {
			c.collision = true
			c.status = fmt.Sprintf("%s already exists: [y] overwrite, [r] rename, [N] skip", filepath.Base(file.Name))
			return
		}
		c.status = "Downloading " + filepath.Base(file.Name) + "..."
		c.startDownload(false, path)
		return
	}
	c.status = fmt.Sprintf("Download to %s? [y/N/A always]", path)
}

func (c *chatConsole) advanceDownload() {
	c.pending = nil
	c.downloading = false
	c.collision = false
	c.renaming = false
	c.rename = nil
	if len(c.downloadQueue) == 0 {
		return
	}
	next := c.downloadQueue[0]
	c.downloadQueue = c.downloadQueue[1:]
	c.offerDownload(next)
}

func (c *chatConsole) startDownload(overwrite bool, destination string) {
	file := c.pending
	c.downloading = true
	go func() {
		temp := destination + ".send-part"
		_ = os.Remove(temp)
		progressCtx := tgservice.WithTransferProgress(c.ctx, func(progress tgservice.TransferProgress) {
			progress.Name = filepath.Base(destination)
			c.deliver(transferProgressResult{progress: progress})
		})
		if err := file.Download(progressCtx, temp); err != nil {
			_ = os.Remove(temp)
			c.deliver(downloadResult{err: err})
			return
		}
		if overwrite {
			if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
				_ = os.Remove(temp)
				c.deliver(downloadResult{err: err})
				return
			}
		}
		if err := os.Rename(temp, destination); err != nil {
			_ = os.Remove(temp)
			c.deliver(downloadResult{err: err})
			return
		}
		c.deliver(downloadResult{path: destination})
	}()
}

func (c *chatConsole) deliver(result any) {
	select {
	case c.results <- result:
	case <-c.ctx.Done():
	}
}

func (c *chatConsole) removeTemporaryAttachments() {
	for _, file := range c.attachments {
		if file.Temporary {
			_ = os.Remove(file.Path)
		}
	}
}

func (c *chatConsole) downloadPath(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." {
		base = "telegram-download"
	}
	cwd, err := os.Getwd()
	if err != nil {
		return base
	}
	return filepath.Join(cwd, base)
}

type renderer struct {
	output           io.Writer
	width            int
	lines            int
	cursorFromBottom int
}

func newRenderer(output *os.File) *renderer {
	width, _, err := term.GetSize(int(output.Fd()))
	if err != nil || width < 20 {
		width = 80
	}
	return &renderer{output: output, width: width}
}

func (r *renderer) clear() {
	if r.lines == 0 {
		return
	}
	if r.cursorFromBottom > 0 {
		fmt.Fprintf(r.output, "\x1b[%dB", r.cursorFromBottom)
		r.cursorFromBottom = 0
	}
	fmt.Fprint(r.output, "\r")
	for line := 0; line < r.lines; line++ {
		fmt.Fprint(r.output, "\x1b[2K")
		if line+1 < r.lines {
			fmt.Fprint(r.output, "\x1b[1A\r")
		}
	}
	r.lines = 0
}

func (r *renderer) draw(c *chatConsole) {
	r.clear()
	if c.showQR {
		text := c.qrCode + "\nr hide QR"
		fmt.Fprint(r.output, strings.ReplaceAll(text, "\n", "\r\n"))
		r.lines = visualLines(text, r.width)
		return
	}
	var block strings.Builder
	if c.status != "" {
		block.WriteString(c.status)
		if c.pending == nil {
			block.WriteString("\n")
		}
	}
	if c.pending != nil {
		text := safeTerminalText(block.String())
		fmt.Fprint(r.output, strings.ReplaceAll(text, "\n", "\r\n"))
		r.lines = visualLines(text, r.width)
		return
	}
	if len(c.attachments) > 0 {
		block.WriteString("Attachments: ")
		for _, file := range c.attachments {
			block.WriteString("[")
			block.WriteString(filepath.Base(file.Path))
			block.WriteString("] ")
		}
		block.WriteString("\n")
	}
	block.WriteString("U> ")
	block.WriteString(strings.ReplaceAll(string(c.draft), "\n", "\n   "))
	text := safeTerminalText(block.String())
	fmt.Fprint(r.output, strings.ReplaceAll(text, "\n", "\r\n"))
	r.lines = visualLines(text, r.width)
	if c.cursor < len(c.draft) {
		promptStart := strings.TrimSuffix(text, strings.ReplaceAll(string(c.draft), "\n", "\n   "))
		beforeCursor := promptStart + strings.ReplaceAll(string(c.draft[:c.cursor]), "\n", "\n   ")
		endRow, _ := terminalPosition(text, r.width)
		cursorRow, cursorColumn := terminalPosition(beforeCursor, r.width)
		r.cursorFromBottom = endRow - cursorRow
		if r.cursorFromBottom > 0 {
			fmt.Fprintf(r.output, "\x1b[%dA", r.cursorFromBottom)
		}
		fmt.Fprint(r.output, "\r")
		if cursorColumn > 0 {
			fmt.Fprintf(r.output, "\x1b[%dC", cursorColumn)
		}
	}
}

func terminalQRCode(value string) string {
	var output strings.Builder
	qrterminal.GenerateHalfBlock(value, qrterminal.L, &output)
	return strings.TrimRight(output.String(), "\r\n")
}

func (r *renderer) printLine(line string) {
	r.clear()
	line = safeTerminalText(line)
	fmt.Fprint(r.output, strings.ReplaceAll(line, "\n", "\r\n"))
	fmt.Fprint(r.output, "\r\n")
}

func visualLines(value string, width int) int {
	lines := 0
	for _, line := range strings.Split(value, "\n") {
		length := utf8.RuneCountInString(line)
		lineCount := (length + width - 1) / width
		if lineCount < 1 {
			lineCount = 1
		}
		lines += lineCount
	}
	return lines
}

func terminalPosition(value string, width int) (row, column int) {
	for _, char := range value {
		if char == '\n' {
			row++
			column = 0
			continue
		}
		column++
		if column >= width {
			row++
			column = 0
		}
	}
	return row, column
}

type inputKind uint8

const (
	inputText inputKind = iota
	inputPaste
	inputEnter
	inputNewline
	inputBackspace
	inputPick
	inputClipboard
	inputEscape
	inputLeft
	inputRight
	inputHome
	inputEnd
	inputDelete
	inputClear
	inputQuit
	inputError
)

type inputEvent struct {
	kind inputKind
	text string
	err  error
}

func readInput(ctx context.Context, input io.Reader, output chan<- inputEvent) {
	parser := inputParser{}
	buffer := make([]byte, 256)
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			for _, event := range parser.Feed(buffer[:count]) {
				select {
				case output <- event:
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil {
			select {
			case output <- inputEvent{kind: inputError, err: err}:
			case <-ctx.Done():
			}
			return
		}
	}
}

type inputParser struct {
	pending []byte
	pasting bool
}

var (
	pasteStart       = []byte("\x1b[200~")
	pasteEnd         = []byte("\x1b[201~")
	newlineSequences = [][]byte{
		[]byte("\x1b\r"),
		[]byte("\x1b\n"),
		[]byte("\x1b[13;2u"),
		[]byte("\x1b[13;2~"),
		[]byte("\x1b[27;2;13~"),
	}
)

func (p *inputParser) Feed(data []byte) []inputEvent {
	p.pending = append(p.pending, data...)
	var events []inputEvent
	for len(p.pending) > 0 {
		if p.pasting {
			end := bytes.Index(p.pending, pasteEnd)
			if end < 0 {
				return events
			}
			events = append(events, inputEvent{kind: inputPaste, text: string(p.pending[:end])})
			p.pending = p.pending[end+len(pasteEnd):]
			p.pasting = false
			continue
		}
		if bytes.HasPrefix(p.pending, pasteStart) {
			p.pending = p.pending[len(pasteStart):]
			p.pasting = true
			continue
		}
		if bytes.HasPrefix(pasteStart, p.pending) {
			return events
		}

		switch p.pending[0] {
		case 0x03:
			events = append(events, inputEvent{kind: inputQuit})
			p.pending = p.pending[1:]
		case 0x08, 0x7f:
			events = append(events, inputEvent{kind: inputBackspace})
			p.pending = p.pending[1:]
		case 0x0d:
			events = append(events, inputEvent{kind: inputEnter})
			p.pending = p.pending[1:]
		case 0x0a, 0x1c:
			events = append(events, inputEvent{kind: inputNewline})
			p.pending = p.pending[1:]
		case 0x0f:
			events = append(events, inputEvent{kind: inputPick})
			p.pending = p.pending[1:]
		case 0x16:
			events = append(events, inputEvent{kind: inputClipboard})
			p.pending = p.pending[1:]
		case 0x01:
			events = append(events, inputEvent{kind: inputHome})
			p.pending = p.pending[1:]
		case 0x05:
			events = append(events, inputEvent{kind: inputEnd})
			p.pending = p.pending[1:]
		case 0x15:
			events = append(events, inputEvent{kind: inputClear})
			p.pending = p.pending[1:]
		case 0x1b:
			event, consumed, wait := parseEscape(p.pending)
			if wait {
				return events
			}
			p.pending = p.pending[consumed:]
			if event != nil {
				events = append(events, *event)
			}
		default:
			if !utf8.FullRune(p.pending) {
				return events
			}
			r, size := utf8.DecodeRune(p.pending)
			p.pending = p.pending[size:]
			if r == utf8.RuneError && size == 1 {
				continue
			}
			if r == '\t' || r >= ' ' && r != '\x7f' {
				events = append(events, inputEvent{kind: inputText, text: string(r)})
			}
		}
	}
	return events
}

func parseEscape(data []byte) (*inputEvent, int, bool) {
	if len(data) == 1 {
		return nil, 0, true
	}
	for _, sequence := range newlineSequences {
		if bytes.HasPrefix(data, sequence) {
			return &inputEvent{kind: inputNewline}, len(sequence), false
		}
		if bytes.HasPrefix(sequence, data) {
			return nil, 0, true
		}
	}
	if data[1] == '[' {
		for index := 2; index < len(data); index++ {
			if data[index] >= 0x40 && data[index] <= 0x7e {
				sequence := string(data[:index+1])
				kind, ok := map[string]inputKind{
					"\x1b[D":  inputLeft,
					"\x1b[C":  inputRight,
					"\x1b[H":  inputHome,
					"\x1b[F":  inputEnd,
					"\x1b[1~": inputHome,
					"\x1b[4~": inputEnd,
					"\x1b[3~": inputDelete,
				}[sequence]
				if ok {
					return &inputEvent{kind: kind}, index + 1, false
				}
				return nil, index + 1, false
			}
		}
		return nil, 0, true
	}
	if data[1] == 'O' {
		if len(data) < 3 {
			return nil, 0, true
		}
		return nil, 3, false
	}
	return &inputEvent{kind: inputEscape}, 1, false
}

func pastedFiles(value string) ([]string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\x00' })
	if len(parts) == 0 {
		return nil, false
	}
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		path := strings.Trim(strings.TrimSpace(part), `"'`)
		if strings.HasPrefix(path, "file:///") {
			path = strings.TrimPrefix(path, "file:///")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, false
		}
		files = append(files, absolute)
	}
	return files, true
}

func safeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != '\x7f' {
			return r
		}
		return -1
	}, value)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func formatBytes(size int64) string {
	if size <= 0 {
		return "unknown size"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatTransferProgress(progress tgservice.TransferProgress) string {
	action := "Uploading"
	if progress.Direction == tgservice.TransferDownload {
		action = "Downloading"
	}
	status := fmt.Sprintf("%s %s: %s", action, filepath.Base(progress.Name), formatBytes(progress.Transferred))
	if progress.Total > 0 {
		percent := float64(progress.Transferred) * 100 / float64(progress.Total)
		if percent > 100 {
			percent = 100
		}
		status += fmt.Sprintf(" / %s (%.0f%%)", formatBytes(progress.Total), percent)
	}
	if progress.Elapsed > 0 && progress.Transferred > 0 {
		bytesPerSecond := int64(float64(progress.Transferred) / progress.Elapsed.Seconds())
		if bytesPerSecond > 0 {
			status += " at " + formatBytes(bytesPerSecond) + "/s"
		}
	}
	return status
}
