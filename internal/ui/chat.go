package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	tgservice "send-cli/internal/telegram"
)

type Options struct {
	Receiver        string
	AutoDownload    bool
	SetAutoDownload func(bool) error
}

func Run(ctx context.Context, session tgservice.Session, options Options) error {
	input := textarea.New()
	input.Placeholder = "Type a message..."
	input.Prompt = "U> "
	input.SetHeight(4)
	input.SetWidth(78)
	input.CharLimit = 4096
	input.Focus()

	model := chatModel{
		ctx:          ctx,
		session:      session,
		options:      options,
		input:        input,
		autoDownload: options.AutoDownload,
	}
	program := tea.NewProgram(model, tea.WithContext(ctx))
	final, err := program.Run()
	if result, ok := final.(chatModel); ok {
		result.removeTemporaryAttachments()
	}
	return err
}

type attachment struct {
	selectedFile
}

type chatModel struct {
	ctx           context.Context
	session       tgservice.Session
	options       Options
	input         textarea.Model
	history       []string
	attachments   []attachment
	status        string
	sending       bool
	autoDownload  bool
	pending       *tgservice.RemoteFile
	downloadQueue []*tgservice.RemoteFile
	downloading   bool
	collision     bool
	renaming      bool
	rename        string
	width         int
	height        int
}

type eventMsg struct{ event tgservice.Event }
type sendResultMsg struct{ err error }
type pickResultMsg struct {
	files []selectedFile
	err   error
}
type downloadResultMsg struct {
	path string
	err  error
}
type autoDownloadMsg struct{ err error }

func (m chatModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, waitForEvent(m.session.Events()))
}

func waitForEvent(events <-chan tgservice.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return eventMsg{event: tgservice.Event{Err: fmt.Errorf("Telegram connection closed")}}
		}
		return eventMsg{event: event}
	}
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		width := msg.Width - 4
		if width < 20 {
			width = 20
		}
		m.input.SetWidth(width)
	case eventMsg:
		event := msg.event
		var eventCmd tea.Cmd
		if event.Err != nil {
			m.status = "Connection error: " + event.Err.Error()
		} else {
			line := "R> "
			if event.Sender != "" {
				line += "[" + event.Sender + "] "
			}
			line += event.Text
			if event.File != nil {
				if line != "R> " {
					line += " "
				}
				line += fmt.Sprintf("[%s, %s]", filepath.Base(event.File.Name), formatBytes(event.File.Size))
				if m.pending == nil {
					eventCmd = m.offerDownload(event.File)
				} else {
					m.downloadQueue = append(m.downloadQueue, event.File)
					m.status = fmt.Sprintf("%s queued for download (%d waiting)", filepath.Base(event.File.Name), len(m.downloadQueue))
				}
			}
			m.addHistory(line)
		}
		return m, tea.Batch(eventCmd, waitForEvent(m.session.Events()))
	case sendResultMsg:
		m.sending = false
		if msg.err != nil {
			m.status = "Send failed: " + msg.err.Error()
			return m, nil
		}
		for _, file := range m.attachments {
			if file.Temporary {
				_ = os.Remove(file.Path)
			}
		}
		m.input.Reset()
		m.attachments = nil
		m.status = "Sent"
		return m, nil
	case pickResultMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		for _, file := range msg.files {
			if info, err := os.Stat(file.Path); err == nil && info.Mode().IsRegular() {
				m.attachments = append(m.attachments, attachment{selectedFile: file})
			}
		}
		if len(msg.files) == 0 {
			m.status = "No file selected"
		} else {
			m.status = fmt.Sprintf("Attached %d file(s)", len(msg.files))
		}
		return m, nil
	case downloadResultMsg:
		m.downloading = false
		if msg.err != nil {
			m.status = "Download failed: " + msg.err.Error()
		} else {
			m.status = "Saved " + msg.path
		}
		return m, m.advanceDownload()
	case autoDownloadMsg:
		if msg.err != nil {
			m.autoDownload = false
			m.status = "Could not save always-download preference: " + msg.err.Error()
		}
		return m, nil
	case tea.KeyMsg:
		if m.pending != nil {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			if m.downloading {
				return m, nil
			}
			return m.updateDownloadPrompt(msg)
		}
		key := msg.String()
		if m.sending {
			if key == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+o":
			return m, func() tea.Msg {
				files, err := pickFiles()
				return pickResultMsg{files: files, err: err}
			}
		case "ctrl+v":
			return m, func() tea.Msg {
				files, err := clipboardFiles()
				if err == nil && len(files) == 0 {
					err = fmt.Errorf("clipboard does not contain files or an image")
				}
				return pickResultMsg{files: files, err: err}
			}
		case "ctrl+j", "ctrl+\\", "alt+enter", "shift+enter":
			m.input.InsertString("\n")
			return m, nil
		case "enter":
			if m.sending {
				return m, nil
			}
			text := m.input.Value()
			if strings.TrimSpace(text) == "" && len(m.attachments) == 0 {
				return m, nil
			}
			files := make([]string, len(m.attachments))
			labels := make([]string, len(m.attachments))
			for i, file := range m.attachments {
				files[i] = file.Path
				labels[i] = "[" + filepath.Base(file.Path) + "]"
			}
			line := "U> " + text
			if len(labels) > 0 {
				line += " " + strings.Join(labels, " ")
			}
			m.addHistory(strings.TrimSpace(line))
			m.sending = true
			m.status = "Sending..."
			return m, func() tea.Msg {
				return sendResultMsg{err: m.session.Send(m.ctx, text, files)}
			}
		}

		if msg.Paste {
			if files, ok := pastedFiles(string(msg.Runes)); ok {
				for _, file := range files {
					m.attachments = append(m.attachments, attachment{selectedFile: selectedFile{Path: file}})
				}
				m.status = fmt.Sprintf("Attached %d pasted file(s)", len(files))
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m chatModel) updateDownloadPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.renaming {
		switch key {
		case "esc":
			m.status = "Download skipped"
			return m, m.advanceDownload()
		case "backspace":
			runes := []rune(m.rename)
			if len(runes) > 0 {
				m.rename = string(runes[:len(runes)-1])
			}
			m.status = "Save as: " + m.rename
		case "enter":
			name := filepath.Base(strings.TrimSpace(m.rename))
			if name == "" || name == "." {
				m.status = "Enter a valid filename"
				return m, nil
			}
			path := m.downloadPath(name)
			if pathExists(path) {
				m.status = name + " also exists; enter another filename"
				return m, nil
			}
			m.status = "Downloading " + name + "..."
			return m, m.download(false, path)
		default:
			if msg.Type == tea.KeyRunes {
				m.rename += string(msg.Runes)
				m.status = "Save as: " + m.rename
			}
		}
		return m, nil
	}

	if m.collision {
		switch strings.ToLower(key) {
		case "y":
			path := m.downloadPath(m.pending.Name)
			m.status = "Downloading " + filepath.Base(path) + "..."
			return m, m.download(true, path)
		case "r":
			m.renaming = true
			m.rename = ""
			m.status = "Save as: "
		case "n", "enter", "esc":
			m.status = "Download skipped"
			return m, m.advanceDownload()
		}
		return m, nil
	}

	switch key {
	case "y", "Y":
		path := m.downloadPath(m.pending.Name)
		if pathExists(path) {
			m.collision = true
			m.status = filepath.Base(path) + " already exists: [y] overwrite, [r] rename, [N] skip"
			return m, nil
		}
		m.status = "Downloading " + filepath.Base(path) + "..."
		return m, m.download(false, path)
	case "A":
		m.autoDownload = true
		path := m.downloadPath(m.pending.Name)
		var commands []tea.Cmd
		if m.options.SetAutoDownload != nil {
			commands = append(commands, func() tea.Msg {
				return autoDownloadMsg{err: m.options.SetAutoDownload(true)}
			})
		}
		if pathExists(path) {
			m.collision = true
			m.status = filepath.Base(path) + " already exists: [y] overwrite, [r] rename, [N] skip"
		} else {
			m.status = "Downloading " + filepath.Base(path) + "..."
			commands = append(commands, m.download(false, path))
		}
		return m, tea.Batch(commands...)
	case "n", "N", "enter", "esc":
		m.status = "Download skipped"
		return m, m.advanceDownload()
	}
	return m, nil
}

func (m *chatModel) offerDownload(file *tgservice.RemoteFile) tea.Cmd {
	m.pending = file
	m.downloading = false
	m.collision = false
	m.renaming = false
	m.rename = ""
	path := m.downloadPath(file.Name)
	if m.autoDownload {
		if pathExists(path) {
			m.collision = true
			m.status = fmt.Sprintf("%s already exists: [y] overwrite, [r] rename, [N] skip", filepath.Base(file.Name))
			return nil
		}
		m.status = "Downloading " + filepath.Base(file.Name) + "..."
		return m.download(false, path)
	}
	m.status = fmt.Sprintf("Download to %s? [y/N/A always]", path)
	return nil
}

func (m *chatModel) advanceDownload() tea.Cmd {
	m.pending = nil
	m.downloading = false
	m.collision = false
	m.renaming = false
	m.rename = ""
	if len(m.downloadQueue) == 0 {
		return nil
	}
	next := m.downloadQueue[0]
	m.downloadQueue = m.downloadQueue[1:]
	return m.offerDownload(next)
}

func (m chatModel) removeTemporaryAttachments() {
	for _, file := range m.attachments {
		if file.Temporary {
			_ = os.Remove(file.Path)
		}
	}
}

func (m *chatModel) download(overwrite bool, destination string) tea.Cmd {
	file := m.pending
	m.downloading = true
	return func() tea.Msg {
		temp := destination + ".send-part"
		_ = os.Remove(temp)
		if err := file.Download(m.ctx, temp); err != nil {
			_ = os.Remove(temp)
			return downloadResultMsg{err: err}
		}
		if overwrite {
			if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
				_ = os.Remove(temp)
				return downloadResultMsg{err: err}
			}
		}
		if err := os.Rename(temp, destination); err != nil {
			_ = os.Remove(temp)
			return downloadResultMsg{err: err}
		}
		return downloadResultMsg{path: destination}
	}
}

func (m *chatModel) addHistory(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	m.history = append(m.history, line)
	if len(m.history) > 100 {
		m.history = m.history[len(m.history)-100:]
	}
}

func (m chatModel) downloadPath(name string) string {
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

func (m chatModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Render("Telegram · " + m.options.Receiver + " · connected")
	var body strings.Builder
	body.WriteString(title)
	body.WriteString("\n\n")
	start := 0
	maxLines := m.height - 11
	if maxLines < 3 {
		maxLines = 3
	}
	if len(m.history) > maxLines {
		start = len(m.history) - maxLines
	}
	for _, line := range m.history[start:] {
		body.WriteString(line)
		body.WriteByte('\n')
	}
	body.WriteByte('\n')
	body.WriteString(m.input.View())
	if len(m.attachments) > 0 {
		body.WriteString("\nAttachments: ")
		for _, file := range m.attachments {
			body.WriteString("[" + filepath.Base(file.Path) + "] ")
		}
	}
	if m.status != "" {
		body.WriteString("\n" + m.status)
	}
	body.WriteString("\n\nEnter send · Shift+Enter/Ctrl+J/Ctrl+\\/Alt+Enter newline · Ctrl+O attach · Ctrl+C exit")
	return body.String()
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
