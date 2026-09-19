package adbchannel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const DefaultDestination = "/sdcard/Download/"

type Device struct {
	Serial string
	State  string
	Info   string
}

type Result struct {
	Serial string
	Sent   int
}

type Runner func(context.Context, string, ...string) ([]byte, error)

type Client struct {
	Executable string
	Run        Runner
}

func NewClient(executable string) *Client {
	if strings.TrimSpace(executable) == "" {
		executable = "adb"
	}
	return &Client{Executable: executable, Run: runCommand}
}

func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	output, err := c.runner()(ctx, c.Executable, "devices", "-l")
	if err != nil {
		return nil, commandError("list ADB devices", output, err)
	}
	return parseDevices(string(output)), nil
}

func (c *Client) Send(ctx context.Context, serial, destination string, paths []string) (Result, error) {
	if len(paths) == 0 {
		return Result{}, errors.New("provide at least one file to send with ADB")
	}
	files, err := validateFiles(paths)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(destination) == "" {
		destination = DefaultDestination
	}
	serial, err = c.selectDevice(ctx, strings.TrimSpace(serial))
	if err != nil {
		return Result{}, err
	}

	result := Result{Serial: serial}
	for _, path := range files {
		output, err := c.runner()(ctx, c.Executable, "-s", serial, "push", path, destination)
		if err != nil {
			return result, fmt.Errorf(
				"sent %d of %d files to %s; %s failed; %d not attempted: %w",
				result.Sent, len(files), serial, filepath.Base(path), len(files)-result.Sent-1,
				commandError("adb push", output, err),
			)
		}
		result.Sent++
	}
	return result, nil
}

func (c *Client) selectDevice(ctx context.Context, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	var authorized []string
	for _, device := range devices {
		if device.State == "device" {
			authorized = append(authorized, device.Serial)
		}
	}
	switch len(authorized) {
	case 0:
		if len(devices) > 0 {
			return "", fmt.Errorf("no authorized ADB device found; check the device authorization state with 'send adb --list'")
		}
		return "", errors.New("no ADB device connected")
	case 1:
		return authorized[0], nil
	default:
		return "", fmt.Errorf("multiple ADB devices are connected; choose one with --serial (%s)", strings.Join(authorized, ", "))
	}
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

func commandError(action string, output []byte, err error) error {
	detail := strings.TrimSpace(safeText(string(output)))
	if detail != "" {
		return fmt.Errorf("%s: %s: %w", action, detail, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func parseDevices(output string) []Device {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	devices := make([]Device, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "List" || strings.HasPrefix(fields[0], "*") {
			continue
		}
		devices = append(devices, Device{
			Serial: fields[0],
			State:  fields[1],
			Info:   strings.Join(fields[2:], " "),
		})
	}
	return devices
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

func safeText(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || char >= ' ' && char != '\x7f' {
			return char
		}
		return -1
	}, value)
}
