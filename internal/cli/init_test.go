package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteInitDryRunUsesCurrentDirectory(t *testing.T) {
	withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "orc init dry-run:") {
		t.Fatalf("stdout = %q, want init dry-run routing", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteInitRejectsDryRunWithYes(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--dry-run", "--yes"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want invalid flags")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "orc init:") {
		t.Fatalf("stderr = %q, want init error context", got)
	}
}

func TestExecuteInitHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"orc init scaffolds", "Usage:", "--dry-run", "--yes"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteInitUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown flag error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	output := stderr.String()
	for _, want := range []string{`unknown flag: --bogus`, "Usage:", "orc init"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteInitYesCreatesScaffold(t *testing.T) {
	root := withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "--yes"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if got := stdout.String(); !strings.Contains(got, "created .orc/config.yaml") {
		t.Fatalf("stdout = %q, want scaffold creation output", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".orc", "config.yaml")); err != nil {
		t.Fatalf("config stat error: %v", err)
	}
}

func TestExecuteWithInputInitForwardsInteractiveInput(t *testing.T) {
	root := withTempCwd(t)

	if err := os.MkdirAll(filepath.Join(root, ".orc"), 0o755); err != nil {
		t.Fatalf("create .orc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".orc", "config.yaml"), []byte("custom: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	stdin := confirmThreeInitPromptsThroughCLI()
	if err := ExecuteWithInput([]string{"init"}, stdin, &stdout, &stderr); err != nil {
		t.Fatalf("ExecuteWithInput returned error: %v\nstderr: %s", err, stderr.String())
	}

	if got := stdout.String(); !strings.Contains(got, "Overwrite .orc/config.yaml?") {
		t.Fatalf("stdout = %q, want forwarded interactive response", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func withTempCwd(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir temp root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	return root
}

func confirmThreeInitPromptsThroughCLI() *strings.Reader {
	return strings.NewReader("yes\nyes\nyes\n")
}
