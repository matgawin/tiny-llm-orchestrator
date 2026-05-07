package main

import (
	"os"

	"tiny-llm-orchestrator/orc/internal/cli"
)

func main() {
	if err := cli.ExecuteWithInput(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(cli.ExitCode(err))
	}
}
