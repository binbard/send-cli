package main

import (
	"fmt"
	"os"

	"send-cli/internal/cli"
	"send-cli/internal/termux"
)

func main() {
	termux.Configure()
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "send:", err)
		os.Exit(1)
	}
}
