//go:build windows

package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func pickFiles() ([]selectedFile, error) {
	script := `
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.OpenFileDialog
$dialog.Multiselect = $true
$dialog.CheckFileExists = $true
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  $dialog.FileNames | ForEach-Object { [Console]::WriteLine($_) }
}`
	output, err := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", script).Output()
	if err != nil {
		return nil, fmt.Errorf("open Windows file picker: %w", err)
	}
	return selectedLines(string(output), false), nil
}

func clipboardFiles() ([]selectedFile, error) {
	temp := filepath.Join(os.TempDir(), fmt.Sprintf("send-cli-clipboard-%d.png", time.Now().UnixNano()))
	script := `
Add-Type -AssemblyName System.Windows.Forms
if ([System.Windows.Forms.Clipboard]::ContainsFileDropList()) {
  [System.Windows.Forms.Clipboard]::GetFileDropList() | ForEach-Object { [Console]::WriteLine($_) }
  exit 0
}
if ([System.Windows.Forms.Clipboard]::ContainsImage()) {
  [System.Windows.Forms.Clipboard]::GetImage().Save($args[0], [System.Drawing.Imaging.ImageFormat]::Png)
  [Console]::WriteLine($args[0])
}`
	output, err := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", script, temp).Output()
	if err != nil {
		return nil, fmt.Errorf("read Windows clipboard: %w", err)
	}
	files := selectedLines(string(output), false)
	for i := range files {
		if strings.EqualFold(files[i].Path, temp) {
			files[i].Temporary = true
		}
	}
	return files, nil
}

func selectedLines(output string, temporary bool) []selectedFile {
	var files []selectedFile
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		path := strings.TrimSpace(line)
		if path != "" {
			files = append(files, selectedFile{Path: path, Temporary: temporary})
		}
	}
	return files
}
