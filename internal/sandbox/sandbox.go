package sandbox

import (
	"context"
	"encoding/json"
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
	"tiny-llm-orchestrator/orc/internal/stableerr"
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

// RuntimeDirCoverageEnv carries the sandbox-visible mount targets that may
// cover runtime_dirs for worker launches inside an active Orc sandbox.
const RuntimeDirCoverageEnv = "ORC_SANDBOX_RUNTIME_DIR_COVERAGE"

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
		return BwrapSpec{}, stableerr.New("project config is required")
	}
	sandboxConfig := project.Config.Sandbox
	if sandboxConfig == nil {
		return BwrapSpec{}, stableerr.New("sandbox config is required; declare sandbox.command.argv in .orc/config.yaml")
	}
	goos := opts.RuntimeGOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return BwrapSpec{}, stableerr.Errorf("orc sandbox run requires Linux and the system bwrap binary; unsupported platform %s", goos)
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	bwrapPath, err := lookPath("bwrap")
	if err != nil {
		return BwrapSpec{}, stableerr.New("bwrap binary not found; install bubblewrap and ensure bwrap is on PATH")
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
	policy, err := resolvePolicy(project, sandboxConfig, opts, pathExists, stat, evalSymlinks)
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
	beadsPath   string
	homePath    string
	pathMounts  []resolvedMount
	extraMounts []resolvedMount
	env         map[string]string
}

type protectedHostPaths struct {
	paths []string
}

type resolvedMount struct {
	host                     string
	target                   string
	mode                     string
	runtimeID                string
	id                       string
	envSourcedSamePathTarget bool
}

const (
	syntheticHome = "/home/orc"
)

var defaultEnvAllowlist = []string{
	"PATH",
	"TERM",
	"LANG",
	"SHELL",
	"USER",
	"LOGNAME",
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

func resolvePolicy(project *config.Project, sandboxConfig *config.SandboxConfig, opts Options, pathExists func(string) bool, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (sandboxPolicy, error) {
	root := project.Root
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
	protectedPaths, err := resolveProtectedHostPaths(sandboxConfig.ProtectedPaths, hostHome, hostEnv, opts, evalSymlinks)
	if err != nil {
		return sandboxPolicy{}, err
	}
	beadsPath := filepath.Clean(filepath.Join(root, "..", ".beads"))
	sandboxEnv, sandboxMounts, setFromMount := mergeRuntimeSandboxRequirements(project, sandboxConfig)
	extraMounts, resolvedByID, err := resolveExtraMounts(root, beadsPath, home, hostEnv, opts, sandboxMounts, protectedPaths, pathExists, stat, evalSymlinks)
	if err != nil {
		return sandboxPolicy{}, err
	}
	sandboxEnv, err = applyEnvFromMount(sandboxEnv, setFromMount, resolvedByID)
	if err != nil {
		return sandboxPolicy{}, err
	}
	pathMounts, err := resolvePathMounts(root, beadsPath, home, hostHome, effectiveSandboxPath(hostEnv, sandboxEnv), sandboxConfig.Path.Mode, extraMounts, protectedPaths, opts.Stderr, stat, evalSymlinks)
	if err != nil {
		return sandboxPolicy{}, err
	}
	policy := sandboxPolicy{
		homePath:    home,
		pathMounts:  pathMounts,
		extraMounts: extraMounts,
		env:         resolveEnv(hostEnv, sandboxEnv, home, root),
	}
	if pathExists(beadsPath) {
		policy.beadsPath = beadsPath
	}
	policy.env[RuntimeDirCoverageEnv] = runtimeDirCoverageValue(root, extraMounts)
	return policy, nil
}

func resolveProtectedHostPaths(paths []config.SandboxProtectedPath, hostHome string, hostEnv map[string]string, opts Options, evalSymlinks func(string) (string, error)) (protectedHostPaths, error) {
	if len(paths) == 0 {
		return protectedHostPaths{}, nil
	}
	resolved := protectedHostPaths{paths: make([]string, 0, len(paths))}
	seen := map[string]struct{}{}
	for i, protected := range paths {
		entryName := fmt.Sprintf("sandbox.protected_paths[%d]", i)
		literal := protected.Absolute
		if protected.HostHomeSet {
			if hostHome == "" {
				var err error
				hostHome, err = resolveHostHome(hostEnv, opts)
				if err != nil {
					return protectedHostPaths{}, fmt.Errorf("%s.host_home: %w", entryName, err)
				}
			}
			literal = filepath.Join(hostHome, protected.HostHome)
		}
		literal = filepath.Clean(literal)
		resolved.add(literal, seen)
		if !literalHostPathExists(literal) {
			continue
		}
		realPath, err := evalSymlinks(literal)
		if err != nil {
			return protectedHostPaths{}, fmt.Errorf("%s %q: resolve symlinks: %w", entryName, literal, err)
		}
		resolved.add(realPath, seen)
	}
	return resolved, nil
}

func (p *protectedHostPaths) add(path string, seen map[string]struct{}) {
	clean := filepath.Clean(path)
	if _, ok := seen[clean]; ok {
		return
	}
	seen[clean] = struct{}{}
	p.paths = append(p.paths, clean)
}

func (p *protectedHostPaths) conflict(path string) (string, bool) {
	clean := filepath.Clean(path)
	for _, protected := range p.paths {
		if pathIntersects(clean, protected) {
			return protected, true
		}
	}
	return "", false
}

func literalHostPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func runtimeDirCoverageValue(root string, extraMounts []resolvedMount) string {
	targets := make([]string, 0, 1+len(extraMounts))
	targets = append(targets, filepath.Clean(root))
	for _, mount := range extraMounts {
		targets = append(targets, filepath.Clean(mount.target))
	}
	value, err := json.Marshal(targets)
	if err != nil {
		return "[]"
	}
	return string(value)
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
		return "", stableerr.Errorf("host HOME %q must resolve to an absolute path", home)
	}
	return filepath.Clean(home), nil
}

func effectiveSandboxPath(hostEnv map[string]string, sandboxEnv config.SandboxEnvConfig) string {
	if sandboxEnv.Set != nil {
		if path, ok := sandboxEnv.Set["PATH"]; ok {
			return path
		}
	}
	return hostEnv["PATH"]
}

type envFromMountRequirement struct {
	envName   string
	runtimeID string
	mountID   string
	source    string
}

func mergeRuntimeSandboxRequirements(project *config.Project, sandboxConfig *config.SandboxConfig) (config.SandboxEnvConfig, []sourcedSandboxMount, []envFromMountRequirement) {
	env := sandboxConfig.Env
	if env.Set != nil {
		env.Set = maps.Clone(env.Set)
	}
	mounts := make([]sourcedSandboxMount, 0, len(sandboxConfig.Mounts))
	setFromMount := make([]envFromMountRequirement, 0)
	for i, mount := range sandboxConfig.Mounts {
		mounts = append(mounts, sourcedSandboxMount{
			mount:  mount,
			source: fmt.Sprintf("sandbox.mounts[%d]", i),
		})
	}
	for _, runtimeID := range config.SelectedRuntimeIDs(project.Workflows) {
		runtimeConfig, ok := project.Runtimes[runtimeID]
		if !ok {
			continue
		}
		requirements := runtimeConfig.Sandbox.Requirements
		env.Pass = append(env.Pass, requirements.Env.Pass...)
		if len(requirements.Env.Set) > 0 {
			if env.Set == nil {
				env.Set = map[string]string{}
			}
			maps.Copy(env.Set, requirements.Env.Set)
		}
		for i, mount := range requirements.Mounts {
			mounts = append(mounts, sourcedSandboxMount{
				runtimeMount: mount,
				source:       fmt.Sprintf("runtime %q sandbox.requirements.mounts[%d]", runtimeID, i),
				runtimeID:    runtimeID,
				runtime:      true,
			})
		}
		envNames := make([]string, 0, len(requirements.Env.SetFromMount))
		for envName := range requirements.Env.SetFromMount {
			envNames = append(envNames, envName)
		}
		sort.Strings(envNames)
		for _, envName := range envNames {
			ref := requirements.Env.SetFromMount[envName]
			setFromMount = append(setFromMount, envFromMountRequirement{
				envName:   envName,
				runtimeID: runtimeID,
				mountID:   ref.Mount,
				source:    fmt.Sprintf("runtime %q sandbox.requirements.env.set_from_mount[%q]", runtimeID, envName),
			})
		}
	}
	return env, mounts, setFromMount
}

type sourcedSandboxMount struct {
	mount        config.SandboxMount
	runtimeMount config.RuntimeSandboxMount
	source       string
	runtimeID    string
	runtime      bool
}

type runtimeMountKey struct {
	runtimeID string
	mountID   string
}

func resolveExtraMounts(root, beadsPath, sandboxHome string, hostEnv map[string]string, opts Options, mounts []sourcedSandboxMount, protectedPaths protectedHostPaths, pathExists func(string) bool, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) ([]resolvedMount, map[runtimeMountKey]resolvedMount, error) {
	resolved := make([]resolvedMount, 0, len(mounts))
	resolvedByID := map[runtimeMountKey]resolvedMount{}
	for _, sourced := range mounts {
		mount, ok, err := resolveSourcedMount(root, sandboxHome, hostEnv, opts, sourced, protectedPaths, pathExists, stat, evalSymlinks)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		target := filepath.Clean(mount.target)
		mount.target = target
		if err := validateExtraMountTarget(sourced.source, root, beadsPath, sandboxHome, mount); err != nil {
			return nil, nil, err
		}
		conflict := false
		for _, existing := range resolved {
			if existing.target == mount.target && existing.host == mount.host && existing.mode == mount.mode {
				conflict = true
				break
			}
			if pathIntersects(existing.target, mount.target) {
				return nil, nil, stableerr.Errorf("%s.target %q conflicts with explicit sandbox mount target %q", sourced.source, mount.target, existing.target)
			}
		}
		if conflict {
			indexResolvedRuntimeMount(resolvedByID, mount)
			continue
		}
		resolved = append(resolved, mount)
		indexResolvedRuntimeMount(resolvedByID, mount)
	}
	return resolved, resolvedByID, nil
}

func indexResolvedRuntimeMount(resolvedByID map[runtimeMountKey]resolvedMount, mount resolvedMount) {
	if mount.runtimeID != "" && mount.id != "" {
		resolvedByID[runtimeMountKey{runtimeID: mount.runtimeID, mountID: mount.id}] = mount
	}
}

func resolveSourcedMount(root, sandboxHome string, hostEnv map[string]string, opts Options, sourced sourcedSandboxMount, protectedPaths protectedHostPaths, pathExists func(string) bool, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (resolvedMount, bool, error) {
	if sourced.runtime && sourced.runtimeMount.Host == "" {
		return resolveRuntimeSandboxMount(root, sandboxHome, hostEnv, opts, sourced, protectedPaths, pathExists, stat, evalSymlinks)
	}
	return resolveStaticSandboxMount(root, staticSandboxMount(sourced), protectedPaths, pathExists, evalSymlinks)
}

type staticSandboxMountDescriptor struct {
	host      string
	target    string
	mode      string
	optional  bool
	source    string
	runtimeID string
	id        string
}

func staticSandboxMount(sourced sourcedSandboxMount) staticSandboxMountDescriptor {
	if sourced.runtime {
		mount := sourced.runtimeMount
		return staticSandboxMountDescriptor{
			host:      mount.Host,
			target:    mount.Target.Path,
			mode:      mount.Mode,
			optional:  mount.Optional.Value,
			source:    sourced.source,
			runtimeID: sourced.runtimeID,
			id:        mount.ID,
		}
	}
	mount := sourced.mount
	return staticSandboxMountDescriptor{
		host:     mount.Host,
		target:   mount.Target,
		mode:     mount.Mode,
		optional: mount.Optional.Value,
		source:   sourced.source,
	}
}

func resolveStaticSandboxMount(root string, mount staticSandboxMountDescriptor, protectedPaths protectedHostPaths, pathExists func(string) bool, evalSymlinks func(string) (string, error)) (resolvedMount, bool, error) {
	host := mount.host
	if !filepath.IsAbs(host) {
		host = filepath.Join(root, host)
	}
	host = filepath.Clean(host)
	if !pathExists(host) {
		if mount.optional {
			return resolvedMount{}, false, nil
		}
		return resolvedMount{}, false, stableerr.Errorf("%s.host %q does not exist", mount.source, mount.host)
	}
	if protected, ok, err := protectedMountConflict(host, protectedPaths, evalSymlinks); err != nil {
		return resolvedMount{}, false, fmt.Errorf("%s.host %q: %w", mount.source, mount.host, err)
	} else if ok {
		return resolvedMount{}, false, stableerr.Errorf("%s.host %q conflicts with sandbox.protected_paths host path %q", mount.source, host, protected)
	}
	if !filepath.IsAbs(mount.host) && mount.mode == "rw" {
		realRoot, err := evalSymlinks(root)
		if err != nil {
			return resolvedMount{}, false, fmt.Errorf("%s.host %q: resolve repository root: %w", mount.source, mount.host, err)
		}
		realHost, err := evalSymlinks(host)
		if err != nil {
			return resolvedMount{}, false, fmt.Errorf("%s.host %q: %w", mount.source, mount.host, err)
		}
		rel, err := filepath.Rel(realRoot, realHost)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolvedMount{}, false, stableerr.Errorf("%s.host %q must not escape repository root for writable mounts", mount.source, mount.host)
		}
	}
	return resolvedMount{
		host:      host,
		target:    mount.target,
		mode:      mount.mode,
		runtimeID: mount.runtimeID,
		id:        mount.id,
	}, true, nil
}

func resolveRuntimeSandboxMount(root, sandboxHome string, hostEnv map[string]string, opts Options, sourced sourcedSandboxMount, protectedPaths protectedHostPaths, pathExists func(string) bool, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (resolvedMount, bool, error) {
	mount := sourced.runtimeMount
	host, usedFallback, err := resolveRuntimeMountSource(sourced.source, mount, hostEnv, opts)
	if err != nil {
		return resolvedMount{}, false, err
	}
	host = filepath.Clean(host)
	if !pathExists(host) {
		if mount.Optional.Value {
			return resolvedMount{}, false, nil
		}
		if !mount.Source.Create {
			return resolvedMount{}, false, stableerr.Errorf("%s.source resolved path %q does not exist", sourced.source, host)
		}
		if protected, ok := protectedPaths.conflict(host); ok {
			return resolvedMount{}, false, stableerr.Errorf("%s.source %q conflicts with sandbox.protected_paths host path %q", sourced.source, host, protected)
		}
		mkdirAll := os.MkdirAll
		if opts.MkdirAll != nil {
			mkdirAll = opts.MkdirAll
		}
		if err := mkdirAll(host, 0o700); err != nil {
			return resolvedMount{}, false, fmt.Errorf("%s.source create %q: %w", sourced.source, host, err)
		}
	}
	info, err := stat(host)
	if err != nil {
		return resolvedMount{}, false, fmt.Errorf("%s.source %q: %w", sourced.source, host, err)
	}
	if !info.IsDir() {
		return resolvedMount{}, false, stableerr.Errorf("%s.source %q must be a directory", sourced.source, host)
	}
	realHost, err := evalSymlinks(host)
	if err != nil {
		return resolvedMount{}, false, fmt.Errorf("%s.source %q: %w", sourced.source, host, err)
	}
	if protected, ok := protectedPaths.conflict(host); ok {
		return resolvedMount{}, false, stableerr.Errorf("%s.source %q conflicts with sandbox.protected_paths host path %q", sourced.source, host, protected)
	}
	if protected, ok := protectedPaths.conflict(realHost); ok {
		return resolvedMount{}, false, stableerr.Errorf("%s.source %q conflicts with sandbox.protected_paths host path %q", sourced.source, host, protected)
	}
	target := host
	if usedFallback {
		target = filepath.Join(sandboxHome, mount.Target.Fallback.SandboxHome)
	}
	return resolvedMount{
		host:                     filepath.Clean(realHost),
		target:                   filepath.Clean(target),
		mode:                     mount.Mode,
		runtimeID:                sourced.runtimeID,
		id:                       mount.ID,
		envSourcedSamePathTarget: !usedFallback && mount.Target.EnvSameAsSource,
	}, true, nil
}

func resolveRuntimeMountSource(source string, mount config.RuntimeSandboxMount, hostEnv map[string]string, opts Options) (string, bool, error) {
	if value := hostEnv[mount.Source.Env]; filepath.IsAbs(value) {
		return value, false, nil
	}
	if mount.Source.Fallback.HostHome == "" {
		return "", false, stableerr.Errorf("%s.source.env %q is unset, empty, or not absolute and no source fallback is configured", source, mount.Source.Env)
	}
	hostHome, err := resolveHostHome(hostEnv, opts)
	if err != nil {
		return "", false, fmt.Errorf("%s.source.fallback.host_home: %w", source, err)
	}
	return filepath.Join(hostHome, mount.Source.Fallback.HostHome), true, nil
}

func applyEnvFromMount(env config.SandboxEnvConfig, requirements []envFromMountRequirement, resolvedByID map[runtimeMountKey]resolvedMount) (config.SandboxEnvConfig, error) {
	if len(requirements) == 0 {
		return env, nil
	}
	if env.Set == nil {
		env.Set = map[string]string{}
	}
	for _, requirement := range requirements {
		mount, ok := resolvedByID[runtimeMountKey{runtimeID: requirement.runtimeID, mountID: requirement.mountID}]
		if !ok {
			return env, stableerr.Errorf("%s references unresolved mount id %q", requirement.source, requirement.mountID)
		}
		if existing, ok := env.Set[requirement.envName]; ok && existing != mount.target {
			return env, stableerr.Errorf("%s conflicts with another sandbox environment value for %s", requirement.source, requirement.envName)
		}
		env.Set[requirement.envName] = mount.target
	}
	return env, nil
}

func protectedMountConflict(host string, protectedPaths protectedHostPaths, evalSymlinks func(string) (string, error)) (string, bool, error) {
	if protected, ok := protectedPaths.conflict(host); ok {
		return protected, true, nil
	}
	if !literalHostPathExists(host) {
		return "", false, nil
	}
	realHost, err := evalSymlinks(host)
	if err != nil {
		return "", false, fmt.Errorf("resolve symlinks: %w", err)
	}
	if protected, ok := protectedPaths.conflict(realHost); ok {
		return protected, true, nil
	}
	return "", false, nil
}

func resolvePathMounts(root, beadsPath, sandboxHome, hostHome, pathValue, mode string, explicitMounts []resolvedMount, protectedPaths protectedHostPaths, stderr io.Writer, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) ([]resolvedMount, error) {
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
		if protected, ok := protectedPaths.conflict(target); ok {
			warnProtectedPathMount(stderr, target, protected)
			continue
		}
		if protected, ok := protectedPaths.conflict(resolvedSource); ok {
			warnProtectedPathMount(stderr, target, protected)
			continue
		}
		for _, explicit := range explicitMounts {
			if explicit.target == target {
				return nil, stableerr.Errorf("sandbox.path.mode host_entries generated mount target %q conflicts with explicit sandbox mount target %q; explicit mounts cannot override automatic PATH mounts", target, explicit.target)
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

func warnProtectedPathMount(stderr io.Writer, target, protected string) {
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "warning: sandbox.path.mode host_entries skipped PATH entry %q because it conflicts with sandbox.protected_paths host path %q\n", target, protected)
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
		return stableerr.Errorf("unsafe PATH entry %q: must not mount active sandbox HOME %s", target, sandboxHome)
	}
	if isStrictPathAncestor(target, sandboxHome) {
		return stableerr.Errorf("unsafe PATH entry %q: must not mount ancestor of active sandbox HOME %s", target, sandboxHome)
	}
	if target == hostHome {
		return stableerr.Errorf("unsafe PATH entry %q: must not mount resolved host HOME %s", target, hostHome)
	}
	if isStrictPathAncestor(target, hostHome) {
		return stableerr.Errorf("unsafe PATH entry %q: must not mount ancestor of resolved host HOME %s", target, hostHome)
	}
	if filepath.IsAbs(root) && pathIntersects(target, filepath.Clean(root)) {
		return stableerr.Errorf("unsafe PATH entry %q: must not override the repository mount %s", target, filepath.Clean(root))
	}
	if beadsPath != "" && pathIntersects(target, filepath.Clean(beadsPath)) {
		return stableerr.Errorf("unsafe PATH entry %q: must not override the Beads mount %s", target, filepath.Clean(beadsPath))
	}
	if protected, ok := config.ProtectedSandboxTargetConflict(target); ok {
		return stableerr.Errorf("unsafe PATH entry %q: must not override protected sandbox path %s", target, protected)
	}
	return nil
}

func pathIntersects(a, b string) bool {
	return a == b || isStrictPathAncestor(a, b) || isStrictPathAncestor(b, a)
}

func validateExtraMountTarget(source, root, beadsPath, sandboxHome string, mount resolvedMount) error {
	target := mount.target
	name := fmt.Sprintf("%s.target %q", source, target)
	if target == sandboxHome {
		return stableerr.Errorf("%s: must not override active sandbox HOME %s", name, sandboxHome)
	}
	if isStrictPathAncestor(target, sandboxHome) {
		return stableerr.Errorf("%s: must not override ancestor of active sandbox HOME %s", name, sandboxHome)
	}
	if isStrictPathAncestor(sandboxHome, target) {
		return nil
	}
	if strings.HasPrefix(target, "/home/") || target == "/home" {
		if !mount.envSourcedSamePathTarget || target == "/home" {
			return stableerr.Errorf("%s: must not override critical sandbox path /home", name)
		}
	}
	if filepath.IsAbs(root) && pathIntersects(target, filepath.Clean(root)) {
		return stableerr.Errorf("%s: must not override the repository mount %s", name, filepath.Clean(root))
	}
	if filepath.IsAbs(beadsPath) && pathIntersects(target, filepath.Clean(beadsPath)) {
		return stableerr.Errorf("%s: must not override the Beads mount %s", name, filepath.Clean(beadsPath))
	}
	if protected, ok := config.ProtectedSandboxTargetConflict(target); ok {
		return stableerr.Errorf("%s: must not override protected sandbox path %s", name, protected)
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

func resolveEnv(hostEnv map[string]string, sandboxEnv config.SandboxEnvConfig, home, root string) map[string]string {
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
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...) // #nosec G204 -- bwrap path is resolved from PATH and argv is validated config.
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
		return fmt.Errorf("run spec: %w", ctx.Err())
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
