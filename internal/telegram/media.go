package telegram

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

const (
	officialPhotoUploadLimit = int64(10 * 1024 * 1024)
	officialFileUploadLimit  = int64(50 * 1024 * 1024)
	officialDownloadLimit    = int64(20 * 1024 * 1024)
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
		limit, kind := uploadLimit(abs)
		if info.Size() > limit {
			return nil, fmt.Errorf(
				"%q is %.1f MiB; the official Telegram Bot API accepts %s uploads up to %.0f MiB (a self-hosted local Bot API server has different limits)",
				path,
				float64(info.Size())/(1024*1024),
				kind,
				float64(limit)/(1024*1024),
			)
		}
		result = append(result, abs)
	}
	return result, nil
}

func uploadLimit(path string) (int64, string) {
	method, _ := mediaMethod(path)
	if method == "sendPhoto" {
		return officialPhotoUploadLimit, "photo"
	}
	return officialFileUploadLimit, "file"
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
