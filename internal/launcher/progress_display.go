package launcher

import (
	"context"
	"fmt"
	"io"
	"sync"

	"tiny-llm-orchestrator/orc/internal/progress"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

type liveProgressDisplay struct {
	listener  *progress.Listener
	runID     string
	stepID    string
	attemptID string
	token     string
	done      chan struct{}
	once      sync.Once
	err       error
}

func startLiveProgress(ctx context.Context, opts Options, attempt runstore.Attempt) (*liveProgressDisplay, error) {
	token, err := progress.GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate progress token: %w", err)
	}
	listener, err := progress.NewListenerContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("start progress listener: %w", err)
	}
	if err := listener.Register(progress.Registration{
		RunID:     opts.RunID,
		StepID:    attempt.StepID,
		AttemptID: attempt.AttemptID,
		Token:     token,
	}); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("register progress attempt: %w", err)
	}
	display := &liveProgressDisplay{
		listener:  listener,
		runID:     opts.RunID,
		stepID:    attempt.StepID,
		attemptID: attempt.AttemptID,
		token:     token,
		done:      make(chan struct{}),
	}
	writer := opts.Progress
	if writer == nil {
		writer = opts.Stdout
	}
	if writer == nil {
		writer = io.Discard
	}
	go func() {
		defer close(display.done)
		for msg := range listener.Accepted() {
			_, _ = fmt.Fprintf(writer, "[%s %s] %s\n", msg.StepID, msg.AttemptID, msg.Message)
		}
	}()
	return display, nil
}

func (d *liveProgressDisplay) Env() map[string]string {
	if d == nil || d.listener == nil {
		return nil
	}
	return map[string]string{
		"ORC_PROGRESS_SOCKET": d.listener.SocketPath(),
		"ORC_PROGRESS_TOKEN":  d.token,
		"ORC_RUN_ID":          d.runID,
		"ORC_STEP_ID":         d.stepID,
		"ORC_ATTEMPT_ID":      d.attemptID,
	}
}

func (d *liveProgressDisplay) Close() error {
	if d == nil {
		return nil
	}
	d.once.Do(func() {
		if d.listener != nil {
			d.err = d.listener.Close()
		}
		if d.done != nil {
			<-d.done
		}
	})
	return d.err
}

func printLaunchResult(w io.Writer, result Result) {
	if w == nil {
		return
	}
	if result.SoftCap != nil {
		loopCap := result.SoftCap
		_, _ = fmt.Fprintf(w, "warning: workflow loop soft cap reached for workflow %s state %s at count %d (soft %d, hard %d); continue only if this attempt can break the loop or escalate\n", loopCap.Workflow, loopCap.State, loopCap.Count, loopCap.Soft, loopCap.Hard)
	}
	action := "launched"
	if result.Recovered {
		action = "recovered"
	} else if !result.Launched {
		action = "terminalized"
	}
	_, _ = fmt.Fprintf(w, "%s attempt %s\n", action, result.Attempt.AttemptID)
	_, _ = fmt.Fprintf(w, "step: %s\n", result.Attempt.StepID)
	_, _ = fmt.Fprintf(w, "agent: %s\n", result.Attempt.AgentID)
	_, _ = fmt.Fprintf(w, "result: %s/%s\n", result.Attempt.Status, result.Attempt.Result)
	if result.Log.Path != "" {
		_, _ = fmt.Fprintf(w, "log: %s\n", result.Log.Path)
	}
}
