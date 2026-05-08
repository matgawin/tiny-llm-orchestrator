package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"tiny-llm-orchestrator/orc/internal/progress"
)

func executeProgress(args []string, stdout, stderr io.Writer) error {
	message, help, err := parseProgressMessage(args)
	if help {
		return printProgressHelp(stdout)
	}
	if err != nil {
		return progressFlagError(stderr, err)
	}
	if err := progress.ValidateMessage(message); err != nil {
		return progressFlagError(stderr, err)
	}
	socketPath := os.Getenv("ORC_PROGRESS_SOCKET")
	if socketPath == "" {
		_, err := fmt.Fprintln(stderr, "orc progress: live progress channel unavailable: ORC_PROGRESS_SOCKET is not set")
		return err
	}
	req := progress.Request{
		RunID:     os.Getenv("ORC_RUN_ID"),
		StepID:    os.Getenv("ORC_STEP_ID"),
		AttemptID: os.Getenv("ORC_ATTEMPT_ID"),
		Token:     os.Getenv("ORC_PROGRESS_TOKEN"),
		Message:   message,
	}
	if req.RunID == "" || req.StepID == "" || req.AttemptID == "" || req.Token == "" {
		return progressFlagError(stderr, errors.New("ORC_RUN_ID, ORC_STEP_ID, ORC_ATTEMPT_ID, and ORC_PROGRESS_TOKEN must be set when ORC_PROGRESS_SOCKET is set"))
	}
	resp, err := progress.Send(socketPath, req)
	if err != nil {
		if errors.Is(err, progress.ErrUnavailable) {
			_, writeErr := fmt.Fprintf(stderr, "orc progress: live progress channel unavailable: %v\n", err)
			return writeErr
		}
		return progressFlagError(stderr, err)
	}
	switch resp.Status {
	case progress.StatusAccepted:
		return nil
	case progress.StatusDropped:
		_, err := fmt.Fprintln(stderr, "orc progress: live progress update was rate-limited and dropped")
		return err
	case progress.StatusRejected:
		if resp.Error == "" {
			resp.Error = "progress listener rejected the update"
		}
		return progressFlagError(stderr, errors.New(resp.Error))
	default:
		return progressFlagError(stderr, fmt.Errorf("progress listener returned unknown status %q", resp.Status))
	}
}

func parseProgressMessage(args []string) (message string, help bool, err error) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == helpFlag) {
		return "", true, nil
	}
	if len(args) == 0 {
		return "", false, errors.New("requires <message>")
	}
	words := make([]string, 0, len(args))
	allowFlags := false
	for _, arg := range args {
		if !allowFlags && arg == "--" {
			allowFlags = true
			continue
		}
		if !allowFlags && strings.HasPrefix(arg, "-") {
			return "", false, fmt.Errorf("unknown flag %q; use -- before a message word that starts with '-'", arg)
		}
		words = append(words, arg)
	}
	if len(words) == 0 {
		return "", false, errors.New("requires <message>")
	}
	return strings.Join(words, " "), false, nil
}

func progressFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s progress: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printProgressHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}
