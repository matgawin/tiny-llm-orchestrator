package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
	Environ     func() []string
	UserHomeDir func() (string, error)
	MkdirAll    func(string, os.FileMode) error
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
	policy, err := resolvePolicy(project.Root, sandboxConfig, opts, pathExists)
	if err != nil {
		return BwrapSpec{}, err
	}
	argv := buildBwrapArgs(project.Root, cwd, sandboxConfig, policy, pathExists)
	env := policy.envList()
	return BwrapSpec{
		Path: bwrapPath,
		Args: argv,
		CWD:  cwd,
		Env:  env,
	}, nil
}

func buildBwrapArgs(root, cwd string, sandboxConfig *config.SandboxConfig, policy sandboxPolicy, pathExists func(string) bool) []string {
	args := []string{
		"--die-with-parent",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
	}
	if !sandboxConfig.Bubblewrap.Network.Value {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--clearenv", "--tmpfs", "/tmp")
	for _, dir := range policy.setupDirs(root) {
		args = append(args, "--dir", dir)
	}
	args = append(args, "--bind", root, root)
	if policy.beadsPath != "" {
		args = append(args, "--bind", policy.beadsPath, policy.beadsPath)
	}
	args = append(args, "--bind", policy.codexHostPath, policy.codexTargetPath)
	for _, path := range minimalExecutableHostPaths {
		if pathExists(path) {
			args = append(args, "--ro-bind", path, path)
		}
	}
	for _, mount := range policy.extraMounts {
		flag := "--ro-bind"
		if mount.mode == "rw" {
			flag = "--bind"
		}
		args = append(args, flag, mount.host, mount.target)
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	for _, name := range policy.envNames() {
		args = append(args, "--setenv", name, policy.env[name])
	}
	args = append(args, "--chdir", cwd, "--")
	args = append(args, sandboxConfig.Command.Argv...)
	return args
}

type sandboxPolicy struct {
	beadsPath       string
	codexHostPath   string
	codexTargetPath string
	extraMounts     []resolvedMount
	env             map[string]string
}

type resolvedMount struct {
	host   string
	target string
	mode   string
}

const (
	syntheticHomeParent = "/home"
	syntheticHome       = "/home/orc"
	defaultCodexTarget  = syntheticHome + "/.codex"
)

var defaultEnvAllowlist = []string{
	"PATH",
	"TERM",
	"LANG",
	"SHELL",
	"USER",
	"LOGNAME",
	"CODEX_HOME",
	"OPENAI_API_KEY",
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

func resolvePolicy(root string, sandboxConfig *config.SandboxConfig, opts Options, pathExists func(string) bool) (sandboxPolicy, error) {
	hostEnv := hostEnvMap(opts)
	codexHost, codexTarget, err := resolveCodexHome(hostEnv, opts, pathExists)
	if err != nil {
		return sandboxPolicy{}, err
	}
	policy := sandboxPolicy{
		codexHostPath:   codexHost,
		codexTargetPath: codexTarget,
		extraMounts:     resolveExtraMounts(root, sandboxConfig.Mounts, pathExists),
		env:             resolveEnv(hostEnv, sandboxConfig.Env, codexTarget, root),
	}
	beadsPath := filepath.Clean(filepath.Join(root, "..", ".beads"))
	if pathExists(beadsPath) {
		policy.beadsPath = beadsPath
	}
	return policy, nil
}

func hostEnvMap(opts Options) map[string]string {
	environ := os.Environ
	if opts.Environ != nil {
		environ = opts.Environ
	}
	env := make(map[string]string)
	for _, entry := range environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			env[name] = value
		}
	}
	return env
}

func resolveCodexHome(hostEnv map[string]string, opts Options, pathExists func(string) bool) (string, string, error) {
	if codexHome := hostEnv["CODEX_HOME"]; codexHome != "" {
		if !filepath.IsAbs(codexHome) {
			return "", "", fmt.Errorf("CODEX_HOME %q must be absolute for sandbox mounting", codexHome)
		}
		return filepath.Clean(codexHome), filepath.Clean(codexHome), nil
	}
	home := hostEnv["HOME"]
	if home == "" {
		userHomeDir := os.UserHomeDir
		if opts.UserHomeDir != nil {
			userHomeDir = opts.UserHomeDir
		}
		resolvedHome, err := userHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("resolve home for default CODEX_HOME: %w", err)
		}
		home = resolvedHome
	}
	if home == "" || !filepath.IsAbs(home) {
		return "", "", fmt.Errorf("home %q must be absolute for default CODEX_HOME", home)
	}
	codexHome := filepath.Join(home, ".codex")
	if !pathExists(codexHome) {
		mkdirAll := os.MkdirAll
		if opts.MkdirAll != nil {
			mkdirAll = opts.MkdirAll
		}
		if err := mkdirAll(codexHome, 0o700); err != nil {
			return "", "", fmt.Errorf("create default CODEX_HOME %q: %w", codexHome, err)
		}
	}
	return filepath.Clean(codexHome), defaultCodexTarget, nil
}

func resolveExtraMounts(root string, mounts []config.SandboxMount, pathExists func(string) bool) []resolvedMount {
	resolved := make([]resolvedMount, 0, len(mounts))
	for _, mount := range mounts {
		host := mount.Host
		if !filepath.IsAbs(host) {
			host = filepath.Join(root, host)
		}
		host = filepath.Clean(host)
		if mount.Optional.Value && !pathExists(host) {
			continue
		}
		resolved = append(resolved, resolvedMount{
			host:   host,
			target: filepath.Clean(mount.Target),
			mode:   mount.Mode,
		})
	}
	return resolved
}

func resolveEnv(hostEnv map[string]string, sandboxEnv config.SandboxEnvConfig, codexTarget, root string) map[string]string {
	env := make(map[string]string)
	for _, name := range defaultEnvAllowlist {
		if value, ok := hostEnv[name]; ok {
			env[name] = value
		}
	}
	for name, value := range hostEnv {
		if strings.HasPrefix(name, "LC_") {
			env[name] = value
		}
	}
	for _, name := range sandboxEnv.Pass {
		if value, ok := hostEnv[name]; ok {
			env[name] = value
		}
	}
	maps.Copy(env, sandboxEnv.Set)
	env["HOME"] = syntheticHome
	env["CODEX_HOME"] = codexTarget
	env["ORC_SANDBOX"] = "1"
	env["ORC_SANDBOX_ROOT"] = root
	return env
}

func (p sandboxPolicy) setupDirs(root string) []string {
	dirs := make([]string, 0)
	seen := map[string]bool{"/": true, "/tmp": true}
	appendDir := func(path string) {
		if path == "" || path == "." || seen[path] {
			return
		}
		seen[path] = true
		dirs = append(dirs, path)
	}
	appendDir(syntheticHomeParent)
	appendDir(syntheticHome)
	appendAbsPathDirs(&dirs, seen, root, true)
	if p.beadsPath != "" {
		appendAbsPathDirs(&dirs, seen, p.beadsPath, true)
	}
	appendAbsPathDirs(&dirs, seen, p.codexTargetPath, true)
	for _, mount := range p.extraMounts {
		appendAbsPathDirs(&dirs, seen, filepath.Dir(mount.target), true)
	}
	return dirs
}

func appendAbsPathDirs(dirs *[]string, seen map[string]bool, path string, includePath bool) {
	clean := filepath.Clean(path)
	if clean == "." || !filepath.IsAbs(clean) {
		return
	}
	if !includePath {
		clean = filepath.Dir(clean)
	}
	var current strings.Builder
	for part := range strings.SplitSeq(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		current.WriteString(string(filepath.Separator) + part)
		dir := current.String()
		if dir == string(filepath.Separator) || dir == "/tmp" || seen[dir] {
			continue
		}
		seen[dir] = true
		*dirs = append(*dirs, dir)
	}
}

func (p sandboxPolicy) envNames() []string {
	names := make([]string, 0, len(p.env))
	for name := range p.env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p sandboxPolicy) envList() []string {
	names := p.envNames()
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+p.env[name])
	}
	return env
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
