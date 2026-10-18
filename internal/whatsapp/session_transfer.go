package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxTransferSize bounds the session data kept in memory during provisioning.
const MaxTransferSize = 64 << 20

// ExportSession takes a consistent SQLite snapshot of a linked session.
// Callers must hold whatsapp.lock so another send process cannot write to it.
func ExportSession(ctx context.Context, path string) ([]byte, error) {
	linked, err := IsLinked(ctx, path)
	if err != nil || !linked {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "whatsapp-export-*.db")
	if err != nil {
		return nil, fmt.Errorf("prepare WhatsApp snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath) // VACUUM INTO requires a path that does not exist.
	defer os.Remove(tmpPath)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open WhatsApp snapshot source: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		return nil, fmt.Errorf("snapshot WhatsApp session: %w", err)
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxTransferSize {
		return nil, fmt.Errorf("WhatsApp session exceeds the %d MiB provisioning limit", MaxTransferSize>>20)
	}
	return os.ReadFile(tmpPath)
}

// InstallSession validates a snapshot and installs it only if no local session exists.
func InstallSession(ctx context.Context, path string, snapshot []byte) error {
	if len(snapshot) == 0 || len(snapshot) > MaxTransferSize {
		return errors.New("invalid WhatsApp session snapshot size")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return errors.New("a local WhatsApp session already exists; it was not replaced")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "whatsapp-join-*.db")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(tmpPath + suffix)
		}
	}()
	if _, err := tmp.Write(snapshot); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	linked, err := IsLinked(ctx, tmpPath)
	if err != nil {
		return fmt.Errorf("validate transferred WhatsApp session: %w", err)
	}
	if !linked {
		return errors.New("transferred WhatsApp session is not linked")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("a local WhatsApp session already exists; it was not replaced")
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(snapshot)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("install WhatsApp session: %w", err)
	}
	return nil
}

// RemoveInstalledSession rolls back an installation that has not been opened.
func RemoveInstalledSession(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove WhatsApp session %s: %w", strings.TrimPrefix(suffix, "-"), err)
		}
	}
	return nil
}
