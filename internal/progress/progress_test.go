package progress

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var validRegistration = Registration{
	RunID:     "run-001",
	StepID:    "code",
	AttemptID: "attempt-001",
	Token:     "token-001",
}

func TestListenerAcceptsValidTokenBearingPayload(t *testing.T) {
	l := newRegisteredListener(t, validRegistration)

	resp := sendProgress(t, l.SocketPath(), Request{
		RunID:     validRegistration.RunID,
		StepID:    validRegistration.StepID,
		AttemptID: validRegistration.AttemptID,
		Token:     validRegistration.Token,
		Message:   "  \x1b[31mediting\ncode\rnow\x1b[0m  ",
	})
	if resp.Status != StatusAccepted {
		t.Fatalf("response status = %q, want accepted: %s", resp.Status, resp.Error)
	}
	msg := receiveAccepted(t, l)
	want := AcceptedMessage{StepID: "code", AttemptID: "attempt-001", Message: "editing code now"}
	if msg != want {
		t.Fatalf("accepted message = %+v, want %+v", msg, want)
	}
}

func TestListenerRejectsMissingWrongOrMismatchedToken(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "missing token",
			req: Request{
				RunID:     validRegistration.RunID,
				StepID:    validRegistration.StepID,
				AttemptID: validRegistration.AttemptID,
				Message:   "working",
			},
		},
		{
			name: "wrong token",
			req: Request{
				RunID:     validRegistration.RunID,
				StepID:    validRegistration.StepID,
				AttemptID: validRegistration.AttemptID,
				Token:     "wrong",
				Message:   "working",
			},
		},
		{
			name: "mismatched identity",
			req: Request{
				RunID:     validRegistration.RunID,
				StepID:    "test",
				AttemptID: validRegistration.AttemptID,
				Token:     validRegistration.Token,
				Message:   "working",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newRegisteredListener(t, validRegistration)
			resp := sendProgress(t, l.SocketPath(), tt.req)
			if resp.Status != StatusRejected {
				t.Fatalf("response status = %q, want rejected", resp.Status)
			}
			if resp.Error == "" {
				t.Fatal("response error is empty, want actionable rejection error")
			}
			assertNoAccepted(t, l)
		})
	}
}

func TestListenerSanitizationAndInputRejection(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		want      string
		wantState string
	}{
		{name: "empty", message: "", wantState: StatusRejected},
		{name: "whitespace only", message: " \t\n\r ", wantState: StatusRejected},
		{name: "control character only", message: "\x1b[31m\x1b[0m\a", wantState: StatusRejected},
		{name: "multiline", message: "  first\nsecond\rthird  ", want: "first second third", wantState: StatusAccepted},
		{name: "oversized", message: strings.Repeat("x", maxMessageBytes+1), wantState: StatusRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newRegisteredListener(t, validRegistration)
			resp := sendProgress(t, l.SocketPath(), requestWithMessage(tt.message))
			if resp.Status != tt.wantState {
				t.Fatalf("response status = %q, want %q; error=%q", resp.Status, tt.wantState, resp.Error)
			}
			if tt.wantState == StatusAccepted {
				msg := receiveAccepted(t, l)
				if msg.Message != tt.want {
					t.Fatalf("sanitized message = %q, want %q", msg.Message, tt.want)
				}
			} else {
				if resp.Error == "" {
					t.Fatal("response error is empty, want rejection reason")
				}
				assertNoAccepted(t, l)
			}
		})
	}
}

func TestListenerRateLimitDropsExcessMessages(t *testing.T) {
	l := newRegisteredListener(t, validRegistration)

	for i := range 3 {
		resp := sendProgress(t, l.SocketPath(), requestWithMessage("burst"))
		if resp.Status != StatusAccepted {
			t.Fatalf("response %d status = %q, want accepted: %s", i, resp.Status, resp.Error)
		}
	}
	resp := sendProgress(t, l.SocketPath(), requestWithMessage("burst"))
	if resp.Status != StatusDropped {
		t.Fatalf("fourth response status = %q, want dropped", resp.Status)
	}
	if resp.Error != "" {
		t.Fatalf("dropped response error = %q, want empty", resp.Error)
	}
	for range 3 {
		_ = receiveAccepted(t, l)
	}
	assertNoAccepted(t, l)
}

func TestListenerCleanupRemovesSocketDirectory(t *testing.T) {
	l := newRegisteredListener(t, validRegistration)
	path := l.SocketPath()
	dir := strings.TrimSuffix(path, "/"+socketName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket path does not exist before close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if _, ok := <-l.Accepted(); ok {
		t.Fatal("accepted channel is still open after Close")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket path stat after close error = %v, want not exist", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("socket directory stat after close error = %v, want not exist", err)
	}
}

func TestListenerCloseUnblocksIdleConnection(t *testing.T) {
	l := newRegisteredListener(t, validRegistration)
	conn, err := net.DialTimeout("unix", l.SocketPath(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout returned error: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if err := l.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline returned error: %v", err)
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("Read after listener close returned nil error, want closed connection")
	}
}

func TestGenerateTokenIsPrintableAndAtLeast128Bits(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}
	if len(token) != 32 {
		t.Fatalf("token length = %d, want 32 hex characters", len(token))
	}
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("token contains non-hex printable rune %q", r)
		}
	}
}

func TestSocketDirectoryUsesPrivatePermissions(t *testing.T) {
	l := newRegisteredListener(t, validRegistration)
	info, err := os.Stat(strings.TrimSuffix(l.SocketPath(), "/"+socketName))
	if err != nil {
		t.Fatalf("stat socket directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("socket directory mode = %o, want 700", got)
	}
}

func TestPackageHasNoRunStoreWorkflowOrReportDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", "{{join .Deps \"\\n\"}}", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list returned error: %v\n%s", err, out)
	}
	for dep := range strings.SplitSeq(string(out), "\n") {
		switch dep {
		case "tiny-llm-orchestrator/orc/internal/runstore",
			"tiny-llm-orchestrator/orc/internal/workflow",
			"tiny-llm-orchestrator/orc/internal/report":
			t.Fatalf("progress package must not depend on %s", dep)
		}
	}
}

func newRegisteredListener(t *testing.T, reg Registration) *Listener {
	t.Helper()
	l, err := NewListener()
	if err != nil {
		t.Fatalf("NewListener returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	if err := l.Register(reg); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	return l
}

func requestWithMessage(message string) Request {
	return Request{
		RunID:     validRegistration.RunID,
		StepID:    validRegistration.StepID,
		AttemptID: validRegistration.AttemptID,
		Token:     validRegistration.Token,
		Message:   message,
	}
}

func sendProgress(t *testing.T, socketPath string, req Request) Response {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("DialTimeout returned error: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetDeadline returned error: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("Decode response returned error: %v", err)
	}
	return resp
}

func receiveAccepted(t *testing.T, l *Listener) AcceptedMessage {
	t.Helper()
	select {
	case msg := <-l.Accepted():
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for accepted progress message")
		return AcceptedMessage{}
	}
}

func assertNoAccepted(t *testing.T, l *Listener) {
	t.Helper()
	select {
	case msg := <-l.Accepted():
		t.Fatalf("unexpected accepted message: %+v", msg)
	case <-time.After(25 * time.Millisecond):
	}
}
