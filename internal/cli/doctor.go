package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"send-cli/internal/config"
	"send-cli/internal/whatsapp"
)

type doctorCheck struct {
	Channel string
	Ready   bool
	Status  string
	Detail  string
}

func newDoctorCommand() *cobra.Command {
	var strict bool
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check channel configuration and local dependencies",
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
			sessionPath, err := whatsapp.SessionPath()
			if err != nil {
				return err
			}
			whatsappLinked, err := whatsapp.IsLinked(command.Context(), sessionPath)
			if err != nil {
				return fmt.Errorf("inspect WhatsApp session: %w", err)
			}
			checks := doctorChecks(cfg, whatsappLinked, exec.LookPath)
			writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(writer, "CHANNEL\tSTATUS\tDETAIL")
			allReady := true
			for _, check := range checks {
				fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Channel, check.Status, check.Detail)
				allReady = allReady && check.Ready
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			if strict && !allReady {
				return errors.New("one or more channels need setup; see the doctor report above")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&strict, "strict", false, "return a failure exit code when any channel needs setup")
	return command
}

func doctorChecks(
	cfg config.Config,
	whatsappLinked bool,
	lookPath func(string) (string, error),
) []doctorCheck {
	checks := make([]doctorCheck, 0, 6)
	if cfg.Telegram.Ready() {
		checks = append(checks, doctorCheck{"telegram", true, "ready", receiverName(cfg.Telegram.Receiver)})
	} else {
		checks = append(checks, doctorCheck{"telegram", false, "setup required", "run send tg --setup"})
	}

	if whatsappLinked {
		checks = append(checks, doctorCheck{"whatsapp", true, "ready", "linked user session"})
	} else {
		checks = append(checks, doctorCheck{"whatsapp", false, "setup required", "run send wa --setup"})
	}

	checks = append(checks, executableCheck("adb", "adb", lookPath))
	checks = append(checks, executableCheck("ssh", "scp", lookPath))
	checks = append(checks,
		doctorCheck{"http", true, "ready", "built in"},
		doctorCheck{"lan", true, "ready", "built in"},
	)
	return checks
}

func executableCheck(channel, executable string, lookPath func(string) (string, error)) doctorCheck {
	path, err := lookPath(executable)
	if err != nil {
		return doctorCheck{channel, false, "dependency missing", executable + " not found in PATH"}
	}
	return doctorCheck{channel, true, "ready", path}
}
