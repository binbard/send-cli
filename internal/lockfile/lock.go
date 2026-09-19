package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// ErrLocked indicates that another process owns the requested lock.
var ErrLocked = errors.New("lock is already held")

// Lock is an operating-system-backed advisory lock.
type Lock struct {
	file *os.File
}

// Acquire obtains an exclusive, non-blocking lock for path.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLock(file); err != nil {
		_ = file.Close()
		if isContended(err) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock file: %w", err)
	}

	// The PID is diagnostic only. The OS lock, not this content, owns validity.
	if err := file.Truncate(0); err == nil {
		if _, err := file.Seek(0, 0); err == nil {
			_, _ = file.WriteString(strconv.Itoa(os.Getpid()) + "\n")
		}
	}
	return &Lock{file: file}, nil
}

// Release relinquishes the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unlock(file)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock file: %w", unlockErr)
	}
	return closeErr
}
