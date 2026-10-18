package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"send-cli/internal/config"
	"send-cli/internal/lockfile"
	"send-cli/internal/provision"
	"send-cli/internal/whatsapp"
)

func newDeviceCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "device", Short: "Provision another personal device from this one",
		Long: "Transfer Telegram setup and a WhatsApp linked session over a one-time encrypted LAN connection. " +
			"The configured device must remain running until the new device joins.",
	}
	command.AddCommand(newDeviceOfferCommand(), newDeviceJoinCommand())
	return command
}

func newDeviceOfferCommand() *cobra.Command {
	var bind string
	var port int
	var timeout time.Duration
	command := &cobra.Command{
		Use: "offer", Short: "Offer this device's setup to another device", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return errors.New("provisioning timeout must be greater than zero")
			}
			store, err := config.NewStore()
			if err != nil {
				return err
			}
			cfg, err := store.Load()
			if err != nil {
				return err
			}
			sessionPath, err := whatsapp.SessionPath()
			if err != nil {
				return err
			}
			sessionLock, err := lockfile.Acquire(filepath.Join(filepath.Dir(sessionPath), "whatsapp.lock"))
			if errors.Is(err, lockfile.ErrLocked) {
				return errors.New("close the other send instance using WhatsApp before provisioning")
			}
			if err != nil {
				return fmt.Errorf("protect WhatsApp session: %w", err)
			}
			defer sessionLock.Release()
			snapshot, err := whatsapp.ExportSession(command.Context(), sessionPath)
			if err != nil {
				return err
			}
			telegram := cfg.Telegram
			if !telegram.Ready() {
				telegram = config.TelegramConfig{}
			}
			telegram.BotTokenProtected = "" // DPAPI material belongs to the source Windows user.
			telegram.UpdateOffset = 0
			telegram.UpdateOffsetInitialized = false
			bundle := provision.Bundle{Telegram: telegram, WhatsAppSession: snapshot}
			if len(snapshot) > 0 {
				bundle.WhatsApp = cfg.WhatsApp
			}
			offerCtx, cancel := context.WithTimeout(command.Context(), timeout)
			defer cancel()
			writer := command.OutOrStdout()
			err = provision.Offer(offerCtx, bundle, provision.OfferOptions{
				Bind: bind, Port: port,
				Ready: func(address, code string) {
					fmt.Fprintf(writer, "On the new device, run: send device join %s\n", address)
					fmt.Fprintf(writer, "Pairing code: %s\n", code)
					fmt.Fprintf(writer, "Expires in %s or after one use. Press Ctrl+C to cancel.\n", timeout)
					if len(snapshot) > 0 {
						fmt.Fprintln(writer, "WhatsApp session included. This is a copy of one linked identity; using either copy can cause them to diverge and require relinking.")
					}
				},
			})
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.New("pairing code expired; run send device offer again")
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(writer, "Provisioning data sent to the new device.")
			return nil
		},
	}
	command.Flags().StringVar(&bind, "bind", "0.0.0.0", "local interface to listen on")
	command.Flags().IntVar(&port, "port", 0, "local TCP port (0 chooses an available port)")
	command.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "how long the pairing code remains valid")
	return command
}

func newDeviceJoinCommand() *cobra.Command {
	return &cobra.Command{
		Use: "join <host:port>", Short: "Receive setup from a configured device", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := config.NewStore()
			if err != nil {
				return err
			}
			cfg, err := store.Load()
			if err != nil {
				return err
			}
			code, err := readPairingCode(command.InOrStdin(), command.OutOrStdout())
			if err != nil {
				return err
			}
			bundle, err := provision.Join(command.Context(), args[0], code)
			if err != nil {
				return err
			}
			sessionPath, err := whatsapp.SessionPath()
			if err != nil {
				return err
			}
			sessionLock, err := lockfile.Acquire(filepath.Join(filepath.Dir(sessionPath), "whatsapp.lock"))
			if errors.Is(err, lockfile.ErrLocked) {
				return errors.New("close the other send instance using WhatsApp before provisioning")
			}
			if err != nil {
				return fmt.Errorf("protect WhatsApp session: %w", err)
			}
			defer sessionLock.Release()
			if bundle.Telegram.Ready() && cfg.Telegram.Ready() {
				return errors.New("Telegram is already configured on this device; existing setup was not replaced")
			}
			if len(bundle.WhatsAppSession) > 0 {
				if _, err := os.Stat(sessionPath); err == nil {
					return errors.New("a local WhatsApp session already exists; existing setup was not replaced")
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if err := whatsapp.InstallSession(command.Context(), sessionPath, bundle.WhatsAppSession); err != nil {
					return err
				}
			}
			if bundle.Telegram.Ready() {
				cfg.Telegram = bundle.Telegram
			}
			if len(bundle.WhatsAppSession) > 0 {
				cfg.WhatsApp = bundle.WhatsApp
			}
			if err := store.Save(cfg); err != nil {
				if len(bundle.WhatsAppSession) > 0 {
					return errors.Join(err, whatsapp.RemoveInstalledSession(sessionPath))
				}
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "Device provisioned. Run send doctor to check the imported setup.")
			return nil
		},
	}
}

func readPairingCode(input io.Reader, output io.Writer) (string, error) {
	fmt.Fprint(output, "Pairing code: ")
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(output)
		return strings.TrimSpace(string(value)), err
	}
	value, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
