package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type target struct {
	goos, goarch string
}

var targets = []target{
	{"windows", "amd64"}, {"windows", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"android", "arm64"},
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/build <local|dist>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "local":
		err = buildLocal()
	case "dist":
		err = buildDist()
	default:
		err = fmt.Errorf("unknown build task %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildLocal() error {
	if err := os.MkdirAll("bin", 0o755); err != nil {
		return err
	}
	path := filepath.Join("bin", binaryName(runtime.GOOS))
	if err := build(target{runtime.GOOS, runtime.GOARCH}, path); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func buildDist() error {
	label := strings.TrimSpace(os.Getenv("SEND_BUILD_LABEL"))
	if label == "" {
		output, err := exec.Command("git", "rev-parse", "--short=7", "HEAD").Output()
		if err != nil {
			return fmt.Errorf("determine build revision: %w", err)
		}
		label = strings.TrimSpace(string(output))
	}
	if label == "" || strings.ContainsAny(label, `/\ :`) || label == "." || label == ".." {
		return errors.New("invalid build label")
	}
	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll("dist", 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("build", "send-dist-")
	if err != nil {
		return err
	}
	defer cleanupStage(stage)
	var archives []string
	for _, platform := range targets {
		binary := binaryName(platform.goos)
		output := filepath.Join(stage, binary)
		if err := build(platform, output); err != nil {
			return err
		}
		archive := fmt.Sprintf("send-%s-%s-%s", label, platform.goos, platform.goarch)
		if platform.goos == "windows" {
			archive += ".zip"
		} else {
			archive += ".tar.gz"
		}
		if err := createArchive(filepath.Join(stage, archive), output, binary, platform.goos); err != nil {
			return fmt.Errorf("package %s: %w", archive, err)
		}
		if err := os.Remove(output); err != nil {
			return err
		}
		archives = append(archives, archive)
	}
	entries, err := os.ReadDir("dist")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".zip") && !strings.HasSuffix(entry.Name(), ".tar.gz")) {
			return fmt.Errorf("dist contains %q; remove it before rebuilding archives", entry.Name())
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join("dist", entry.Name())); err != nil {
			return err
		}
	}
	for _, archive := range archives {
		if err := os.Rename(filepath.Join(stage, archive), filepath.Join("dist", archive)); err != nil {
			return err
		}
		fmt.Println(filepath.Join("dist", archive))
	}
	return nil
}

func cleanupStage(stage string) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(stage, entry.Name()))
		}
	}
	_ = os.Remove(stage)
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "send.exe"
	}
	return "send"
}

func build(platform target, output string) error {
	flags := "-s -w"
	if version := strings.TrimSpace(os.Getenv("SEND_BUILD_VERSION")); version != "" {
		flags += " -X send-cli/internal/cli.version=" + version
	}
	command := exec.Command("go", "build", "-trimpath", "-ldflags="+flags, "-o", output, "./cmd/send")
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+platform.goos, "GOARCH="+platform.goarch)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s/%s: %w", platform.goos, platform.goarch, err)
	}
	return nil
}

func createArchive(path, binaryPath, binaryName, goos string) error {
	if goos == "windows" {
		return writeZip(path, binaryPath, binaryName)
	}
	return writeTarGzip(path, binaryPath, binaryName)
}

func writeZip(path, binaryPath, binaryName string) (err error) {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	archive := zip.NewWriter(output)
	defer func() { err = errors.Join(err, archive.Close()) }()
	header := &zip.FileHeader{Name: binaryName, Method: zip.Deflate}
	header.SetMode(0o755)
	entry, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	binary, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer binary.Close()
	_, err = io.Copy(entry, binary)
	return err
}

func writeTarGzip(path, binaryPath, binaryName string) (err error) {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	compressed := gzip.NewWriter(output)
	defer func() { err = errors.Join(err, compressed.Close()) }()
	archive := tar.NewWriter(compressed)
	defer func() { err = errors.Join(err, archive.Close()) }()
	binary, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer binary.Close()
	info, err := binary.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = binaryName
	header.Mode = 0o755
	header.Uid, header.Gid = 0, 0
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(archive, binary)
	return err
}
