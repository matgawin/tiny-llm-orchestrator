package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

const (
	execHelperEnv = "ORC_LAUNCHER_EXEC_HELPER"
	execHelperArg = "__orc-launcher-exec-helper"
	execHelperFD  = uintptr(3)

	execHelperExitHandshake = 125
	execHelperExitExec      = 126
)

func resolveRepoRelativeDir(root, rel string) (string, error) {
	path, err := resolveRepoRelative(root, rel)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("resolve repo relative dir: %w", err)
	}

	if !info.IsDir() {
		return "", stableerr.Errorf("cwd %q is not a directory", rel)
	}

	return path, nil
}

func resolveRepoRelativeExecutable(root, rel string) (string, error) {
	path, err := resolveRepoRelative(root, rel)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("resolve repo relative executable: %w", err)
	}

	if info.IsDir() {
		return "", stableerr.Errorf("script %q is a directory", rel)
	}

	if info.Mode()&0o111 == 0 {
		return "", stableerr.Errorf("script %q is not executable", rel)
	}

	return path, nil
}

func resolveRepoRelative(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", stableerr.Errorf("path %q must be repo-relative", rel)
	}

	clean := filepath.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", stableerr.Errorf("path %q must be clean and stay under repository root", rel)
	}

	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repo relative: %w", err)
	}

	candidate := filepath.Join(root, clean)

	realPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve repo relative: %w", err)
	}

	relToRoot, err := filepath.Rel(rootReal, realPath)
	if err != nil {
		return "", fmt.Errorf("resolve repo relative: %w", err)
	}

	if relToRoot == "." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || relToRoot == ".." || filepath.IsAbs(relToRoot) {
		return "", stableerr.Errorf("path %q escapes repository root", rel)
	}

	return realPath, nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}

	out := append([]string(nil), base...)

	for key, value := range overrides {
		prefix := key + "="
		replaced := false

		for i := range out {
			if strings.HasPrefix(out[i], prefix) {
				out[i] = prefix + value
				replaced = true
			}
		}

		if !replaced {
			out = append(out, prefix+value)
		}
	}

	return out
}

//nolint:contextcheck // The helper process must stay alive until explicit release even if the launch context is canceled first.
func newWorkerCommand(ctx context.Context, command, env []string, dir string) (*exec.Cmd, func(bool) error, error) {
	execPath, err := resolveWorkerExecutable(command[0], env, dir)
	if err != nil {
		return nil, nil, err
	}

	helperPath, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve launcher helper: %w", err)
	}

	helperToken, err := newExecHelperToken()
	if err != nil {
		return nil, nil, err
	}

	helperArgs := append([]string{execHelperArg, helperToken, execPath}, command[1:]...)

	readFile, writeFile, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("new worker command: %w", err)
	}

	released := false
	release := func(start bool) error {
		if released {
			return nil
		}

		released = true

		defer func() {
			_ = writeFile.Close()
		}()

		_ = readFile.Close()

		if !start {
			return nil
		}

		if _, err := writeFile.WriteString(helperToken + "\n"); err != nil {
			return fmt.Errorf("release worker exec: %w", err)
		}

		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), helperPath, helperArgs...) // #nosec G204,G702 -- re-execing the current launcher binary is intentional; helper execs the configured worker only after durable PID recording.
	cmd.ExtraFiles = []*os.File{readFile}
	cmd.Env = []string{execHelperEnv + "=" + helperToken}

	return cmd, release, nil
}

func resolveWorkerExecutable(name string, env []string, cwd string) (string, error) {
	execPath := name
	if strings.ContainsRune(execPath, os.PathSeparator) {
		statPath := execPath
		if !filepath.IsAbs(statPath) {
			statPath = filepath.Join(cwd, statPath)
		}

		info, err := os.Stat(statPath)
		if err != nil {
			return "", fmt.Errorf("resolve worker executable: %w", err)
		}

		if info.IsDir() {
			return "", stableerr.Errorf("%s is a directory", execPath)
		}

		if info.Mode()&0o111 == 0 {
			return "", stableerr.Errorf("%s is not executable", execPath)
		}

		return execPath, nil
	}

	for _, dir := range workerPath(env) {
		if dir == "" {
			dir = "."
		}

		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}

		candidate := filepath.Join(dir, execPath)

		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}

		return candidate, nil
	}

	return "", exec.ErrNotFound
}

func workerPath(env []string) []string {
	const prefix = "PATH="
	for i := len(env) - 1; i >= 0; i-- {
		if after, ok := strings.CutPrefix(env[i], prefix); ok {
			return filepath.SplitList(after)
		}
	}

	return nil
}

func runExecHelper(wantToken string, command []string) int {
	if wantToken == "" || os.Getenv(execHelperEnv) != wantToken {
		return execHelperExitHandshake
	}

	if len(command) == 0 || command[0] == "" {
		return execHelperExitHandshake
	}

	handshake := os.NewFile(execHelperFD, "launcher-handshake")
	if handshake == nil {
		return execHelperExitHandshake
	}

	token, err := readExecHelperToken(handshake)

	closeErr := handshake.Close()
	if err != nil || closeErr != nil || token != wantToken {
		return execHelperExitHandshake
	}

	env := filteredExecEnv(os.Environ())
	if err := syscall.Exec(command[0], command, env); err != nil { // #nosec G204,G702 -- worker launching intentionally execs the configured Codex command after the parent records process metadata.
		_, _ = fmt.Fprintln(os.Stderr, err)
		return execHelperExitExec
	}

	return 0
}

func newExecHelperToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate exec helper token: %w", err)
	}

	return hex.EncodeToString(buf[:]), nil
}

func readExecHelperToken(reader io.Reader) (string, error) {
	var buf [33]byte

	n, err := io.ReadFull(reader, buf[:])
	if err != nil {
		return "", fmt.Errorf("read exec helper token: %w", err)
	}

	if n != len(buf) || buf[len(buf)-1] != '\n' {
		return "", stableerr.New("invalid exec helper token")
	}

	return string(buf[:len(buf)-1]), nil
}

func filteredExecEnv(env []string) []string {
	out := env[:0]

	prefix := execHelperEnv + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}

		out = append(out, item)
	}

	return out
}
