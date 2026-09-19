package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const currentVersion = 1

type Config struct {
	Version  int            `json:"version"`
	Telegram TelegramConfig `json:"telegram"`
}

type TelegramConfig struct {
	Mode         string   `json:"mode,omitempty"`
	BotToken     string   `json:"bot_token,omitempty"`
	Receiver     Receiver `json:"receiver"`
	AutoDownload bool     `json:"auto_download,omitempty"`
}

type Receiver struct {
	Kind       string `json:"kind,omitempty"`
	ID         int64  `json:"id,omitempty"`
	AccessHash int64  `json:"access_hash,omitempty"`
	Title      string `json:"title,omitempty"`
	Username   string `json:"username,omitempty"`
}

func (t TelegramConfig) Ready() bool {
	if t.Receiver.ID == 0 {
		return false
	}
	switch t.Mode {
	case "bot":
		return t.BotToken != ""
	case "user":
		return t.Receiver.Kind != ""
	default:
		return false
	}
}

type Store struct {
	path string
}

func NewStore() (*Store, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find user config directory: %w", err)
	}
	return &Store{path: filepath.Join(root, "send-cli", "config.json")}, nil
}

func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) SessionPath() string {
	return filepath.Join(filepath.Dir(s.path), "telegram-session.json")
}

func (s *Store) Load() (Config, error) {
	cfg := Config{Version: currentVersion}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode %s: %w", s.path, err)
	}
	if cfg.Version == 0 {
		cfg.Version = currentVersion
	}
	if cfg.Version != currentVersion {
		return Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	return cfg, nil
}

func (s *Store) Save(cfg Config) error {
	cfg.Version = currentVersion
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(s.path)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
