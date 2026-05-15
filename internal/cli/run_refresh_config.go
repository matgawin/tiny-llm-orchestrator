package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runconfigrefresh"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func executeRunRefreshConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		if _, err := fmt.Fprintf(stderr, "%s run refresh-config: requires <run-id>\n", appName); err != nil {
			return fmt.Errorf("execute run refresh config: %w", err)
		}
		return stableerr.Errorf("run refresh-config requires run id")
	}
	result, err := runconfigrefresh.Refresh(context.Background(), runconfigrefresh.Options{
		Root:   ".",
		RunID:  args[0],
		Source: "cli",
		Env:    os.Environ(),
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run refresh-config: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute run refresh config: %w", writeErr)
		}
		return fmt.Errorf("execute run refresh config: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "refreshed run %s config %s -> %s\nmanifest %s:%s\n", result.RunID, result.OldVersionDir, result.NewVersionDir, result.ManifestHashAlgorithm, result.ManifestHash)
	if err != nil {
		return fmt.Errorf("execute run refresh config: %w", err)
	}
	return nil
}
