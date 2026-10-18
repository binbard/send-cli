package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const completionHeader = "send-cli completion; managed by send completion install"

func addCompletionCommands(root *cobra.Command) {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var completion *cobra.Command
	for _, child := range root.Commands() {
		if child.Name() == "completion" {
			completion = child
			break
		}
	}
	if completion == nil {
		panic("Cobra completion command missing")
	}
	completion.AddCommand(&cobra.Command{
		Use:   "cmd",
		Short: "Generate completion for cmd.exe with Clink",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeClinkCompletion(command.OutOrStdout(), command.Root())
		},
	})
	completion.AddCommand(&cobra.Command{
		Use:       "install <bash|zsh|fish|powershell|cmd>",
		Short:     "Install completion for one shell",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell", "cmd"},
		RunE: func(command *cobra.Command, args []string) error {
			if err := installCompletion(command.Root(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Installed %s completion. Open a new shell to use it.\n", args[0])
			return nil
		},
	})
	completion.AddCommand(&cobra.Command{
		Use:       "uninstall <bash|zsh|fish|powershell|cmd>",
		Short:     "Remove completion installed by send",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell", "cmd"},
		RunE: func(command *cobra.Command, args []string) error {
			if err := uninstallCompletion(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Removed %s completion. Open a new shell to apply the change.\n", args[0])
			return nil
		},
	})
}

func completionPaths(shell string) (script string, profiles []string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	base := filepath.Join(home, ".local", "share", "send-cli", "completions")
	switch shell {
	case "bash":
		login := filepath.Join(home, ".bash_profile")
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			candidate := filepath.Join(home, name)
			if _, err := os.Stat(candidate); err == nil {
				login = candidate
				break
			}
		}
		return filepath.Join(base, "send.bash"), []string{filepath.Join(home, ".bashrc"), login}, nil
	case "zsh":
		return filepath.Join(base, "send.zsh"), []string{filepath.Join(home, ".zshrc")}, nil
	case "fish":
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return filepath.Join(config, "fish", "completions", "send.fish"), nil, nil
	case "powershell":
		if runtime.GOOS == "windows" {
			local := os.Getenv("LOCALAPPDATA")
			if local == "" {
				return "", nil, errors.New("LOCALAPPDATA is not set")
			}
			base = filepath.Join(local, "send-cli", "completions")
		}
		return filepath.Join(base, "send.ps1"), nil, nil
	case "cmd":
		if runtime.GOOS != "windows" {
			return "", nil, errors.New("Clink completion for cmd.exe is only available on Windows")
		}
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", nil, errors.New("LOCALAPPDATA is not set")
		}
		return filepath.Join(local, "send-cli", "clink", "completions", "send.lua"), nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported shell %q (choose bash, zsh, fish, powershell, or cmd)", shell)
	}
}

func completionScript(root *cobra.Command, shell string) ([]byte, error) {
	var output bytes.Buffer
	var err error
	switch shell {
	case "bash":
		err = root.GenBashCompletionV2(&output, true)
	case "zsh":
		err = root.GenZshCompletion(&output)
	case "fish":
		err = root.GenFishCompletion(&output, true)
	case "powershell":
		err = root.GenPowerShellCompletionWithDesc(&output)
	case "cmd":
		err = writeClinkCompletion(&output, root)
	default:
		return nil, fmt.Errorf("unsupported shell %q", shell)
	}
	if err != nil {
		return nil, err
	}
	if shell == "cmd" {
		return append([]byte("-- "+completionHeader+"\n"), output.Bytes()...), nil
	}
	return append([]byte("# "+completionHeader+"\n"), output.Bytes()...), nil
}

func installCompletion(root *cobra.Command, shell string) error {
	script, profiles, err := completionPaths(shell)
	if err != nil {
		return err
	}
	content, err := completionScript(root, shell)
	if err != nil {
		return err
	}
	if shell == "powershell" {
		profiles, err = powershellProfiles()
		if err != nil {
			return err
		}
	}
	if shell == "cmd" {
		if old, err := os.ReadFile(script); err == nil && !bytes.HasPrefix(old, []byte("-- "+completionHeader+"\n")) {
			return fmt.Errorf("%s is not managed by send", script)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err := exec.LookPath("clink"); err != nil {
			return errors.New("Clink is required for cmd.exe completion; install Clink first")
		}
	}
	if err := writeManagedScript(script, content); err != nil {
		return err
	}
	if shell == "powershell" {
		if err := writeManagedScript(script+".profiles", []byte("# "+completionHeader+"\n"+strings.Join(profiles, "\n")+"\n")); err != nil {
			return err
		}
	}
	for _, profile := range profiles {
		if err := writeProfileBlock(profile, shell, script); err != nil {
			return err
		}
	}
	if shell == "cmd" {
		output, err := exec.Command("clink", "installscripts", filepath.Dir(filepath.Dir(script))).CombinedOutput()
		if err != nil {
			return fmt.Errorf("register completion with Clink: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}

func uninstallCompletion(shell string) error {
	script, profiles, err := completionPaths(shell)
	if err != nil {
		return err
	}
	if shell == "bash" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		profiles = []string{filepath.Join(home, ".bashrc"), filepath.Join(home, ".bash_profile"), filepath.Join(home, ".bash_login"), filepath.Join(home, ".profile")}
	}
	if shell == "powershell" {
		state, err := os.ReadFile(script + ".profiles")
		if err == nil {
			if !strings.HasPrefix(string(state), "# "+completionHeader+"\n") {
				return fmt.Errorf("%s is not managed by send", script+".profiles")
			}
			profiles = strings.Split(strings.TrimSpace(strings.TrimPrefix(string(state), "# "+completionHeader+"\n")), "\n")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, profile := range profiles {
		if err := removeProfileBlock(profile, shell); err != nil {
			return err
		}
	}
	if shell == "cmd" {
		if _, err := exec.LookPath("clink"); err != nil {
			return errors.New("Clink is required to unregister cmd.exe completion")
		}
		output, err := exec.Command("clink", "uninstallscripts", filepath.Dir(filepath.Dir(script))).CombinedOutput()
		if err != nil {
			return fmt.Errorf("unregister completion from Clink: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	if err := removeManagedScript(script); err != nil {
		return err
	}
	if shell == "powershell" {
		if err := removeManagedScript(script + ".profiles"); err != nil {
			return err
		}
	}
	return nil
}

func writeManagedScript(path string, content []byte) error {
	old, err := os.ReadFile(path)
	if err == nil && !bytes.HasPrefix(old, []byte("# "+completionHeader+"\n")) && !bytes.HasPrefix(old, []byte("-- "+completionHeader+"\n")) {
		return fmt.Errorf("%s exists and is not managed by send", path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0600)
}

func removeManagedScript(path string) error {
	old, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(old, []byte("# "+completionHeader+"\n")) && !bytes.HasPrefix(old, []byte("-- "+completionHeader+"\n")) {
		return fmt.Errorf("%s is not managed by send", path)
	}
	return os.Remove(path)
}

func profileBlock(shell, script string) string {
	switch shell {
	case "bash", "zsh":
		path := "$HOME/.local/share/send-cli/completions/send." + shell
		if shell == "zsh" {
			return "if ! whence -w compdef >/dev/null 2>&1; then autoload -Uz compinit; compinit; fi\n[ -f \"" + path + "\" ] && source \"" + path + "\""
		}
		return "if [ -n \"${BASH_VERSION:-}\" ] && [ -f \"" + path + "\" ]; then . \"" + path + "\"; fi"
	case "powershell":
		return "if (Test-Path -LiteralPath '" + strings.ReplaceAll(script, "'", "''") + "') { . '" + strings.ReplaceAll(script, "'", "''") + "' }"
	default:
		return ""
	}
}

func profileMarkers(shell string) (string, string) {
	return "# send-cli completion " + shell + " start", "# send-cli completion " + shell + " end"
}

func writeProfileBlock(path, shell, script string) error {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
	}
	start, end := profileMarkers(shell)
	data, err := replaceProfileBlock(string(old), start, end, profileBlock(shell, script))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(data), 0600)
}

func removeProfileBlock(path, shell string) error {
	old, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	start, end := profileMarkers(shell)
	data, err := replaceProfileBlock(string(old), start, end, "")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if data != string(old) {
		return os.WriteFile(path, []byte(data), 0600)
	}
	return nil
}

func replaceProfileBlock(existing, start, end, body string) (string, error) {
	startCount, endCount := strings.Count(existing, start), strings.Count(existing, end)
	if startCount != endCount || startCount > 1 {
		return "", errors.New("completion markers were changed; edit this profile manually")
	}
	if startCount == 1 {
		from := strings.Index(existing, start)
		to := strings.Index(existing, end)
		if from > to || !isWholeProfileLine(existing, from, start) || !isWholeProfileLine(existing, to, end) {
			return "", errors.New("completion markers are out of order")
		}
		to += len(end)
		if to < len(existing) && existing[to] == '\r' {
			to++
		}
		if to < len(existing) && existing[to] == '\n' {
			to++
		}
		if from > 0 && existing[from-1] == '\n' {
			from--
			if from > 0 && existing[from-1] == '\r' {
				from--
			}
		}
		existing = existing[:from] + existing[to:]
	}
	if body == "" {
		return existing, nil
	}
	nl := "\n"
	if strings.Contains(existing, "\r\n") {
		nl = "\r\n"
	}
	return existing + nl + start + nl + strings.ReplaceAll(body, "\n", nl) + nl + end + nl, nil
}

func isWholeProfileLine(content string, offset int, marker string) bool {
	if offset < 0 || (offset > 0 && content[offset-1] != '\n') {
		return false
	}
	after := offset + len(marker)
	return after == len(content) || content[after] == '\r' || content[after] == '\n'
}

func powershellProfiles() ([]string, error) {
	var executables []string
	if runtime.GOOS == "windows" {
		executables = []string{"powershell.exe", "pwsh.exe"}
	} else {
		executables = []string{"pwsh"}
	}
	var profiles []string
	for _, name := range executables {
		if _, err := exec.LookPath(name); err != nil {
			continue
		}
		output, err := exec.Command(name, "-NoProfile", "-NonInteractive", "-Command", "[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($PROFILE.CurrentUserAllHosts))").Output()
		if err != nil {
			return nil, fmt.Errorf("find %s profile: %w", name, err)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(output), "\ufeff")))
		if err != nil {
			return nil, fmt.Errorf("decode %s profile path: %w", name, err)
		}
		path := string(decoded)
		if path == "" {
			return nil, fmt.Errorf("%s did not report a profile path", name)
		}
		profiles = append(profiles, path)
	}
	if len(profiles) == 0 {
		return nil, errors.New("PowerShell was not found")
	}
	return profiles, nil
}

func writeClinkCompletion(out io.Writer, root *cobra.Command) error {
	_, err := io.WriteString(out, "-- Clink completion for send; generated from the command tree.\n")
	if err != nil {
		return err
	}
	var write func(*cobra.Command, string, bool) error
	index := 0
	write = func(command *cobra.Command, name string, isRoot bool) error {
		children := make([]*cobra.Command, 0)
		for _, child := range command.Commands() {
			if child.IsAvailableCommand() && !child.Hidden {
				children = append(children, child)
			}
		}
		childNames := make([]string, len(children))
		for i, child := range children {
			index++
			childNames[i] = fmt.Sprintf("matcher%d", index)
			if err := write(child, childNames[i], false); err != nil {
				return err
			}
		}
		constructor := "clink.argmatcher()"
		if isRoot {
			constructor = "clink.argmatcher(\"send\")"
		}
		if _, err := fmt.Fprintf(out, "local %s = %s\n", name, constructor); err != nil {
			return err
		}
		flags := []string{luaString("--help"), luaString("-h")}
		if isRoot {
			flags = append(flags, luaString("--version"))
		}
		command.Flags().VisitAll(func(flag *pflag.Flag) {
			if !flag.Hidden {
				long := luaString("--" + flag.Name)
				short := ""
				if flag.Shorthand != "" {
					short = luaString("-" + flag.Shorthand)
				}
				if flag.Value.Type() != "bool" && flag.NoOptDefVal == "" {
					value := " .. clink.argmatcher():addarg()"
					long += value
					if short != "" {
						short += value
					}
				}
				flags = append(flags, long)
				if short != "" {
					flags = append(flags, short)
				}
			}
		})
		sort.Strings(flags)
		if _, err := fmt.Fprintf(out, "%s:addflags(%s)\n", name, strings.Join(flags, ", ")); err != nil {
			return err
		}
		if len(children) > 0 {
			var links []string
			for i, child := range children {
				for _, word := range append([]string{child.Name()}, child.Aliases...) {
					links = append(links, luaString(word)+" .. "+childNames[i])
				}
			}
			if isRoot {
				links = append(links, luaString("help"))
			}
			if strings.Contains(command.Use, "[files") {
				_, err := fmt.Fprintf(out, "%s:addarg({%s, clink.filematches}):loop(1)\n", name, strings.Join(links, ", "))
				return err
			}
			_, err := fmt.Fprintf(out, "%s:addarg({%s}):nofiles()\n", name, strings.Join(links, ", "))
			return err
		}
		if strings.Contains(command.Use, "[files") {
			_, err := fmt.Fprintf(out, "%s:addarg(clink.filematches):loop(1)\n", name)
			return err
		}
		_, err := fmt.Fprintf(out, "%s:nofiles()\n", name)
		return err
	}
	return write(root, "send_matcher", true)
}

func luaString(value string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\"") + "\""
}
