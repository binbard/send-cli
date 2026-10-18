package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveContainsOnlyExecutable(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "source")
	if err := os.WriteFile(binary, []byte("binary data"), 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(directory, "windows.zip")
	if err := createArchive(zipPath, binary, "send.exe", "windows"); err != nil {
		t.Fatal(err)
	}
	zipped, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zipped.Close()
	if len(zipped.File) != 1 || zipped.File[0].Name != "send.exe" {
		t.Fatalf("zip entries = %#v", zipped.File)
	}

	tarPath := filepath.Join(directory, "mac.tar.gz")
	if err := createArchive(tarPath, binary, "send", "darwin"); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	header, err := archive.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "send" || header.Mode != 0o755 {
		t.Fatalf("tar entry = %q mode %#o", header.Name, header.Mode)
	}
	data, err := io.ReadAll(archive)
	if err != nil || string(data) != "binary data" {
		t.Fatalf("tar binary = %q, %v", data, err)
	}
	if _, err := archive.Next(); err != io.EOF {
		t.Fatalf("expected one tar entry, got %v", err)
	}
}
