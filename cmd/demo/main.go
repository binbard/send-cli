package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const fixturePath = "docs/demo-output.json"

type transcripts struct {
	List     string `json:"list"`
	Telegram string `json:"telegram"`
	WhatsApp string `json:"whatsapp"`
}

var (
	usernamePattern = regexp.MustCompile(`@[A-Za-z0-9_]+`)
	phonePattern    = regexp.MustCompile(`\+?[0-9]{6,15}`)
)

func main() {
	var err error
	if len(os.Args) >= 2 && os.Args[1] == "render" {
		if len(os.Args) > 3 || (len(os.Args) == 3 && os.Args[2] != "--live") {
			err = errors.New("usage: go run ./cmd/demo render [--live]")
		} else {
			err = render(len(os.Args) == 3)
		}
	} else {
		err = show(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func render(live bool) error {
	if _, err := exec.LookPath("vhs"); err != nil {
		return errors.New("VHS is required to render the demo")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("ffmpeg is required to render the demo")
	}
	if !live {
		fixture, err := readTranscripts()
		if err != nil {
			return err
		}
		if fixture.List == "" || fixture.Telegram == "" || fixture.WhatsApp == "" {
			return errors.New("demo transcripts are incomplete; run just demo-live to capture them")
		}
	}
	helpDir := filepath.Join("build", "vhs-bin")
	if err := os.MkdirAll(helpDir, 0o755); err != nil {
		return err
	}
	helpBinary := filepath.Join(helpDir, binaryName())
	if err := runCommand(nil, "go", "build", "-o", helpBinary, "./cmd/demo"); err != nil {
		return fmt.Errorf("build demo helper: %w", err)
	}
	tape := "docs/demo-unix.tape"
	if runtime.GOOS == "windows" {
		tape = "docs/demo.tape"
	}
	liveValue := "0"
	if live {
		liveValue = "1"
	}
	defer os.Remove("docs/demo.mp4") // VHS may leave an intermediate MP4.
	if err := runCommand([]string{"SEND_DEMO_LIVE=" + liveValue}, "vhs", tape); err != nil {
		return fmt.Errorf("render demo: %w", err)
	}
	if err := runCommand(nil, "ffmpeg", "-y", "-ss", "8", "-i", "docs/demo.gif", "-frames:v", "1", "docs/screenshot.png", "-loglevel", "error"); err != nil {
		return fmt.Errorf("extract website preview screenshot: %w", err)
	}
	return nil
}

func runCommand(extraEnv []string, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Env = append(os.Environ(), extraEnv...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "send.exe"
	}
	return "send"
}

func show(args []string) error {
	key, err := transcriptKey(args)
	if err != nil {
		return err
	}
	fixture, err := readTranscripts()
	if err != nil {
		return err
	}
	var output string
	if os.Getenv("SEND_DEMO_LIVE") == "1" {
		command := exec.Command(filepath.Join("bin", binaryName()), args...)
		raw, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("live %s command failed: %w (output withheld)", key, err)
		}
		output = redact(key, string(raw))
		if strings.TrimSpace(output) == "" {
			return fmt.Errorf("live %s command produced no output", key)
		}
		fixture.set(key, output)
		if err := writeTranscripts(fixture); err != nil {
			return fmt.Errorf("save redacted %s output: %w", key, err)
		}
	} else {
		output = fixture.get(key)
		if output == "" {
			return fmt.Errorf("missing %s transcript; run just demo-live", key)
		}
	}
	_, err = fmt.Fprint(os.Stdout, output)
	return err
}

func transcriptKey(args []string) (string, error) {
	switch {
	case len(args) == 1 && args[0] == "list":
		return "list", nil
	case len(args) == 2 && args[0] == "tg" && args[1] == "README.md":
		return "telegram", nil
	case len(args) == 3 && args[0] == "wa" && args[1] == "-m" && args[2] == "Demo from send-cli: WhatsApp. No reply needed.":
		return "whatsapp", nil
	default:
		return "", errors.New("demo helper supports only the recorded list, Telegram, and WhatsApp commands")
	}
}

func (t transcripts) get(key string) string {
	switch key {
	case "list":
		return t.List
	case "telegram":
		return t.Telegram
	case "whatsapp":
		return t.WhatsApp
	default:
		return ""
	}
}

func (t *transcripts) set(key, value string) {
	switch key {
	case "list":
		t.List = value
	case "telegram":
		t.Telegram = value
	case "whatsapp":
		t.WhatsApp = value
	}
}

func readTranscripts() (transcripts, error) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return transcripts{}, fmt.Errorf("read demo transcripts: %w", err)
	}
	var fixture transcripts
	if err := json.Unmarshal(data, &fixture); err != nil {
		return transcripts{}, fmt.Errorf("parse demo transcripts: %w", err)
	}
	return fixture, nil
}

func writeTranscripts(fixture transcripts) error {
	return writeTranscriptsTo(fixturePath, fixture)
}

func writeTranscriptsTo(path string, fixture transcripts) error {
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".demo-output-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func redact(key, raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.SplitAfter(raw, "\n")
	for i, line := range lines {
		if key == "list" {
			fields := strings.Fields(line)
			if len(fields) == 0 || (fields[0] != "telegram" && fields[0] != "whatsapp") {
				continue
			}
		}
		line = usernamePattern.ReplaceAllString(line, "@***")
		lines[i] = maskPhoneNumbers(line)
	}
	return strings.Join(lines, "")
}

func maskPhoneNumbers(line string) string {
	matches := phonePattern.FindAllStringIndex(line, -1)
	var result strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		if (start > 0 && isASCIIWord(line[start-1])) || (end < len(line) && isASCIIWord(line[end])) {
			continue
		}
		result.WriteString(line[last:start])
		result.WriteString("***")
		last = end
	}
	result.WriteString(line[last:])
	return result.String()
}

func isASCIIWord(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}
