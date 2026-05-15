package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

const sandboxWorkerGuardGuidance = "sandbox.require_for_workers is enabled; start the orchestrator with `orc sandbox run` so worker launches inherit the sandbox"

func enforceWorkerSandboxGuard(root string, sandboxConfig *config.SandboxConfig) error {
	if sandboxConfig == nil || !sandboxConfig.RequireForWorkers {
		return nil
	}
	if err := verifyWorkerRepoSandbox(root); err != nil {
		return workerSandboxGuardError(err)
	}
	return nil
}

func verifyWorkerRepoSandbox(root string) error {
	if os.Getenv("ORC_SANDBOX") != "1" {
		return errMissingWorkerSandboxMarker
	}
	markerRoot := os.Getenv("ORC_SANDBOX_ROOT")
	if markerRoot == "" {
		return errMissingWorkerSandboxRoot
	}
	currentRoot, err := canonicalPath(root)
	if err != nil {
		return fmt.Errorf("resolve current repo root for sandbox worker guard: %w", err)
	}
	sandboxRoot, err := canonicalPath(markerRoot)
	if err != nil {
		return workerSandboxRootInvalidError{markerRoot: markerRoot, err: err}
	}
	if sandboxRoot != currentRoot {
		return workerSandboxRootMismatchError{sandboxRoot: sandboxRoot, currentRoot: currentRoot}
	}
	return nil
}

func workerSandboxGuardError(err error) error {
	switch {
	case errors.Is(err, errMissingWorkerSandboxMarker):
		return stableerr.New(sandboxWorkerGuardGuidance + " (missing ORC_SANDBOX=1)")
	case errors.Is(err, errMissingWorkerSandboxRoot):
		return stableerr.New(sandboxWorkerGuardGuidance + " (missing ORC_SANDBOX_ROOT)")
	default:
		var invalid workerSandboxRootInvalidError
		if errors.As(err, &invalid) {
			return fmt.Errorf("%s; ORC_SANDBOX_ROOT %q is invalid: %w", sandboxWorkerGuardGuidance, invalid.markerRoot, invalid.err)
		}
		var mismatch workerSandboxRootMismatchError
		if errors.As(err, &mismatch) {
			return stableerr.Errorf("%s; ORC_SANDBOX_ROOT %q does not match current repo root %q", sandboxWorkerGuardGuidance, mismatch.sandboxRoot, mismatch.currentRoot)
		}
		return err
	}
}

var (
	errMissingWorkerSandboxMarker = errors.New("missing ORC_SANDBOX=1")
	errMissingWorkerSandboxRoot   = errors.New("missing ORC_SANDBOX_ROOT")
)

type workerSandboxRootInvalidError struct {
	markerRoot string
	err        error
}

func (e workerSandboxRootInvalidError) Error() string {
	return fmt.Sprintf("ORC_SANDBOX_ROOT %q is invalid: %v", e.markerRoot, e.err)
}

func (e workerSandboxRootInvalidError) Unwrap() error {
	return e.err
}

type workerSandboxRootMismatchError struct {
	sandboxRoot string
	currentRoot string
}

func (e workerSandboxRootMismatchError) Error() string {
	return fmt.Sprintf("ORC_SANDBOX_ROOT %q does not match current repo root %q", e.sandboxRoot, e.currentRoot)
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
