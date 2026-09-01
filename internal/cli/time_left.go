package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/attemptdeadline"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

type timeLeftOptions struct {
	RunID     string
	AttemptID string
	Root      string
	JSON      bool
}

type timeLeftJSON struct {
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id"`
	AgentID   string `json:"agent_id"`
	AttemptID string `json:"attempt_id"`
	StartedAt string `json:"started_at"`
	Deadline  string `json:"deadline"`
	Elapsed   string `json:"elapsed"`
	Remaining string `json:"remaining"`
	Timeout   string `json:"timeout"`
	Phase     string `json:"phase"`
	Action    string `json:"action"`
}

func newTimeLeftCommand(stdout, stderr io.Writer) *cobra.Command {
	var opts timeLeftOptions

	cmd := &cobra.Command{
		Use:           commandTimeLeft,
		Short:         "Show active attempt deadline guidance",
		Long:          timeLeftHelpLong(),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := executeTimeLeft(cmd.Context(), opts, stdout); err != nil {
				if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
					return fmt.Errorf("time-left error: %w", writeErr)
				}

				return err
			}

			return nil
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.JSON, "json", false, "Print machine-readable JSON")
	flags.StringVar(&opts.RunID, "run", "", "Run id for explicit lookup")
	flags.StringVar(&opts.AttemptID, "attempt", "", "Attempt id for explicit lookup")
	flags.StringVar(&opts.Root, "root", "", "Project root for run store lookup")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if _, writeErr := fmt.Fprintf(stderr, "%s %s: %v\n\n", appName, commandTimeLeft, err); writeErr != nil {
			return fmt.Errorf("time-left flag error: %w", writeErr)
		}

		cmd.SetOut(stderr)

		if helpErr := cmd.Usage(); helpErr != nil {
			return fmt.Errorf("time-left flag error: %w", helpErr)
		}

		return fmt.Errorf("%s %s: %w", appName, commandTimeLeft, err)
	})

	return cmd
}

func executeTimeLeft(ctx context.Context, opts timeLeftOptions, stdout io.Writer) error {
	runID := opts.RunID
	attemptID := opts.AttemptID

	if runID == "" && attemptID == "" {
		runID = os.Getenv("ORC_RUN_ID")
		attemptID = os.Getenv("ORC_ATTEMPT_ID")
	}

	if runID == "" || attemptID == "" {
		return errors.New("orc time-left requires ORC_RUN_ID and ORC_ATTEMPT_ID, or --run and --attempt")
	}

	if (opts.RunID == "") != (opts.AttemptID == "") {
		return errors.New("orc time-left requires --run and --attempt together")
	}

	root, err := timeLeftRoot(opts.Root)
	if err != nil {
		return fmt.Errorf("orc time-left: %w", err)
	}

	store, err := runstore.Open(root)
	if err != nil {
		return fmt.Errorf("orc time-left: %w", err)
	}

	run, err := store.LoadContext(ctx, runID)
	if err != nil {
		return fmt.Errorf("orc time-left: %w", err)
	}

	attempt, ok := findAttempt(run.Status.Attempts, attemptID)
	if !ok {
		return fmt.Errorf("orc time-left: attempt %q not found in run %q", attemptID, runID)
	}

	if envStepID := os.Getenv("ORC_STEP_ID"); opts.RunID == "" && envStepID != "" && envStepID != attempt.StepID {
		return fmt.Errorf("orc time-left: ORC_STEP_ID %q does not match attempt step %q", envStepID, attempt.StepID)
	}

	guidance, err := attemptdeadline.FromAttempt(attempt, time.Now())
	if err != nil {
		return fmt.Errorf("orc time-left: %w", err)
	}

	if opts.JSON {
		return writeTimeLeftJSON(stdout, guidance)
	}

	return writeTimeLeftHuman(stdout, guidance)
}

func findAttempt(attempts []runstore.Attempt, attemptID string) (runstore.Attempt, bool) {
	for _, attempt := range attempts {
		if attempt.AttemptID == attemptID {
			return attempt, true
		}
	}

	return runstore.Attempt{}, false
}

func writeTimeLeftHuman(stdout io.Writer, guidance attemptdeadline.Guidance) error {
	_, err := fmt.Fprintf(stdout, "run: %s\nstep: %s\nagent: %s\nattempt: %s\nstarted_at: %s\ndeadline: %s\nelapsed: %s\nremaining: %s\ntimeout: %s\nphase: %s\naction: %s\n",
		guidance.RunID,
		guidance.StepID,
		guidance.AgentID,
		guidance.AttemptID,
		attemptdeadline.FormatTime(guidance.StartedAt),
		attemptdeadline.FormatTime(guidance.Deadline),
		guidance.Elapsed.String(),
		guidance.Remaining.String(),
		guidance.TimeoutRaw,
		guidance.Phase,
		guidance.Action,
	)
	if err != nil {
		return fmt.Errorf("write time-left output: %w", err)
	}

	return nil
}

func writeTimeLeftJSON(stdout io.Writer, guidance attemptdeadline.Guidance) error {
	payload := timeLeftJSON{
		RunID:     guidance.RunID,
		StepID:    guidance.StepID,
		AgentID:   guidance.AgentID,
		AttemptID: guidance.AttemptID,
		StartedAt: attemptdeadline.FormatTime(guidance.StartedAt),
		Deadline:  attemptdeadline.FormatTime(guidance.Deadline),
		Elapsed:   guidance.Elapsed.String(),
		Remaining: guidance.Remaining.String(),
		Timeout:   guidance.TimeoutRaw,
		Phase:     guidance.Phase,
		Action:    guidance.Action,
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode time-left output: %w", err)
	}

	if _, err := fmt.Fprintf(stdout, "%s\n", encoded); err != nil {
		return fmt.Errorf("write time-left output: %w", err)
	}

	return nil
}

func timeLeftRoot(flagRoot string) (string, error) {
	if flagRoot != "" {
		return flagRoot, nil
	}

	if envRoot := os.Getenv("ORC_PROJECT_ROOT"); envRoot != "" {
		return envRoot, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}

	return wd, nil
}
