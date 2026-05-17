// Package progress implements Orc's internal live worker progress socket
// protocol.
package progress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"tiny-llm-orchestrator/orc/internal/stableerr"

	"golang.org/x/time/rate"
)

const (
	maxMessageBytes       = 1000
	socketName            = "progress.sock"
	progressDirPerm       = 0o700
	acceptedQueueCapacity = 64
	sendTimeout           = 5 * time.Second
	minEscapeBytes        = 2
	csiSequenceSkip       = 2
	progressBurstLimit    = 3

	StatusAccepted = "accepted"
	StatusDropped  = "dropped"
	StatusRejected = "rejected"
)

var ErrUnavailable = errors.New("progress channel is unavailable")

type Request struct {
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id"`
	AttemptID string `json:"attempt_id"`
	Token     string `json:"token"`
	Message   string `json:"message"`
}

type Response struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Registration struct {
	RunID     string
	StepID    string
	AttemptID string
	Token     string
}

type AcceptedMessage struct {
	StepID    string
	AttemptID string
	Message   string
}

type Listener struct {
	listener net.Listener
	dir      string
	path     string
	now      func() time.Time

	mu           sync.Mutex
	closed       bool
	registration Registration
	limiter      progressLimiter
	conns        map[net.Conn]struct{}

	accepted chan AcceptedMessage
	wg       sync.WaitGroup
}

func NewListener() (*Listener, error) {
	return NewListenerContext(context.Background())
}

func NewListenerContext(ctx context.Context) (*Listener, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, stableerr.Errorf("progress sockets are unsupported on %s", runtime.GOOS)
	}

	dir, err := os.MkdirTemp("", "orc-progress-*")
	if err != nil {
		return nil, fmt.Errorf("new listener context: %w", err)
	}

	// #nosec G302 -- the progress socket directory must be traversable by the
	// current user while excluding group and other users.
	if err := os.Chmod(dir, progressDirPerm); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("new listener context: %w", err)
	}

	path := filepath.Join(dir, socketName)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("new listener context: %w", err)
	}

	l := &Listener{
		listener: ln,
		dir:      dir,
		path:     path,
		now:      time.Now,
		conns:    make(map[net.Conn]struct{}),
		accepted: make(chan AcceptedMessage, acceptedQueueCapacity),
	}
	l.wg.Add(1)

	go l.acceptLoop()

	return l, nil
}

func GenerateToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	return hex.EncodeToString(b[:]), nil
}

func ValidateMessage(raw string) error {
	_, err := sanitizeMessage(raw)
	return err
}

func Send(socketPath string, req Request) (Response, error) {
	if socketPath == "" {
		return Response{}, ErrUnavailable
	}

	dialer := net.Dialer{Timeout: sendTimeout}

	conn, err := dialer.DialContext(context.Background(), "unix", socketPath)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	defer func() {
		_ = conn.Close()
	}()

	if err := conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		return Response{}, fmt.Errorf("send: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("send: %w", err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("send: %w", err)
	}

	return resp, nil
}

func (l *Listener) SocketPath() string {
	if l == nil {
		return ""
	}

	return l.path
}

func (l *Listener) Accepted() <-chan AcceptedMessage {
	return l.accepted
}

func (l *Listener) Register(reg Registration) error {
	if l == nil {
		return stableerr.New("listener is nil")
	}

	if reg.RunID == "" {
		return stableerr.New("run id is required")
	}

	if reg.StepID == "" {
		return stableerr.New("step id is required")
	}

	if reg.AttemptID == "" {
		return stableerr.New("attempt id is required")
	}

	if reg.Token == "" {
		return stableerr.New("token is required")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return stableerr.New("listener is closed")
	}

	l.registration = reg
	l.limiter = newProgressLimiter()

	return nil
}

func (l *Listener) Close() error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}

	l.closed = true
	for conn := range l.conns {
		_ = conn.Close()
	}
	l.mu.Unlock()

	err := l.listener.Close()
	l.wg.Wait()
	close(l.accepted)

	if removeErr := os.RemoveAll(l.dir); err == nil {
		err = removeErr
	}

	return err
}

func (l *Listener) acceptLoop() {
	defer l.wg.Done()

	for {
		conn, err := l.listener.Accept()
		if err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()

			if closed {
				return
			}

			continue
		}

		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()

			_ = conn.Close()

			return
		}

		l.conns[conn] = struct{}{}
		l.mu.Unlock()
		l.wg.Add(1)

		go l.handleConn(conn)
	}
}

func (l *Listener) handleConn(conn net.Conn) {
	defer l.wg.Done()
	defer func() {
		_ = conn.Close()

		l.mu.Lock()
		delete(l.conns, conn)
		l.mu.Unlock()
	}()

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	for {
		var req Request

		dec.DisallowUnknownFields()

		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			if err := enc.Encode(Response{Status: StatusRejected, Error: "invalid progress request JSON"}); err != nil {
				return
			}

			return
		}

		if err := enc.Encode(l.evaluate(req)); err != nil {
			return
		}
	}
}

func (l *Listener) evaluate(req Request) Response {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return Response{Status: StatusRejected, Error: "progress listener is closed"}
	}

	reg := l.registration
	if reg.RunID == "" {
		l.mu.Unlock()
		return Response{Status: StatusRejected, Error: "no active progress attempt is registered"}
	}

	if req.RunID != reg.RunID || req.StepID != reg.StepID || req.AttemptID != reg.AttemptID || req.Token != reg.Token {
		l.mu.Unlock()
		return Response{Status: StatusRejected, Error: "progress request identity or token is invalid"}
	}

	msg, err := sanitizeMessage(req.Message)
	if err != nil {
		l.mu.Unlock()
		return Response{Status: StatusRejected, Error: err.Error()}
	}

	if !l.limiter.allow(l.now()) {
		l.mu.Unlock()
		return Response{Status: StatusDropped}
	}

	accepted := AcceptedMessage{
		StepID:    req.StepID,
		AttemptID: req.AttemptID,
		Message:   msg,
	}
	l.mu.Unlock()

	select {
	case l.accepted <- accepted:
	default:
		return Response{Status: StatusDropped}
	}

	return Response{Status: StatusAccepted}
}

func sanitizeMessage(raw string) (string, error) {
	var b strings.Builder
	b.Grow(len(raw))

	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}

		if r == '\x1b' {
			i += consumeEscape(raw[i:])
			continue
		}

		if r == '\n' || r == '\r' {
			b.WriteByte(' ')

			i += size

			continue
		}

		if unicode.IsControl(r) {
			i += size
			continue
		}

		b.WriteRune(r)

		i += size
	}

	msg := strings.TrimSpace(b.String())
	if msg == "" {
		return "", stableerr.New("progress message is empty after sanitization")
	}

	if len([]byte(msg)) > maxMessageBytes {
		return "", stableerr.Errorf("progress message exceeds %d bytes after sanitization", maxMessageBytes)
	}

	return msg, nil
}

func consumeEscape(s string) int {
	if len(s) < minEscapeBytes {
		return 1
	}

	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}

		return len(s)
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == '\a' {
				return i + 1
			}

			if i+1 < len(s) && s[i] == '\x1b' && s[i+1] == '\\' {
				return i + csiSequenceSkip
			}
		}

		return len(s)
	default:
		return 1
	}
}

type progressLimiter struct {
	limiter *rate.Limiter
}

func newProgressLimiter() progressLimiter {
	return progressLimiter{
		limiter: rate.NewLimiter(rate.Limit(1), progressBurstLimit),
	}
}

func (l progressLimiter) allow(now time.Time) bool {
	if l.limiter == nil {
		return false
	}

	return l.limiter.AllowN(now, 1)
}
