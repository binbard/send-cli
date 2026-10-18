package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBashCompletionInstallAndUninstallPreservesProfiles(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	profile := filepath.Join(home, ".profile")
	original := "export ORIGINAL=1\n"
	if err := os.WriteFile(profile, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRootCommand()
	for i := 0; i < 2; i++ {
		if err := installCompletion(root, "bash"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "# send-cli completion bash start") != 1 {
		t.Fatalf("installer duplicated the profile block: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".bash_profile")); !os.IsNotExist(err) {
		t.Fatal("installer shadowed the existing .profile")
	}
	if err := uninstallCompletion("bash"); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("profile changed after uninstall: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "send-cli", "completions", "send.bash")); !os.IsNotExist(err) {
		t.Fatal("completion script was not removed")
	}
}

func TestCompletionInstallDoesNotOverwriteUnmanagedScript(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	path := filepath.Join(home, ".local", "share", "send-cli", "completions", "send.zsh")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("my custom completion"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installCompletion(newRootCommand(), "zsh"); err == nil {
		t.Fatal("expected an error for an unmanaged completion file")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "my custom completion" {
		t.Fatalf("unmanaged file changed: %q, %v", got, err)
	}
}

func TestClinkCompletionIncludesAliasesAndNestedCommands(t *testing.T) {
	var output bytes.Buffer
	if err := writeClinkCompletion(&output, newRootCommand()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"clink.argmatcher(\"send\")", "\"tg\" ..", "\"wa\" ..", "\"join\" ..", "\"install\" ..", "\"--to\" .. clink.argmatcher():addarg()", "--setup", "clink.filematches"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("Clink script does not include %q", want)
		}
	}
}

func TestManagedProfileBlockRoundTripWithCRLF(t *testing.T) {
	original := "# existing profile\r\nWrite-Host 'ready'\r\n"
	start, end := profileMarkers("powershell")
	installed, err := replaceProfileBlock(original, start, end, "Write-Host 'completion'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(installed, "\r\n"+start+"\r\n") {
		t.Fatalf("profile block did not use CRLF: %q", installed)
	}
	restored, err := replaceProfileBlock(installed, start, end, "")
	if err != nil {
		t.Fatal(err)
	}
	if restored != original {
		t.Fatalf("profile was not restored: %q", restored)
	}
	if _, err := replaceProfileBlock(original+start, start, end, "new body"); err == nil {
		t.Fatal("expected a partial marker to be rejected")
	}
}
