package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
)

const (
	markerSandbox     = "ORC_SANDBOX=1"
	markerSandboxRoot = "ORC_SANDBOX_ROOT="
)

// ExitError reports the sandboxed command exit status.
type ExitError struct {
	Code int
	Err  error
}

func (e ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("sandbox command exited with status %d", e.Code)
	}
	return e.Err.Error()
}

func (e ExitError) Unwrap() error {
	return e.Err
}

// Options declares the process execution boundary for orc sandbox run.
type Options struct {
	Root   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	RuntimeGOOS string
	LookPath    func(string) (string, error)
	PathExists  func(string) bool
}

// BwrapSpec is the resolved bubblewrap process invocation.
type BwrapSpec struct {
	Path string
	Args []string
	CWD  string
	Env  []string
}

// Run loads project sandbox config, starts bwrap, and waits for it to finish.
func Run(ctx context.Context, opts Options) error {
	project, err := config.Load(opts.Root)
	if err != nil {
		return fmt.Errorf("load project config: %w", err)
	}
	spec, err := BuildSpec(project, opts)
	if err != nil {
		return err
	}
	return runSpec(ctx, spec, opts)
}

// BuildSpec returns the bwrap invocation for a validated project config.
func BuildSpec(project *config.Project, opts Options) (BwrapSpec, error) {
	if project == nil {
		return BwrapSpec{}, errors.New("project config is required")
	}
	sandboxConfig := project.Config.Sandbox
	if sandboxConfig == nil {
		return BwrapSpec{}, errors.New("sandbox config is required; declare sandbox.command.argv in .orc/config.yaml")
	}
	goos := opts.RuntimeGOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return BwrapSpec{}, fmt.Errorf("orc sandbox run requires Linux and the system bwrap binary; unsupported platform %s", goos)
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	bwrapPath, err := lookPath("bwrap")
	if err != nil {
		return BwrapSpec{}, errors.New("bwrap binary not found; install bubblewrap and ensure bwrap is on PATH")
	}
	cwd := filepath.Join(project.Root, sandboxConfig.CWD)
	pathExists := opts.PathExists
	if pathExists == nil {
		pathExists = hostPathExists
	}
	argv := buildBwrapArgs(project.Root, cwd, sandboxConfig, pathExists)
	env := append([]string(nil), os.Environ()...)
	env = append(env, markerSandbox, markerSandboxRoot+project.Root)
	return BwrapSpec{
		Path: bwrapPath,
		Args: argv,
		CWD:  cwd,
		Env:  env,
	}, nil
}

func buildBwrapArgs(root, cwd string, sandboxConfig *config.SandboxConfig, pathExists func(string) bool) []string {
	args := []string{
		"--die-with-parent",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
	}
	if !sandboxConfig.Bubblewrap.Network.Value {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--bind", root, root)
	for _, path := range minimalExecutableHostPaths {
		if pathExists(path) {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--chdir", cwd, "--")
	args = append(args, sandboxConfig.Command.Argv...)
	return args
}

var minimalExecutableHostPaths = []string{
	"/usr",
	"/bin",
	"/sbin",
	"/lib",
	"/lib64",
	"/etc",
	"/nix/store",
}

func hostPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runSpec(ctx context.Context, spec BwrapSpec, opts Options) error {
	cmd := exec.Command(spec.Path, spec.Args...) // #nosec G204 -- bwrap path is resolved from PATH and argv is validated config.
	cmd.Dir = spec.CWD
	cmd.Env = spec.Env
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start bwrap: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return exitError(err)
	case <-ctx.Done():
		forwardTermination(cmd.Process.Pid)
		err := <-done
		if err != nil {
			return errors.Join(ctx.Err(), exitError(err))
		}
		return ctx.Err()
	}
}

func exitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ExitError{Code: processExitCode(exitErr), Err: err}
	}
	return err
}

func processExitCode(exitErr *exec.ExitError) int {
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return exitErr.ExitCode()
}

func forwardTermination(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
