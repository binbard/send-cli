//go:build !windows

package config

func prepareTokenForSave(*TelegramConfig) error {
	return nil
}

func loadProtectedToken(*TelegramConfig) error {
	return nil
}
