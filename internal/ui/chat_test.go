package ui

import (
	"os"
	"path/filepath"
	"testing"
)

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
