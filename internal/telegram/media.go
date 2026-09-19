package telegram

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

func ValidateFiles(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", path, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", path)
		}
		result = append(result, abs)
	}
	return result, nil
}

func mediaMethod(path string) (method, field string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return "sendPhoto", "photo"
	case ".mp4", ".mov", ".m4v":
		return "sendVideo", "video"
	default:
		return "sendDocument", "document"
	}
}

func mimeType(path string) string {
	if value := mime.TypeByExtension(filepath.Ext(path)); value != "" {
		return value
	}
	return "application/octet-stream"
}
