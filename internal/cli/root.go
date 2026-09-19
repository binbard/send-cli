package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"send-cli/internal/adbchannel"
	"send-cli/internal/config"
	"send-cli/internal/httpchannel"
	"send-cli/internal/lanchannel"
	"send-cli/internal/lockfile"
	"send-cli/internal/sshchannel"
	tgservice "send-cli/internal/telegram"
	"send-cli/internal/ui"
	"send-cli/internal/whatsapp"
)

var version = "0.1.0"

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
	root.AddCommand(newHTTPCommand())
	root.AddCommand(newADBCommand())
	root.AddCommand(newSSHCommand())
	root.AddCommand(newWhatsAppCommand())
	root.AddCommand(newLANCommand())
	root.AddCommand(newListCommand())
	root.AddCommand(newDoctorCommand())
	return root
}

func newLANCommand() *cobra.Command {
	var message, target, token string
	var list bool
	var discoveryPort int
	var discoveryTimeout, timeout time.Duration
	command := &cobra.Command{
		Use: "lan [files...]", Short: "Discover and send to send-cli peers on the LAN", Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if list {
				peers, err := lanchannel.Discover(command.Context(), discoveryPort, discoveryTimeout)
				if err != nil {
					return err
				}
				if len(peers) == 0 {
					fmt.Fprintln(command.OutOrStdout(), "No send-cli LAN peers found.")
					return nil
				}
				for _, peer := range peers {
					fmt.Fprintf(command.OutOrStdout(), "%s\t%s\tauth required\n", peer.Name, peer.URL)
				}
				return nil
			}
			endpoint, err := lanchannel.ResolveTarget(command.Context(), target, discoveryPort, discoveryTimeout)
			if err != nil {
				return err
			}
			if err := lanchannel.Send(command.Context(), endpoint, token, message, args, timeout); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Sent %d file(s) to %s.\n", len(args), target)
			return nil
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "message sent with the transfer")
	command.Flags().StringVar(&target, "to", "", "discovered peer name or host[:port]")
	command.Flags().StringVar(&token, "token", os.Getenv("SEND_LAN_TOKEN"), "receiver token (or SEND_LAN_TOKEN)")
	command.Flags().BoolVar(&list, "list", false, "discover and list send-cli peers")
	command.Flags().IntVar(&discoveryPort, "discovery-port", lanchannel.DefaultDiscoveryPort, "UDP discovery port")
	command.Flags().DurationVar(&discoveryTimeout, "discovery-timeout", time.Second, "LAN discovery duration")
	command.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "transfer timeout")
	command.AddCommand(newLANServeCommand())
	return command
}

func newLANServeCommand() *cobra.Command {
	var name, directory, address, token string
	var port, discoveryPort int
	command := &cobra.Command{
		Use: "serve", Short: "Receive authenticated transfers from LAN peers", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if token == "" {
				generated, err := randomHex(12)
				if err != nil {
					return err
				}
				token = generated
			}
			writer := command.OutOrStdout()
			fmt.Fprintf(writer, "LAN receiver token: %s\n", token)
			fmt.Fprintln(writer, "Share this token only with the sender. LAN traffic is not encrypted.")
			receiver := &lanchannel.Receiver{
				Directory: directory, Token: token,
				OnMessage: func(message string) { fmt.Fprintf(writer, "R> %s\n", message) },
				OnFiles: func(files []string) {
					for _, file := range files {
						fmt.Fprintf(writer, "Saved %s\n", file)
					}
				},
			}
			return lanchannel.Serve(command.Context(), lanchannel.ServeOptions{
				Name: name, Address: address, Port: port, DiscoveryPort: discoveryPort, Receiver: receiver,
				Ready: func(bound string) {
					fmt.Fprintf(writer, "LAN receiver %s listening on %s. Press Ctrl+C to stop.\n", name, bound)
				},
			})
		},
	}
	command.Flags().StringVar(&name, "name", defaultHostName(), "peer name advertised on the LAN")
	command.Flags().StringVar(&directory, "directory", ".", "directory for received files")
	command.Flags().StringVar(&address, "address", "0.0.0.0", "local address to bind")
	command.Flags().StringVar(&token, "token", os.Getenv("SEND_LAN_TOKEN"), "receiver token; generated when omitted")
	command.Flags().IntVar(&port, "port", lanchannel.DefaultTransferPort, "TCP transfer port")
	command.Flags().IntVar(&discoveryPort, "discovery-port", lanchannel.DefaultDiscoveryPort, "UDP discovery port")
	return command
}

func randomHex(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(value)), nil
}

func defaultHostName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "send-cli-peer"
	}
	return name
}

func newWhatsAppCommand() *cobra.Command {
	var (
		message      string
		recipient    string
		setup        bool
		logout       bool
		askDownloads bool
		timeout      time.Duration
	)
	command := &cobra.Command{
		Use:     "whatsapp [files...]",
		Aliases: []string{"wa"},
		Short:   "Send with a linked WhatsApp user account",
		Long: "Send messages and files through a linked WhatsApp user account. Running send wa without a message or files opens the interactive chat.\n\n" +
			"After a successful send, the recipient is remembered as the default for the next send. " +
			"Use --to to select and remember a different recipient. SEND_WHATSAPP_TO overrides the remembered default.",
		Example: "  send wa --to +919876543210 -m \"hello\"\n" +
			"  send wa\n" +
			"  send wa -m \"uses the last successful recipient\"\n" +
			"  send wa --to +919876543210 photo.jpg",
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			sessionPath, err := whatsapp.SessionPath()
			if err != nil {
				return err
			}
			sessionLock, err := lockfile.Acquire(filepath.Join(filepath.Dir(sessionPath), "whatsapp.lock"))
			if errors.Is(err, lockfile.ErrLocked) {
				return errors.New("another send instance is using the WhatsApp session")
			}
			if err != nil {
				return fmt.Errorf("protect WhatsApp session: %w", err)
			}
			defer sessionLock.Release()

			if logout {
				linked, err := whatsapp.IsLinked(command.Context(), sessionPath)
				if err != nil {
					return fmt.Errorf("inspect WhatsApp session: %w", err)
				}
				if !linked {
					return errors.New("WhatsApp is not linked on this computer")
				}
			}
			client, err := whatsapp.Open(command.Context(), sessionPath, command.OutOrStdout())
			if err != nil {
				return err
			}
			defer client.Close()
			if logout {
				if err := client.Logout(command.Context()); err != nil {
					return err
				}
				fmt.Fprintln(command.OutOrStdout(), "WhatsApp device unlinked and local session removed.")
				return nil
			}

			hasPayload := len(args) > 0 || command.Flags().Changed("message")
			interactive := !hasPayload && !setup
			if interactive && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))) {
				return errors.New("interactive mode requires a terminal; provide files or use -m")
			}
			if !client.Linked() && !term.IsTerminal(int(os.Stdout.Fd())) {
				return errors.New("WhatsApp setup requires a terminal to display the QR code")
			}
			if setup || interactive {
				if err := client.Connect(command.Context()); err != nil {
					return err
				}
			}
			if setup && !hasPayload {
				if client.Linked() {
					fmt.Fprintln(command.OutOrStdout(), "WhatsApp is linked and ready.")
					return nil
				}
				return errors.New("WhatsApp setup did not complete")
			}
			store, err := config.NewStore()
			if err != nil {
				return err
			}
			recipient, err = whatsappRecipient(store, recipient)
			if err != nil {
				return err
			}
			if recipient == "" {
				return errors.New("WhatsApp recipient is required; use --to once and it will be remembered")
			}
			if interactive {
				cfg, err := store.Load()
				if err != nil {
					return err
				}
				if askDownloads {
					cfg.WhatsApp.AutoDownload = false
					if err := store.Save(cfg); err != nil {
						return err
					}
				}
				return client.Chat(command.Context(), recipient, func(session tgservice.Session, receiverLabel string) error {
					session = &rememberingSession{
						Session:       session,
						remember:      func() error { return rememberWhatsAppRecipient(store, recipient) },
						warningOutput: command.ErrOrStderr(),
					}
					return ui.Run(command.Context(), session, ui.Options{
						Channel:      "WhatsApp",
						Receiver:     receiverLabel,
						AutoDownload: cfg.WhatsApp.AutoDownload,
						SetAutoDownload: func(enabled bool) error {
							return store.Update(func(latest *config.Config) error {
								latest.WhatsApp.AutoDownload = enabled
								return nil
							})
						},
					})
				})
			}

			result, err := client.Send(command.Context(), whatsapp.Request{
				Recipient: recipient, Message: message, Files: args, Timeout: timeout,
			})
			if err != nil {
				return err
			}
			if err := rememberWhatsAppRecipient(store, recipient); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "warning: message was sent, but the WhatsApp recipient could not be remembered: %v\n", err)
			}
			fmt.Fprintf(command.OutOrStdout(), "Sent %d WhatsApp message(s), including %d file(s), to %s.\n", result.Messages, result.Files, recipient)
			return nil
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "message or first file caption")
	command.Flags().StringVar(&recipient, "to", os.Getenv("SEND_WHATSAPP_TO"), "recipient phone number; remembers the last successful recipient")
	command.Flags().BoolVar(&setup, "setup", false, "link a WhatsApp account by QR code")
	command.Flags().BoolVar(&logout, "logout", false, "unlink WhatsApp and remove the local session")
	command.Flags().BoolVar(&askDownloads, "ask-downloads", false, "ask before downloading incoming files again")
	command.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "overall request timeout")
	command.MarkFlagsMutuallyExclusive("setup", "logout")
	return command
}

type rememberingSession struct {
	tgservice.Session
	remember      func() error
	warningOutput io.Writer
}

func (s *rememberingSession) Send(ctx context.Context, text string, files []string) error {
	if err := s.Session.Send(ctx, text, files); err != nil {
		return err
	}
	if s.remember != nil {
		if err := s.remember(); err != nil {
			fmt.Fprintf(s.warningOutput, "warning: message was sent, but the WhatsApp recipient could not be remembered: %v\n", err)
		}
	}
	return nil
}

func whatsappRecipient(store *config.Store, supplied string) (string, error) {
	if recipient := strings.TrimSpace(supplied); recipient != "" {
		return recipient, nil
	}
	cfg, err := store.Load()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.WhatsApp.DefaultRecipient), nil
}

func rememberWhatsAppRecipient(store *config.Store, recipient string) error {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return nil
	}
	return store.Update(func(cfg *config.Config) error {
		cfg.WhatsApp.DefaultRecipient = recipient
		return nil
	})
}

func newSSHCommand() *cobra.Command {
	var (
		destination string
		port        int
		identity    string
		executable  string
		options     []string
	)
	command := &cobra.Command{
		Use:   "ssh <user@host> [files...]",
		Short: "Send files over SSH with SCP",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client := sshchannel.NewClient(executable)
			result, err := client.Send(command.Context(), sshchannel.Request{
				Target: args[0], Destination: destination, Files: args[1:], Port: port,
				Identity: identity, Options: options,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Sent %d file(s) to %s:%s.\n", result.Sent, args[0], destination)
			return nil
		},
	}
	command.Flags().StringVar(&destination, "to", sshchannel.DefaultDestination, "remote destination directory or path")
	command.Flags().IntVarP(&port, "port", "p", 0, "SSH port")
	command.Flags().StringVarP(&identity, "identity", "i", "", "SSH private key path")
	command.Flags().StringVar(&executable, "scp-path", "scp", "path to the scp executable")
	command.Flags().StringArrayVarP(&options, "ssh-option", "o", nil, "OpenSSH option in Key=Value form (repeatable)")
	return command
}

func newADBCommand() *cobra.Command {
	var (
		serial      string
		destination string
		executable  string
		list        bool
	)
	command := &cobra.Command{
		Use:   "adb [files...]",
		Short: "Send files to an Android device with ADB",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			client := adbchannel.NewClient(executable)
			if list {
				devices, err := client.ListDevices(command.Context())
				if err != nil {
					return err
				}
				if len(devices) == 0 {
					fmt.Fprintln(command.OutOrStdout(), "No ADB devices found.")
					return nil
				}
				for _, device := range devices {
					fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\n", device.Serial, device.State, device.Info)
				}
				return nil
			}
			result, err := client.Send(command.Context(), serial, destination, args)
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Sent %d file(s) to ADB device %s.\n", result.Sent, result.Serial)
			return nil
		},
	}
	command.Flags().StringVarP(&serial, "serial", "s", "", "ADB device serial (required when multiple devices are connected)")
	command.Flags().StringVar(&destination, "to", adbchannel.DefaultDestination, "destination directory or path on the device")
	command.Flags().StringVar(&executable, "adb-path", "adb", "path to the adb executable")
	command.Flags().BoolVar(&list, "list", false, "list connected ADB devices")
	return command
}

func newHTTPCommand() *cobra.Command {
	var (
		message      string
		method       string
		headers      []string
		timeout      time.Duration
		address      string
		port         int
		askDownloads bool
	)
	command := &cobra.Command{
		Use:   "http [files...]",
		Short: "Share files and messages in a browser over the LAN",
		Long: "Start a session-scoped web app for sharing files and messages over the local network. " +
			"When --port is omitted, the last successful port is tried first, beginning with 8080, and the next available port is used. " +
			"The terminal opens the same interactive composer used by Telegram and WhatsApp.\n\n" +
			"For compatibility, a first argument beginning with http:// or https:// sends an outbound HTTP request. " +
			"The explicit form is send http post <url> [files...].",
		Example: "  send http report.pdf photo.jpg\n" +
			"  send http\n" +
			"  send http --port 8080 -m \"Files for the team\"\n" +
			"  send http post https://example.test/upload report.pdf",
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) > 0 && isHTTPURL(args[0]) {
				return runHTTPPost(command, args[0], args[1:], message, method, headers, timeout)
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				return errors.New("HTTP sharing requires a terminal; use 'send http post <url>' for an outbound request")
			}
			store, err := config.NewStore()
			if err != nil {
				return err
			}
			cfg, err := store.Load()
			if err != nil {
				return err
			}
			if askDownloads {
				cfg.HTTP.AutoDownload = false
				if err := store.Save(cfg); err != nil {
					return err
				}
			}
			incrementPort := !command.Flags().Changed("port")
			if incrementPort && cfg.HTTP.LastPort >= 1 && cfg.HTTP.LastPort <= 65535 {
				port = cfg.HTTP.LastPort
			}
			return httpchannel.Serve(command.Context(), httpchannel.ShareOptions{
				Address: address, Port: port, IncrementPort: incrementPort, InitialMessage: message, Files: args,
			}, func(ctx context.Context, session tgservice.Session, info httpchannel.ShareInfo) error {
				if err := store.Update(func(latest *config.Config) error {
					latest.HTTP.LastPort = info.Port
					return nil
				}); err != nil {
					return err
				}
				return ui.Run(ctx, session, ui.Options{
					Channel: "HTTP", Receiver: info.URL, QRCode: info.URL, AutoDownload: cfg.HTTP.AutoDownload,
					SetAutoDownload: func(enabled bool) error {
						return store.Update(func(latest *config.Config) error {
							latest.HTTP.AutoDownload = enabled
							return nil
						})
					},
				})
			})
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "initial shared message")
	command.Flags().StringVar(&address, "bind", "0.0.0.0", "local address to bind")
	command.Flags().IntVar(&port, "port", httpchannel.DefaultPort, "TCP port; omitted uses the last successful port, initially 8080")
	command.Flags().BoolVar(&askDownloads, "ask-downloads", false, "ask before downloading incoming files again")
	command.Flags().StringVar(&method, "method", http.MethodPost, "outbound compatibility mode HTTP method")
	command.Flags().StringArrayVarP(&headers, "header", "H", nil, "outbound compatibility request header")
	command.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "outbound compatibility request timeout")
	_ = command.Flags().MarkHidden("method")
	_ = command.Flags().MarkHidden("header")
	_ = command.Flags().MarkHidden("timeout")
	command.AddCommand(newHTTPPostCommand())
	return command
}

func newHTTPPostCommand() *cobra.Command {
	var message, method string
	var headers []string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "post <url> [files...]",
		Short: "Send files or a message to an HTTP endpoint",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runHTTPPost(command, args[0], args[1:], message, method, headers, timeout)
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "message body or multipart message field")
	command.Flags().StringVar(&method, "method", http.MethodPost, "HTTP method")
	command.Flags().StringArrayVarP(&headers, "header", "H", nil, "request header in 'Name: value' form (repeatable)")
	command.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "request timeout")
	return command
}

func runHTTPPost(command *cobra.Command, endpoint string, files []string, message, method string, headers []string, timeout time.Duration) error {
	result, err := httpchannel.Send(command.Context(), httpchannel.Request{
		URL: endpoint, Method: method, Message: message, Files: files, Headers: headers, Timeout: timeout,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(command.OutOrStdout(), "HTTP %s succeeded (%s).\n", strings.ToUpper(method), result.Status)
	return nil
}

func isHTTPURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
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
				fmt.Fprintln(command.OutOrStdout(), "Removed local Telegram bot token and receiver configuration.")
				return nil
			}
			if askDownloads {
				cfg.Telegram.AutoDownload = false
				if err := store.Save(cfg); err != nil {
					return err
				}
			}
			if setup || !cfg.Telegram.Ready() {
				telegramConfig, err := setupTelegram(command.Context(), command.InOrStdin(), command.OutOrStdout())
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
			service := tgservice.NewBotService(cfg.Telegram.BotToken, cfg.Telegram.Receiver)
			service.ResumeUpdates(
				cfg.Telegram.UpdateOffset,
				cfg.Telegram.UpdateOffsetInitialized,
				func(offset int64) error {
					return store.Update(func(latest *config.Config) error {
						latest.Telegram.UpdateOffset = offset
						latest.Telegram.UpdateOffsetInitialized = true
						return nil
					})
				},
			)
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
			receiverLock, err := lockfile.Acquire(filepath.Join(filepath.Dir(store.Path()), "telegram-receiver.lock"))
			if errors.Is(err, lockfile.ErrLocked) {
				return errors.New("another send instance is already receiving Telegram updates; close it before starting interactive mode")
			}
			if err != nil {
				return fmt.Errorf("protect Telegram receiver: %w", err)
			}
			defer receiverLock.Release()
			return service.Run(command.Context(), true, func(session tgservice.Session) error {
				return ui.Run(command.Context(), session, ui.Options{
					Channel:      "Telegram",
					Receiver:     receiverName(cfg.Telegram.Receiver),
					AutoDownload: cfg.Telegram.AutoDownload,
					SetAutoDownload: func(enabled bool) error {
						return store.Update(func(latest *config.Config) error {
							latest.Telegram.AutoDownload = enabled
							return nil
						})
					},
				})
			})
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "message or file caption")
	command.Flags().BoolVar(&setup, "setup", false, "configure a BotFather token and select a default receiver")
	command.Flags().BoolVar(&logout, "logout", false, "remove the local Telegram bot token and configuration")
	command.Flags().BoolVar(&askDownloads, "ask-downloads", false, "ask before downloading incoming files again")
	return command
}

func setupTelegram(ctx context.Context, input io.Reader, output io.Writer) (config.TelegramConfig, error) {
	reader := bufio.NewReader(input)
	fmt.Fprintln(output, "Telegram BotFather setup")
	fmt.Fprint(output, "Bot token: ")
	var (
		token string
		err   error
	)
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
	marker, err := setupMarker()
	if err != nil {
		return config.TelegramConfig{}, err
	}
	return tgservice.SetupBot(ctx, token, marker, output)
}

func setupMarker() (string, error) {
	var value [5]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create setup marker: %w", err)
	}
	encoded := strings.ToUpper(hex.EncodeToString(value[:]))
	return "/sendcli_" + encoded, nil
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
				status = "ready (bot)"
				receiver = receiverName(cfg.Telegram.Receiver)
			}
			whatsappStatus := "not linked"
			whatsappReceiver := strings.TrimSpace(cfg.WhatsApp.DefaultRecipient)
			if whatsappReceiver == "" {
				whatsappReceiver = "--to required"
			}
			sessionPath, err := whatsapp.SessionPath()
			if err != nil {
				return err
			}
			whatsappLinked, err := whatsapp.IsLinked(command.Context(), sessionPath)
			if err != nil {
				return fmt.Errorf("inspect WhatsApp session: %w", err)
			}
			if whatsappLinked {
				whatsappStatus = "ready (user)"
			}
			writer := command.OutOrStdout()
			table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "CHANNEL\tALIASES\tSTATUS\tDEFAULT RECEIVER")
			fmt.Fprintf(table, "telegram\ttg\t%s\t%s\n", status, receiver)
			fmt.Fprintf(table, "whatsapp\twa\t%s\t%s\n", whatsappStatus, whatsappReceiver)
			fmt.Fprintln(table, "adb\t—\tready\tconnected device")
			fmt.Fprintln(table, "ssh\t—\tready\tuser@host required")
			fmt.Fprintln(table, "http\t—\tready\tprivate LAN web share")
			fmt.Fprintln(table, "lan\t—\tready\tdiscovery or --to")
			return table.Flush()
		},
	}
}
