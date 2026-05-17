package cli

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteProgressNoSocketWarnsAndExitsZero(t *testing.T) {
	t.Setenv("ORC_PROGRESS_SOCKET", "")

	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"progress", "analyzing", "code", "paths"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "live progress channel unavailable") {
		t.Fatalf("stderr = %q, want unavailable warning", got)
	}
}

func TestExecuteProgressUnavailableSocketPathWarnsAndExitsZero(t *testing.T) {
	root := t.TempDir()
	setCLIProgressEnv(t, filepath.Join(root, "missing.sock"), "token-001")

	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"progress", "working"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "live progress channel unavailable") {
		t.Fatalf("stderr = %q, want unavailable warning", got)
	}
}

func TestExecuteProgressSendsPayloadFromEnvironment(t *testing.T) {
	l := newCLIProgressListener(t)
	setCLIProgressEnv(t, l.SocketPath(), "token-001")

	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"progress", "analyzing", "code", "paths"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout/stderr = %q/%q, want empty", stdout.String(), stderr.String())
	}

	msg := receiveCLIProgress(t, l)
	if msg.StepID != "code" || msg.AttemptID != "attempt-001" || msg.Message != "analyzing code paths" {
		t.Fatalf("accepted message = %+v, want joined CLI message", msg)
	}
}

func TestExecuteProgressSocketWithoutIdentityEnvErrors(t *testing.T) {
	l := newCLIProgressListener(t)
	t.Setenv("ORC_PROGRESS_SOCKET", l.SocketPath())
	t.Setenv("ORC_RUN_ID", "")
	t.Setenv("ORC_STEP_ID", "code")
	t.Setenv("ORC_ATTEMPT_ID", "attempt-001")
	t.Setenv("ORC_PROGRESS_TOKEN", "token-001")

	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"progress", "working"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want missing environment error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "ORC_RUN_ID") || !strings.Contains(got, "ORC_PROGRESS_TOKEN") {
		t.Fatalf("stderr = %q, want actionable missing env error", got)
	}
}

func TestExecuteProgressInputValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no message", args: []string{"progress"}, want: "requires <message>"},
		{name: "empty", args: []string{"progress", ""}, want: "empty after sanitization"},
		{name: "whitespace only", args: []string{"progress", " \n\t "}, want: "empty after sanitization"},
		{name: "oversized", args: []string{"progress", strings.Repeat("x", 1001)}, want: "exceeds 1000 bytes"},
		{name: "unknown flag", args: []string{"progress", "--status", "working"}, want: "unknown flag"},
		{name: "dash message requires terminator", args: []string{"progress", "-working"}, want: "use -- before a message word that starts with '-'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORC_PROGRESS_SOCKET", "")

			var stdout, stderr bytes.Buffer
			if err := Execute(tt.args, &stdout, &stderr); err == nil {
				t.Fatal("Execute returned nil error, want input error")
			}

			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			if got := stderr.String(); !strings.Contains(got, tt.want) {
				t.Fatalf("stderr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteProgressSendsLiteralHelpMessage(t *testing.T) {
	l := newCLIProgressListener(t)
	setCLIProgressEnv(t, l.SocketPath(), "token-001")

	if err := Execute([]string{"progress", "help"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := receiveCLIProgress(t, l).Message; got != "help" {
		t.Fatalf("accepted message = %q, want literal help message", got)
	}
}

func TestExecuteProgressAllowsFlagLikeTextAfterTerminator(t *testing.T) {
	l := newCLIProgressListener(t)
	setCLIProgressEnv(t, l.SocketPath(), "token-001")

	if err := Execute([]string{"progress", "--", "--status", "working"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := receiveCLIProgress(t, l).Message; got != "--status working" {
		t.Fatalf("accepted message = %q, want flag-like text", got)
	}
}

func TestExecuteProgressDroppedWarnsAndExitsZero(t *testing.T) {
	l := newCLIProgressListener(t)
	setCLIProgressEnv(t, l.SocketPath(), "token-001")

	for range 3 {
		if err := Execute([]string{"progress", "burst"}, io.Discard, io.Discard); err != nil {
			t.Fatalf("Execute returned error during burst: %v", err)
		}
	}

	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"progress", "burst"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error for dropped update: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "rate-limited") {
		t.Fatalf("stderr = %q, want rate-limit warning", got)
	}
}

func TestExecuteProgressInvalidTokenErrors(t *testing.T) {
	l := newCLIProgressListener(t)
	setCLIProgressEnv(t, l.SocketPath(), "wrong-token")

	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"progress", "working"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want invalid token error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "identity or token is invalid") {
		t.Fatalf("stderr = %q, want listener rejection", got)
	}

	assertNoCLIProgress(t, l)
}

func TestExecuteProgressHelp(t *testing.T) {
	output := executeCLICommand(t, []string{"progress", "--help"})
	for _, want := range []string{"Usage:", "ORC_PROGRESS_SOCKET", "Rate-limited", "orc report --status"} {
		if !strings.Contains(output, want) {
			t.Fatalf("progress help output missing %q:\n%s", want, output)
		}
	}
}
