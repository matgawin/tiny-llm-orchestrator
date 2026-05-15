package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const launchCleanupTimeout = 2 * time.Second

func finishAttemptWithCleanupContext(parent context.Context, store *runstore.Store, runID string, req runstore.FinishAttemptRequest) (runstore.Attempt, error) {
	ctx, cancel := context.WithTimeout(cleanupContext(parent), launchCleanupTimeout)
	defer cancel()
	finished, _, err := store.FinishAttemptContext(ctx, runID, req)
	if err != nil {
		return finished, fmt.Errorf("finish attempt with cleanup context: %w", err)
	}
	return finished, nil
}

func cleanupContext(parent context.Context) context.Context {
	if parent == nil {
		return context.Background()
	}
	return context.WithoutCancel(parent)
}

func finishProcessErrorAttempt(ctx context.Context, store *runstore.Store, runID, attemptID, exitState string, logRef runstore.ArtifactRef, at time.Time, causes ...error) (runstore.Attempt, error) {
	finished, finishErr := finishAttemptWithCleanupContext(ctx, store, runID, runstore.FinishAttemptRequest{
		AttemptID: attemptID,
		State:     runstore.AttemptStateProcessError,
		Status:    workflow.ReportStatusFailed,
		Result:    resultProcessError,
		ExitState: exitState,
		LogRef:    refPtr(logRef),
		Time:      at,
	})
	return finished, errors.Join(append(causes, finishErr)...)
}

func terminalLaunchResult(stdout io.Writer, result Result, err error) (Result, error) {
	if result.Attempt.AttemptID != "" {
		printLaunchResult(stdout, result)
	}
	return result, err
}

func terminalLaunchResultWithProgress(stdout io.Writer, display *liveProgressDisplay, result Result, err error) (Result, error) {
	_ = display.Close()
	return terminalLaunchResult(stdout, result, err)
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func loadLaunchContext(ctx context.Context, root, runID string) (runcontext.Context, error) {
	launchContext, err := runcontext.LoadContext(ctx, root, runID)
	if err != nil {
		return runcontext.Context{}, fmt.Errorf("load run context for %s: %w", runID, err)
	}
	return launchContext, nil
}

func newAttemptID(now time.Time, step string) (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate attempt id: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", now.UTC().Format("20060102T150405Z"), step, hex.EncodeToString(buf[:])), nil
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func refPtr(ref runstore.ArtifactRef) *runstore.ArtifactRef {
	if ref.Path == "" {
		return nil
	}
	return &ref
}
