//go:build windows

package config

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareTokenForSave(cfg *TelegramConfig) error {
	if cfg.BotToken == "" {
		cfg.BotTokenProtected = ""
		return nil
	}
	if cfg.BotTokenProtected == "" {
		protected, err := protectToken([]byte(cfg.BotToken))
		if err != nil {
			return err
		}
		cfg.BotTokenProtected = base64.StdEncoding.EncodeToString(protected)
	}
	cfg.BotToken = ""
	return nil
}

func loadProtectedToken(cfg *TelegramConfig) error {
	if cfg.BotTokenProtected == "" {
		return nil
	}
	protected, err := base64.StdEncoding.DecodeString(cfg.BotTokenProtected)
	if err != nil {
		return fmt.Errorf("decode protected token: %w", err)
	}
	token, err := unprotectToken(protected)
	if err != nil {
		return err
	}
	cfg.BotToken = string(token)
	clear(token)
	return nil
}

func protectToken(token []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(token)), Data: &token[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(
		&input,
		nil,
		nil,
		0,
		nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&output,
	); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}

func unprotectToken(protected []byte) ([]byte, error) {
	if len(protected) == 0 {
		return nil, fmt.Errorf("protected token is empty")
	}
	input := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(
		&input,
		nil,
		nil,
		0,
		nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&output,
	); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
