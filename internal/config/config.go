package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const currentVersion = 1

type Config struct {
	Version  int            `json:"version"`
	Telegram TelegramConfig `json:"telegram"`
	WhatsApp WhatsAppConfig `json:"whatsapp"`
	HTTP     HTTPConfig     `json:"http"`
}

type WhatsAppConfig struct {
	DefaultRecipient string `json:"default_recipient,omitempty"`
	AutoDownload     bool   `json:"auto_download,omitempty"`
}

type HTTPConfig struct {
	AutoDownload bool `json:"auto_download,omitempty"`
	LastPort     int  `json:"last_port,omitempty"`
}

type TelegramConfig struct {
	BotToken                string   `json:"bot_token,omitempty"`
	BotTokenProtected       string   `json:"bot_token_protected,omitempty"`
	Receiver                Receiver `json:"receiver"`
	AutoDownload            bool     `json:"auto_download,omitempty"`
	UpdateOffset            int64    `json:"update_offset,omitempty"`
	UpdateOffsetInitialized bool     `json:"update_offset_initialized,omitempty"`
}

type Receiver struct {
	Kind     string `json:"kind,omitempty"`
	ID       int64  `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

func (t TelegramConfig) Ready() bool {
	return t.BotToken != "" && t.Receiver.ID != 0
}

type Store struct {
	path string
	mu   sync.Mutex
}

func (s *Store) Update(change func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.Load()
	if err != nil {
		return err
	}
	if err := change(&cfg); err != nil {
		return err
	}
	return s.Save(cfg)
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
	if err := loadProtectedToken(&cfg.Telegram); err != nil {
		return Config{}, fmt.Errorf("decrypt Telegram bot token: %w", err)
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
	if err := prepareTokenForSave(&cfg.Telegram); err != nil {
		return fmt.Errorf("protect Telegram bot token: %w", err)
	}
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
