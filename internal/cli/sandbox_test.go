package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/sandbox"
)

func TestExecuteSandboxHelp(t *testing.T) {
	output := executeCLICommand(t, []string{"sandbox", "--help"})
	for _, want := range []string{"Usage:", "Available Commands:", "run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("sandbox help output missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteSandboxRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"sandbox", "run", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	for _, want := range []string{"Usage:", "sandbox run"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("sandbox run help output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestExecuteSandboxUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"sandbox", "unknown"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown subcommand")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, `unknown command "unknown" for "orc sandbox"`) {
		t.Fatalf("stderr = %q, want Cobra unknown command diagnostic", got)
	}
}

func TestExecuteSandboxRunRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"sandbox", "run", "extra"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want extra arg rejection")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, `orc sandbox run: unexpected argument "extra"`) {
		t.Fatalf("stderr = %q, want extra arg diagnostic", got)
	}
}

func TestExecuteSandboxRunRequiresConfig(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	var stdout, stderr bytes.Buffer

	err := Execute([]string{"sandbox", "run"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want missing sandbox config error")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "sandbox config is required") {
		t.Fatalf("stderr = %q, want missing sandbox config", got)
	}
}

func TestExecuteSandboxRunMissingBwrapDoesNotRunConfiguredCommand(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["sh", "-c", "touch should-not-run"]
  bubblewrap:
    enabled: true
`)
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer

	err := Execute([]string{"sandbox", "run"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want missing bwrap error")
	}
	if _, statErr := os.Stat(filepath.Join(root, "should-not-run")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("configured command marker stat error = %v, want not exist", statErr)
	}
	if got := stderr.String(); !strings.Contains(got, "install bubblewrap") {
		t.Fatalf("stderr = %q, want install guidance", got)
	}
}

func TestExitCodeUsesSandboxChildExitStatus(t *testing.T) {
	err := sandbox.ExitError{Code: 7, Err: errors.New("exit status 7")}
	if got := ExitCode(err); got != 7 {
		t.Fatalf("ExitCode = %d, want 7", got)
	}
}
