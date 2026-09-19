package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaMethod(t *testing.T) {
	tests := map[string]string{
		"photo.JPG": "sendPhoto",
		"clip.mp4":  "sendVideo",
		"data.zip":  "sendDocument",
	}
	for path, want := range tests {
		got, _ := mediaMethod(path)
		if got != want {
			t.Errorf("mediaMethod(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestMediaGroupCompatible(t *testing.T) {
	if !mediaGroupCompatible([]string{"one.jpg", "two.mp4"}) {
		t.Fatal("photo/video mix should be album-compatible")
	}
	if mediaGroupCompatible([]string{"one.jpg"}) {
		t.Fatal("single file must not use an album")
	}
	if mediaGroupCompatible([]string{"one.jpg", "notes.txt"}) {
		t.Fatal("document mix must fall back to sequential sending")
	}
}

func TestValidateFilesRejectsOfficialBotAPISizeLimit(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want string
	}{
		{"large.jpg", officialPhotoUploadLimit + 1, "photo uploads up to 10 MiB"},
		{"large.zip", officialFileUploadLimit + 1, "file uploads up to 50 MiB"},
	}
	for _, test := range tests {
		path := filepath.Join(t.TempDir(), test.name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(test.size); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = ValidateFiles([]string{path})
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("ValidateFiles(%s) error = %v, want %q", test.name, err, test.want)
		}
	}
}

func TestMatchesBotMarker(t *testing.T) {
	const marker = "/sendcli_ABC123"
	for _, value := range []string{marker, marker + "@example_bot", "  " + marker + "  "} {
		if !matchesBotMarker(value, marker) {
			t.Errorf("%q should match", value)
		}
	}
	if matchesBotMarker("/sendcli_OTHER", marker) {
		t.Fatal("different marker must not match")
	}
}
