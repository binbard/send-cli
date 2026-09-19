package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"send-cli/internal/config"
	tgservice "send-cli/internal/telegram"
	"send-cli/internal/ui"
)

const version = "0.1.0"

func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return newRootCommand().ExecuteContext(ctx)
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "send",
		Short:         "Send files and messages with minimum friction",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.AddCommand(newTelegramCommand())
	root.AddCommand(newListCommand())
	for _, channel := range []struct {
		name    string
		aliases []string
		summary string
	}{
		{name: "whatsapp", aliases: []string{"wa"}, summary: "Send with WhatsApp"},
		{name: "adb", summary: "Send to an Android device with ADB"},
		{name: "ssh", summary: "Send over SSH"},
		{name: "http", summary: "Send over HTTP"},
		{name: "lan", summary: "Discover and send to peers on the LAN"},
	} {
		channel := channel
		root.AddCommand(&cobra.Command{
			Use:     channel.name,
			Aliases: channel.aliases,
			Short:   channel.summary + " (not implemented)",
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("%s support is not implemented yet", channel.name)
			},
		})
	}
	return root
}

func newTelegramCommand() *cobra.Command {
	var (
		message      string
		setup        bool
		logout       bool
		askDownloads bool
	)
	command := &cobra.Command{
		Use:     "telegram [files...]",
		Aliases: []string{"tg"},
		Short:   "Send or receive with Telegram",
		Args:    cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			store, err := config.NewStore()
			if err != nil {
				return err
			}
			cfg, err := store.Load()
			if err != nil {
				return err
			}
			if logout {
				cfg.Telegram = config.TelegramConfig{}
				if err := store.Save(cfg); err != nil {
					return err
				}
				if err := os.Remove(store.SessionPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove Telegram session: %w", err)
				}
				fmt.Fprintln(command.OutOrStdout(), "Removed local Telegram login and receiver configuration.")
				return nil
			}
			if askDownloads {
				cfg.Telegram.AutoDownload = false
				if err := store.Save(cfg); err != nil {
					return err
				}
			}
			if setup || !cfg.Telegram.Ready() {
				telegramConfig, err := setupTelegram(command.Context(), store, command.InOrStdin(), command.OutOrStdout())
				if err != nil {
					return err
				}
				cfg.Telegram = telegramConfig
				if err := store.Save(cfg); err != nil {
					return err
				}
				fmt.Fprintln(command.OutOrStdout(), "Telegram setup complete.")
			}

			files, err := tgservice.ValidateFiles(args)
			if err != nil {
				return err
			}
			service, err := telegramService(store, cfg.Telegram)
			if err != nil {
				return err
			}
			interactive := len(files) == 0 && !command.Flags().Changed("message")
			if !interactive {
				return service.Run(command.Context(), false, func(session tgservice.Session) error {
					if err := session.Send(command.Context(), message, files); err != nil {
						return err
					}
					if len(files) == 0 {
						fmt.Fprintf(command.OutOrStdout(), "Message sent to %s.\n", receiverName(cfg.Telegram.Receiver))
					} else {
						fmt.Fprintf(command.OutOrStdout(), "Sent %d file(s) to %s.\n", len(files), receiverName(cfg.Telegram.Receiver))
					}
					return nil
				})
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				return errors.New("interactive mode requires a terminal; provide files or use -m")
			}
			return service.Run(command.Context(), true, func(session tgservice.Session) error {
				return ui.Run(command.Context(), session, ui.Options{
					Receiver:     receiverName(cfg.Telegram.Receiver),
					AutoDownload: cfg.Telegram.AutoDownload,
					SetAutoDownload: func(enabled bool) error {
						latest, err := store.Load()
						if err != nil {
							return err
						}
						latest.Telegram.AutoDownload = enabled
						return store.Save(latest)
					},
				})
			})
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "message or file caption")
	command.Flags().BoolVar(&setup, "setup", false, "log in and select a default receiver")
	command.Flags().BoolVar(&logout, "logout", false, "remove the local Telegram login and configuration")
	command.Flags().BoolVar(&askDownloads, "ask-downloads", false, "ask before downloading incoming files again")
	return command
}

func telegramService(store *config.Store, cfg config.TelegramConfig) (tgservice.Service, error) {
	switch cfg.Mode {
	case "bot":
		return tgservice.NewBotService(cfg.BotToken, cfg.Receiver), nil
	case "user":
		return tgservice.NewUserService(store.SessionPath(), cfg.Receiver)
	default:
		return nil, fmt.Errorf("unknown Telegram mode %q; run send telegram --setup", cfg.Mode)
	}
}

func setupTelegram(ctx context.Context, store *config.Store, input io.Reader, output io.Writer) (config.TelegramConfig, error) {
	reader := bufio.NewReader(input)
	fmt.Fprintln(output, "Telegram setup")
	fmt.Fprintln(output, "  1. User account (phone/code/2FA)")
	fmt.Fprintln(output, "  2. Bot API token")
	fmt.Fprint(output, "Choose [1/2]: ")
	choice, err := reader.ReadString('\n')
	if err != nil {
		return config.TelegramConfig{}, err
	}
	marker, err := setupMarker()
	if err != nil {
		return config.TelegramConfig{}, err
	}
	switch strings.TrimSpace(strings.ToLower(choice)) {
	case "1", "user", "u":
		return tgservice.SetupUser(ctx, store.SessionPath(), marker, output)
	case "2", "bot", "b":
		fmt.Fprint(output, "BotFather token: ")
		var token string
		if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			value, readErr := term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(output)
			if readErr != nil {
				return config.TelegramConfig{}, readErr
			}
			token = strings.TrimSpace(string(value))
		} else {
			token, err = reader.ReadString('\n')
			if err != nil {
				return config.TelegramConfig{}, err
			}
			token = strings.TrimSpace(token)
		}
		if token == "" {
			return config.TelegramConfig{}, errors.New("bot token cannot be empty")
		}
		botMarker := "/sendcli_" + strings.ReplaceAll(strings.TrimPrefix(marker, "send-cli setup "), "-", "")
		return tgservice.SetupBot(ctx, token, botMarker, output)
	default:
		return config.TelegramConfig{}, errors.New("choose 1 for a user account or 2 for a bot")
	}
}

func setupMarker() (string, error) {
	var value [5]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create setup marker: %w", err)
	}
	encoded := strings.ToUpper(hex.EncodeToString(value[:]))
	return "send-cli setup " + encoded[:5] + "-" + encoded[5:], nil
}

func receiverName(receiver config.Receiver) string {
	if receiver.Title != "" && receiver.Username != "" {
		return fmt.Sprintf("%s (@%s)", receiver.Title, receiver.Username)
	}
	if receiver.Title != "" {
		return receiver.Title
	}
	if receiver.Username != "" {
		return "@" + receiver.Username
	}
	return fmt.Sprintf("%s %d", receiver.Kind, receiver.ID)
}

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List channels and setup status",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			store, err := config.NewStore()
			if err != nil {
				return err
			}
			cfg, err := store.Load()
			if err != nil {
				return err
			}
			status, receiver := "not set up", "—"
			if cfg.Telegram.Ready() {
				status = "ready (" + cfg.Telegram.Mode + ")"
				receiver = receiverName(cfg.Telegram.Receiver)
			}
			writer := command.OutOrStdout()
			table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "CHANNEL\tALIASES\tSTATUS\tDEFAULT RECEIVER")
			fmt.Fprintf(table, "telegram\ttg\t%s\t%s\n", status, receiver)
			fmt.Fprintln(table, "whatsapp\twa\tnot implemented\t—")
			fmt.Fprintln(table, "adb\t—\tnot implemented\t—")
			fmt.Fprintln(table, "ssh\t—\tnot implemented\t—")
			fmt.Fprintln(table, "http\t—\tnot implemented\t—")
			fmt.Fprintln(table, "lan\t—\tnot implemented\t—")
			return table.Flush()
		},
	}
}
