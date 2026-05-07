package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
)

const testBwrapPath = "/usr/bin/bwrap"

func TestBuildSpecConstructsBubblewrapArgv(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.Mkdir(cwd, 0o750); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	project := sandboxProject(root, config.SandboxConfig{
		Command: config.SandboxCommand{Argv: []string{"codex", "exec"}},
		CWD:     "work",
		Bubblewrap: config.BubblewrapConfig{
			Enabled: true,
			Network: config.RequiredBool{
				Value: true,
				Set:   true,
			},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		PathExists:  onlyHostPaths("/usr", "/bin", "/lib64", "/etc"),
		LookPath: func(name string) (string, error) {
			if name != "bwrap" {
				t.Fatalf("LookPath name = %q, want bwrap", name)
			}
			return testBwrapPath, nil
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}

	wantArgs := []string{
		"--die-with-parent",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--bind", root, root,
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
		"--proc", "/proc",
		"--dev", "/dev",
		"--chdir", cwd,
		"--",
		"codex", "exec",
	}
	if !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("bwrap args = %#v, want %#v", spec.Args, wantArgs)
	}
	if spec.Path != testBwrapPath {
		t.Fatalf("bwrap path = %q, want %s", spec.Path, testBwrapPath)
	}
	if spec.CWD != cwd {
		t.Fatalf("cwd = %q, want %q", spec.CWD, cwd)
	}
	assertEnvContains(t, spec.Env, markerSandbox)
	assertEnvContains(t, spec.Env, markerSandboxRoot+root)
}

func TestBuildSpecNetworkFalseAddsUnshareNet(t *testing.T) {
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command: config.SandboxCommand{Argv: []string{"sh"}},
		CWD:     ".",
		Bubblewrap: config.BubblewrapConfig{
			Enabled: true,
			Network: config.RequiredBool{
				Value: false,
				Set:   true,
			},
		},
	})

	spec, err := BuildSpec(project, Options{RuntimeGOOS: "linux", LookPath: foundBwrap, PathExists: noHostPaths})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsArg(spec.Args, "--unshare-net") {
		t.Fatalf("bwrap args = %#v, want --unshare-net", spec.Args)
	}
}

func TestBuildSpecRejectsMissingSandboxConfig(t *testing.T) {
	_, err := BuildSpec(&config.Project{Root: t.TempDir()}, Options{RuntimeGOOS: "linux", LookPath: foundBwrap})
	if err == nil || !strings.Contains(err.Error(), "sandbox config is required") {
		t.Fatalf("BuildSpec error = %v, want missing sandbox config error", err)
	}
}

func TestBuildSpecRejectsUnsupportedPlatformBeforeBwrapLookup(t *testing.T) {
	called := false
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "darwin",
		LookPath: func(string) (string, error) {
			called = true
			return "", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires Linux") {
		t.Fatalf("BuildSpec error = %v, want unsupported platform error", err)
	}
	if called {
		t.Fatal("LookPath was called on unsupported platform")
	}
}

func TestBuildSpecRejectsMissingBwrap(t *testing.T) {
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	})
	if err == nil || !strings.Contains(err.Error(), "install bubblewrap") {
		t.Fatalf("BuildSpec error = %v, want install guidance", err)
	}
}

func TestRunSpecReturnsChildExitStatus(t *testing.T) {
	err := runSpec(context.Background(), BwrapSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "exit 7"},
		CWD:  t.TempDir(),
		Env:  os.Environ(),
	}, Options{})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runSpec error = %T %v, want ExitError", err, err)
	}
	if exitErr.Code != 7 {
		t.Fatalf("exit code = %d, want 7", exitErr.Code)
	}
}

func TestRunSpecReturnsSignalExitStatus(t *testing.T) {
	err := runSpec(context.Background(), BwrapSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "kill -TERM $$"},
		CWD:  t.TempDir(),
		Env:  os.Environ(),
	}, Options{})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runSpec error = %T %v, want ExitError", err, err)
	}
	if exitErr.Code != 143 {
		t.Fatalf("exit code = %d, want 143", exitErr.Code)
	}
}

func TestRunSpecCancelsProcessGroup(t *testing.T) {
	root := t.TempDir()
	termPath := filepath.Join(root, "term")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runSpec(ctx, BwrapSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "trap 'touch " + shellQuote(termPath) + "; exit 0' TERM; while :; do sleep 1; done"},
			CWD:  root,
			Env:  os.Environ(),
		}, Options{})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("runSpec error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSpec did not return after context cancellation")
	}
	if _, err := os.Stat(termPath); err != nil {
		t.Fatalf("termination marker missing: %v", err)
	}
}

func sandboxProject(root string, sandboxConfig config.SandboxConfig) *config.Project {
	return &config.Project{
		Root: root,
		Config: config.ProjectConfig{
			Sandbox: &sandboxConfig,
		},
	}
}

func foundBwrap(string) (string, error) {
	return testBwrapPath, nil
}

func noHostPaths(string) bool {
	return false
}

func onlyHostPaths(paths ...string) func(string) bool {
	return func(path string) bool {
		return slices.Contains(paths, path)
	}
}

func containsArg(args []string, want string) bool {
	return slices.Contains(args, want)
}

func assertEnvContains(t *testing.T, env []string, want string) {
	t.Helper()
	if slices.Contains(env, want) {
		return
	}
	t.Fatalf("env missing %q: %#v", want, env)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
