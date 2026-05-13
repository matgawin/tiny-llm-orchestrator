package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestExecuteHelp(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if err := Execute(args, &stdout, &stderr); err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}

			output := stdout.String()
			for _, want := range []string{"Usage:", "Available Commands:", "completion", "init", "progress", "report", "run", "sandbox", "worker", "version"} {
				if !strings.Contains(output, want) {
					t.Fatalf("help output missing %q:\n%s", want, output)
				}
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRootCommandUsesInjectedStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand(strings.NewReader(""), &stdout, &stderr)
	root.SetArgs([]string{"help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("root Execute returned error: %v", err)
	}

	if output := stdout.String(); !strings.Contains(output, "Usage:") || !strings.Contains(output, "Available Commands:") {
		t.Fatalf("stdout missing Cobra help:\n%s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteRootNoArgsShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if output := stdout.String(); !strings.Contains(output, "Usage:") {
		t.Fatalf("stdout missing help:\n%s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	oldVersion := version
	version = defaultVersion
	t.Cleanup(func() {
		version = oldVersion
	})

	if err := Execute([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got, want := stdout.String(), "orc dev\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteCompletionBash(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"completion", "bash"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"bash completion", "__start_orc"} {
		if !strings.Contains(output, want) {
			t.Fatalf("completion output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "unsupported shell") {
		t.Fatalf("stdout contains diagnostic:\n%s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteCompletionUnsupportedShell(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"completion", "nu"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	for _, want := range []string{`unsupported shell "nu"`, "Usage:", "completion <shell>"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestExecuteCompletionHelpDocumentsSupportedShells(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"completion", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	for _, want := range []string{"Supported shells", "bash", "zsh", "fish", "powershell"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestExecuteCompletionMissingShell(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"completion"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	for _, want := range []string{"requires <shell>", "Usage:", "completion <shell>"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := Execute([]string{"nope"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want error")
	}
	if ExitCode(err) == 0 {
		t.Fatalf("ExitCode(%v) = 0, want nonzero", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, `unknown command "nope"`) {
		t.Fatalf("stderr = %q, want unknown command message", got)
	}
}
