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
	beads := filepath.Clean(filepath.Join(root, "..", ".beads"))
	if err := os.Mkdir(beads, 0o750); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	codexHome := filepath.Join(root, "codex-home")
	extraMount := filepath.Join(root, "data")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.Mkdir(extraMount, 0o750); err != nil {
		t.Fatalf("mkdir extra mount: %v", err)
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
		Env: config.SandboxEnvConfig{
			Pass: []string{"EXTRA_TOKEN"},
			Set: map[string]string{
				"TERM": "xterm-256color",
			},
		},
		Mounts: []config.SandboxMount{
			{Host: "data", Target: "/workspace/data", Mode: "ro"},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		Environ: func() []string {
			return []string{
				"PATH=/usr/bin",
				"HOME=" + root,
				"TERM=host-term",
				"LANG=C.UTF-8",
				"LC_ALL=C.UTF-8",
				"SHELL=/bin/sh",
				"USER=tester",
				"LOGNAME=tester",
				"CODEX_HOME=" + codexHome,
				"OPENAI_API_KEY=sk-test",
				"EXTRA_TOKEN=from-pass",
				"UNRELATED_SECRET=hidden",
			}
		},
		PathExists: onlyHostPaths(beads, codexHome, extraMount, "/usr", "/bin", "/lib64", "/etc"),
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
		"--clearenv",
		"--tmpfs", "/tmp",
		"--dir", "/home",
		"--dir", "/home/orc",
		"--dir", filepath.Dir(root),
		"--dir", root,
		"--dir", beads,
		"--dir", codexHome,
		"--dir", "/workspace",
		"--bind", root, root,
		"--bind", beads, beads,
		"--bind", codexHome, codexHome,
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind", extraMount, "/workspace/data",
		"--proc", "/proc",
		"--dev", "/dev",
		"--setenv", "CODEX_HOME", codexHome,
		"--setenv", "EXTRA_TOKEN", "from-pass",
		"--setenv", "HOME", "/home/orc",
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "LC_ALL", "C.UTF-8",
		"--setenv", "LOGNAME", "tester",
		"--setenv", "OPENAI_API_KEY", "sk-test",
		"--setenv", "ORC_SANDBOX", "1",
		"--setenv", "ORC_SANDBOX_ROOT", root,
		"--setenv", "PATH", "/usr/bin",
		"--setenv", "SHELL", "/bin/sh",
		"--setenv", "TERM", "xterm-256color",
		"--setenv", "USER", "tester",
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
	assertEnvContains(t, spec.Env, "HOME=/home/orc")
	assertEnvContains(t, spec.Env, "CODEX_HOME="+codexHome)
	assertEnvContains(t, spec.Env, markerSandbox)
	assertEnvContains(t, spec.Env, markerSandboxRoot+root)
	assertEnvContains(t, spec.Env, "EXTRA_TOKEN=from-pass")
	assertEnvContains(t, spec.Env, "TERM=xterm-256color")
	assertEnvMissing(t, spec.Env, "UNRELATED_SECRET=")
}

func TestBuildSpecCreatesHomeDirsBeforeRepoBind(t *testing.T) {
	root := "/home/tester/project"
	codexHome := "/home/tester/.codex"
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(codexHome),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=/home/tester", "CODEX_HOME=" + codexHome}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	repoBind := indexSequence(spec.Args, "--bind", root, root)
	homeDir := indexSequence(spec.Args, "--dir", "/home")
	userHomeDir := indexSequence(spec.Args, "--dir", "/home/tester")
	if repoBind < 0 || homeDir < 0 || userHomeDir < 0 {
		t.Fatalf("bwrap args = %#v, want /home setup dirs and repo bind", spec.Args)
	}
	if homeDir > repoBind || userHomeDir > repoBind {
		t.Fatalf("bwrap args = %#v, want /home setup dirs before repo bind", spec.Args)
	}
}

func TestBuildSpecNetworkFalseAddsUnshareNet(t *testing.T) {
	root := t.TempDir()
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

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ:     testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsArg(spec.Args, "--unshare-net") {
		t.Fatalf("bwrap args = %#v, want --unshare-net", spec.Args)
	}
}

func TestBuildSpecSkipsMissingOptionalBeadsDir(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(filepath.Join(root, ".codex")),
		Environ:     testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if containsSequence(spec.Args, "--bind", filepath.Clean(filepath.Join(root, "..", ".beads")), filepath.Clean(filepath.Join(root, "..", ".beads"))) {
		t.Fatalf("bwrap args = %#v, want missing beads dir skipped", spec.Args)
	}
}

func TestBuildSpecCreatesDefaultCodexHomeUnderSyntheticHome(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	var created string

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ:     testEnv(root),
		MkdirAll: func(path string, _ os.FileMode) error {
			created = path
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	hostCodex := filepath.Join(root, ".codex")
	if created != hostCodex {
		t.Fatalf("created codex home = %q, want %q", created, hostCodex)
	}
	if !containsSequence(spec.Args, "--bind", hostCodex, "/home/orc/.codex") {
		t.Fatalf("bwrap args = %#v, want default codex home mounted into synthetic home", spec.Args)
	}
	assertEnvContains(t, spec.Env, "HOME=/home/orc")
	assertEnvContains(t, spec.Env, "CODEX_HOME=/home/orc/.codex")
}

func TestBuildSpecSkipsMissingOptionalExtraMount(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Mounts: []config.SandboxMount{
			{Host: "missing", Target: "/workspace/missing", Mode: "rw", Optional: config.RequiredBool{Value: true, Set: true}},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(filepath.Join(root, ".codex")),
		Environ:     testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if containsSequence(spec.Args, "--bind", filepath.Join(root, "missing"), "/workspace/missing") {
		t.Fatalf("bwrap args = %#v, want optional missing mount skipped", spec.Args)
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

func testEnv(home string) func() []string {
	return func() []string {
		return []string{"PATH=/usr/bin", "HOME=" + home}
	}
}

func containsArg(args []string, want string) bool {
	return slices.Contains(args, want)
}

func containsSequence(args []string, want ...string) bool {
	return indexSequence(args, want...) >= 0
}

func indexSequence(args []string, want ...string) int {
	if len(want) == 0 || len(want) > len(args) {
		return -1
	}
	for i := 0; i <= len(args)-len(want); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return i
		}
	}
	return -1
}

func assertEnvContains(t *testing.T, env []string, want string) {
	t.Helper()
	if slices.Contains(env, want) {
		return
	}
	t.Fatalf("env missing %q: %#v", want, env)
}

func assertEnvMissing(t *testing.T, env []string, prefix string) {
	t.Helper()
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			t.Fatalf("env contains %q with prefix %q: %#v", entry, prefix, env)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
