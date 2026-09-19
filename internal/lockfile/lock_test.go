package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireExcludesAnotherOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "receiver.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	second, err := Acquire(path)
	if !errors.Is(err, ErrLocked) {
		if second != nil {
			_ = second.Release()
		}
		t.Fatalf("second Acquire error = %v, want ErrLocked", err)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
}
