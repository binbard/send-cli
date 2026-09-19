package sshchannel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const DefaultDestination = "~/"

type Request struct {
	Target      string
	Destination string
	Files       []string
	Port        int
	Identity    string
	Options     []string
}

type Result struct {
	Sent int
}

type Runner func(context.Context, string, ...string) ([]byte, error)

type Client struct {
	Executable string
	Run        Runner
}

func NewClient(executable string) *Client {
	if strings.TrimSpace(executable) == "" {
		executable = "scp"
	}
	return &Client{Executable: executable, Run: runCommand}
}

func (c *Client) Send(ctx context.Context, request Request) (Result, error) {
	target := strings.TrimSpace(request.Target)
	if err := validateRemoteValue("SSH target", target); err != nil {
		return Result{}, err
	}
	destination := strings.TrimSpace(request.Destination)
	if destination == "" {
		destination = DefaultDestination
	}
	if err := validateRemoteValue("SSH destination", destination); err != nil {
		return Result{}, err
	}
	if request.Port < 0 || request.Port > 65535 {
		return Result{}, errors.New("SSH port must be 0 (default) or between 1 and 65535")
	}
	files, err := validateFiles(request.Files)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, errors.New("provide at least one file to send over SSH")
	}

	baseArgs := []string{"-o", "BatchMode=yes"}
	if request.Port > 0 {
		baseArgs = append(baseArgs, "-P", strconv.Itoa(request.Port))
	}
	if strings.TrimSpace(request.Identity) != "" {
		identity, err := filepath.Abs(request.Identity)
		if err != nil {
			return Result{}, fmt.Errorf("resolve SSH identity: %w", err)
		}
		if info, err := os.Stat(identity); err != nil {
			return Result{}, fmt.Errorf("SSH identity %q: %w", request.Identity, err)
		} else if !info.Mode().IsRegular() {
			return Result{}, fmt.Errorf("SSH identity %q is not a regular file", request.Identity)
		}
		baseArgs = append(baseArgs, "-i", identity)
	}
	for _, option := range request.Options {
		option = strings.TrimSpace(option)
		if option == "" || strings.ContainsAny(option, "\r\n") {
			return Result{}, fmt.Errorf("invalid SSH option %q", option)
		}
		baseArgs = append(baseArgs, "-o", option)
	}

	result := Result{}
	remote := target + ":" + destination
	for _, file := range files {
		args := append(append([]string(nil), baseArgs...), "--", file, remote)
		output, err := c.runner()(ctx, c.Executable, args...)
		if err != nil {
			return result, fmt.Errorf(
				"sent %d of %d files; %s failed; %d not attempted: %w",
				result.Sent, len(files), filepath.Base(file), len(files)-result.Sent-1,
				commandError(output, err),
			)
		}
		result.Sent++
	}
	return result, nil
}

func validateRemoteValue(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s cannot begin with '-'", label)
	}
	if strings.ContainsAny(value, "'\"`;$&|<>!(){}[]\\\r\n") {
		return fmt.Errorf("%s contains unsupported shell characters", label)
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%s cannot contain whitespace", label)
	}
	return nil
}

func validateFiles(paths []string) ([]string, error) {
	files := make([]string, len(paths))
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
		files[index] = absolute
	}
	return files, nil
}

func (c *Client) runner() Runner {
	if c.Run != nil {
		return c.Run
	}
	return runCommand
}

func runCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).CombinedOutput()
}

func commandError(output []byte, err error) error {
	detail := strings.TrimSpace(safeText(string(output)))
	if detail != "" {
		return fmt.Errorf("scp: %s: %w", detail, err)
	}
	return fmt.Errorf("scp: %w", err)
}

func safeText(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || char >= ' ' && char != '\x7f' {
			return char
		}
		return -1
	}, value)
}
