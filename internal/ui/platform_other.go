//go:build !windows

package ui

import "errors"

func pickFiles() ([]selectedFile, error) {
	return nil, errors.New("native file picker is currently available on Windows; paste a file path instead")
}

func clipboardFiles() ([]selectedFile, error) {
	return nil, errors.New("file and image clipboard support is currently available on Windows")
}
