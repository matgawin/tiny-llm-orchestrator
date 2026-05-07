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

	RuntimeGOOS  string
	LookPath     func(string) (string, error)
	PathExists   func(string) bool
	Stat         func(string) (os.FileInfo, error)
	EvalSymlinks func(string) (string, error)
	Environ      func() []string
	UserHomeDir  func() (string, error)
	MkdirAll     func(string, os.FileMode) error
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
	stat := opts.Stat
	if stat == nil {
		stat = os.Stat
	}
	evalSymlinks := opts.EvalSymlinks
	if evalSymlinks == nil {
		evalSymlinks = filepath.EvalSymlinks
	}
	policy, err := resolvePolicy(project.Root, sandboxConfig, opts, pathExists, stat, evalSymlinks)
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
		"--unshare-all",
		"--dir", "/var",
		"--dir", "/run",
		"--dir", "/usr/bin",
		"--dir", "/bin",
	}
	if sandboxConfig.Bubblewrap.Network.Value {
		args = append(args, "--share-net")
	} else {
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
	for _, mount := range policy.pathMounts {
		args = append(args, "--ro-bind", mount.host, mount.target)
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
	homePath        string
	codexHostPath   string
	codexTargetPath string
	pathMounts      []resolvedMount
	extraMounts     []resolvedMount
	env             map[string]string
}

type resolvedMount struct {
	host   string
	target string
	mode   string
}

const (
	syntheticHome      = "/home/orc"
	defaultCodexTarget = syntheticHome + "/.codex"
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

func resolvePolicy(root string, sandboxConfig *config.SandboxConfig, opts Options, pathExists func(string) bool, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (sandboxPolicy, error) {
	hostEnv := hostEnvMap(opts)
	home, err := resolveSandboxHome(sandboxConfig.Home.Mode, hostEnv, opts)
	if err != nil {
		return sandboxPolicy{}, err
	}
	hostHome := ""
	if sandboxConfig.Path.Mode == config.SandboxPathModeHostEntries {
		hostHome, err = resolveHostHome(hostEnv, opts)
		if err != nil {
			return sandboxPolicy{}, fmt.Errorf("resolve host HOME for sandbox path policy: %w", err)
		}
	}
	codexHost, codexTarget, err := resolveCodexHome(sandboxConfig.Home.Mode, home, hostEnv, opts, pathExists)
	if err != nil {
		return sandboxPolicy{}, err
	}
	beadsPath := filepath.Clean(filepath.Join(root, "..", ".beads"))
	extraMounts, err := resolveExtraMounts(root, home, sandboxConfig.Mounts, pathExists)
	if err != nil {
		return sandboxPolicy{}, err
	}
	pathMounts, err := resolvePathMounts(root, beadsPath, home, hostHome, effectiveSandboxPath(hostEnv, sandboxConfig.Env), sandboxConfig.Path.Mode, extraMounts, stat, evalSymlinks)
	if err != nil {
		return sandboxPolicy{}, err
	}
	policy := sandboxPolicy{
		homePath:        home,
		codexHostPath:   codexHost,
		codexTargetPath: codexTarget,
		pathMounts:      pathMounts,
		extraMounts:     extraMounts,
		env:             resolveEnv(hostEnv, sandboxConfig.Env, home, codexTarget, root),
	}
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

func resolveSandboxHome(mode string, hostEnv map[string]string, opts Options) (string, error) {
	if mode == config.SandboxHomeModeHostPath {
		home, err := resolveHostHome(hostEnv, opts)
		if err != nil {
			return "", fmt.Errorf("resolve host HOME for sandbox home mode host_path: %w", err)
		}
		return home, nil
	}
	return syntheticHome, nil
}

func resolveHostHome(hostEnv map[string]string, opts Options) (string, error) {
	home := hostEnv["HOME"]
	if home == "" {
		userHomeDir := os.UserHomeDir
		if opts.UserHomeDir != nil {
			userHomeDir = opts.UserHomeDir
		}
		resolvedHome, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve host HOME: %w", err)
		}
		home = resolvedHome
	}
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("host HOME %q must resolve to an absolute path", home)
	}
	return filepath.Clean(home), nil
}

func resolveCodexHome(mode, sandboxHome string, hostEnv map[string]string, opts Options, pathExists func(string) bool) (string, string, error) {
	if codexHome := hostEnv["CODEX_HOME"]; codexHome != "" {
		if !filepath.IsAbs(codexHome) {
			return "", "", fmt.Errorf("CODEX_HOME %q must be absolute for sandbox mounting", codexHome)
		}
		return filepath.Clean(codexHome), filepath.Clean(codexHome), nil
	}
	home, err := resolveHostHome(hostEnv, opts)
	if err != nil {
		return "", "", fmt.Errorf("resolve home for default CODEX_HOME: %w", err)
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
	target := defaultCodexTarget
	if mode == config.SandboxHomeModeHostPath {
		target = filepath.Join(sandboxHome, ".codex")
	}
	return filepath.Clean(codexHome), filepath.Clean(target), nil
}

func effectiveSandboxPath(hostEnv map[string]string, sandboxEnv config.SandboxEnvConfig) string {
	if sandboxEnv.Set != nil {
		if path, ok := sandboxEnv.Set["PATH"]; ok {
			return path
		}
	}
	return hostEnv["PATH"]
}

func resolveExtraMounts(root, sandboxHome string, mounts []config.SandboxMount, pathExists func(string) bool) ([]resolvedMount, error) {
	resolved := make([]resolvedMount, 0, len(mounts))
	for i, mount := range mounts {
		host := mount.Host
		if !filepath.IsAbs(host) {
			host = filepath.Join(root, host)
		}
		host = filepath.Clean(host)
		if mount.Optional.Value && !pathExists(host) {
			continue
		}
		target := filepath.Clean(mount.Target)
		if err := validateActiveHomeMountTarget(i, sandboxHome, target); err != nil {
			return nil, err
		}
		resolved = append(resolved, resolvedMount{
			host:   host,
			target: target,
			mode:   mount.Mode,
		})
	}
	return resolved, nil
}

func resolvePathMounts(root, beadsPath, sandboxHome, hostHome, pathValue, mode string, explicitMounts []resolvedMount, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) ([]resolvedMount, error) {
	if mode == "" || mode == config.SandboxPathModeNone {
		return nil, nil
	}
	pathMounts := make([]resolvedMount, 0)
	seenTargets := make(map[string]struct{})
	seenPairs := make(map[string]struct{})
	for _, entry := range filepath.SplitList(pathValue) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		target := filepath.Clean(entry)
		info, err := stat(target)
		if err != nil {
			continue
		}
		resolvedSource, err := evalSymlinks(target)
		if err != nil {
			continue
		}
		resolvedInfo, err := stat(resolvedSource)
		if err != nil || !resolvedInfo.IsDir() || !info.IsDir() {
			continue
		}
		if pathUnderExistingMount(target, root) || pathUnderExistingMount(target, beadsPath) || pathUnderMinimalExecutableMount(target, stat) {
			continue
		}
		if err := validatePathMountTarget(root, beadsPath, sandboxHome, hostHome, target); err != nil {
			return nil, err
		}
		for _, explicit := range explicitMounts {
			if explicit.target == target {
				return nil, fmt.Errorf("sandbox.path.mode host_entries generated mount target %q conflicts with explicit sandbox mount target %q; explicit mounts cannot override automatic PATH mounts", target, explicit.target)
			}
		}
		pairKey := filepath.Clean(resolvedSource) + "\x00" + target
		if _, ok := seenTargets[target]; ok {
			continue
		}
		if _, ok := seenPairs[pairKey]; ok {
			continue
		}
		seenTargets[target] = struct{}{}
		seenPairs[pairKey] = struct{}{}
		pathMounts = append(pathMounts, resolvedMount{
			host:   filepath.Clean(resolvedSource),
			target: target,
			mode:   "ro",
		})
	}
	return pathMounts, nil
}

func pathUnderExistingMount(target, mountRoot string) bool {
	if mountRoot == "" || !filepath.IsAbs(mountRoot) {
		return false
	}
	return isStrictPathAncestor(filepath.Clean(mountRoot), target)
}

func pathUnderMinimalExecutableMount(target string, stat func(string) (os.FileInfo, error)) bool {
	for _, mountRoot := range minimalExecutableHostPaths {
		if !isStrictPathAncestor(mountRoot, target) {
			continue
		}
		if _, err := stat(mountRoot); err == nil {
			return true
		}
	}
	return false
}

func validatePathMountTarget(root, beadsPath, sandboxHome, hostHome, target string) error {
	if target == sandboxHome {
		return fmt.Errorf("unsafe PATH entry %q: must not mount active sandbox HOME %s", target, sandboxHome)
	}
	if isStrictPathAncestor(target, sandboxHome) {
		return fmt.Errorf("unsafe PATH entry %q: must not mount ancestor of active sandbox HOME %s", target, sandboxHome)
	}
	if target == hostHome {
		return fmt.Errorf("unsafe PATH entry %q: must not mount resolved host HOME %s", target, hostHome)
	}
	if isStrictPathAncestor(target, hostHome) {
		return fmt.Errorf("unsafe PATH entry %q: must not mount ancestor of resolved host HOME %s", target, hostHome)
	}
	if filepath.IsAbs(root) && pathIntersects(target, filepath.Clean(root)) {
		return fmt.Errorf("unsafe PATH entry %q: must not override the repository mount %s", target, filepath.Clean(root))
	}
	if beadsPath != "" && pathIntersects(target, filepath.Clean(beadsPath)) {
		return fmt.Errorf("unsafe PATH entry %q: must not override the Beads mount %s", target, filepath.Clean(beadsPath))
	}
	for _, protected := range []string{"/proc", "/dev", "/tmp"} {
		if target == protected || strings.HasPrefix(target, protected+string(filepath.Separator)) || isStrictPathAncestor(target, protected) {
			return fmt.Errorf("unsafe PATH entry %q: must not override protected sandbox path %s", target, protected)
		}
	}
	for _, protected := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/nix/store"} {
		if target == protected || isStrictPathAncestor(target, protected) {
			return fmt.Errorf("unsafe PATH entry %q: must not override protected sandbox path %s", target, protected)
		}
	}
	return nil
}

func pathIntersects(a, b string) bool {
	return a == b || isStrictPathAncestor(a, b) || isStrictPathAncestor(b, a)
}

func validateActiveHomeMountTarget(index int, sandboxHome, target string) error {
	name := fmt.Sprintf("sandbox.mounts[%d].target %q", index, target)
	if target == sandboxHome {
		return fmt.Errorf("%s: must not override active sandbox HOME %s", name, sandboxHome)
	}
	if isStrictPathAncestor(target, sandboxHome) {
		return fmt.Errorf("%s: must not override ancestor of active sandbox HOME %s", name, sandboxHome)
	}
	if isStrictPathAncestor(sandboxHome, target) {
		return nil
	}
	if strings.HasPrefix(target, "/home/") || target == "/home" {
		return fmt.Errorf("%s: must not override critical sandbox path /home", name)
	}
	return nil
}

func isStrictPathAncestor(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveEnv(hostEnv map[string]string, sandboxEnv config.SandboxEnvConfig, home, codexTarget, root string) map[string]string {
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
	env["HOME"] = home
	env["CODEX_HOME"] = codexTarget
	env["ORC_SANDBOX"] = "1"
	env["ORC_SANDBOX_ROOT"] = root
	return env
}

func (p sandboxPolicy) setupDirs(root string) []string {
	dirs := make([]string, 0)
	seen := map[string]bool{"/": true, "/tmp": true}
	appendAbsPathDirs(&dirs, seen, p.homePath, true)
	appendAbsPathDirs(&dirs, seen, root, true)
	if p.beadsPath != "" {
		appendAbsPathDirs(&dirs, seen, p.beadsPath, true)
	}
	appendAbsPathDirs(&dirs, seen, p.codexTargetPath, true)
	for _, mount := range p.pathMounts {
		appendAbsPathDirs(&dirs, seen, mount.target, false)
	}
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
	if err := syscall.Kill(pid, syscall.SIGTERM); err == nil {
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
