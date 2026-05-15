package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadAcceptsMinimalSandboxConfig(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
sandbox:
  command:
    argv: ["codex"]
`})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if project.Config.Sandbox == nil {
		t.Fatal("sandbox config was nil")
	}
	if got, want := project.Config.Sandbox.Command.Argv, []string{"codex"}; !slices.Equal(got, want) {
		t.Fatalf("sandbox command argv = %v, want %v", got, want)
	}
	if got := project.Config.Sandbox.CWD; got != "." {
		t.Fatalf("sandbox cwd = %q, want .", got)
	}
	if got := project.Config.Sandbox.Bubblewrap.Network; !got.Set || !got.Value {
		t.Fatalf("sandbox bubblewrap network = %+v, want default true", got)
	}
	if project.Config.Sandbox.RequireForWorkers {
		t.Fatal("sandbox require_for_workers = true, want default false")
	}
	if got := project.Config.Sandbox.Home.Mode; got != SandboxHomeModeSynthetic {
		t.Fatalf("sandbox home mode = %q, want %q", got, SandboxHomeModeSynthetic)
	}
	if got := project.Config.Sandbox.Path.Mode; got != SandboxPathModeNone {
		t.Fatalf("sandbox path mode = %q, want %q", got, SandboxPathModeNone)
	}
	if got := project.Config.Sandbox.ProtectedPaths; len(got) != 0 {
		t.Fatalf("sandbox protected paths = %v, want empty default", got)
	}
}

func TestLoadAcceptsSandboxProtectedPaths(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []SandboxProtectedPath
	}{
		{
			name: "empty list",
			yaml: "[]",
			want: []SandboxProtectedPath{},
		},
		{
			name: "host home and absolute",
			yaml: `[
    {host_home: .ssh},
    {host_home: .config/tool/secrets},
    {absolute: /var/lib/orc/secrets}
  ]`,
			want: []SandboxProtectedPath{
				{HostHome: ".ssh", HostHomeSet: true},
				{HostHome: ".config/tool/secrets", HostHomeSet: true},
				{Absolute: "/var/lib/orc/secrets", AbsoluteSet: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{config: `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
sandbox:
  command:
    argv: ["codex"]
  protected_paths: ` + tt.yaml + `
`})

			project, err := Load(root)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			got := project.Config.Sandbox.ProtectedPaths
			if len(got) != len(tt.want) {
				t.Fatalf("protected paths length = %d, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i].HostHome != tt.want[i].HostHome || got[i].HostHomeSet != tt.want[i].HostHomeSet ||
					got[i].Absolute != tt.want[i].Absolute || got[i].AbsoluteSet != tt.want[i].AbsoluteSet {
					t.Fatalf("protected paths[%d] = %#v, want %#v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadAcceptsFullSandboxConfig(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: readConfigTestdata(t, "full_sandbox_config.yaml")})
	if err := os.Mkdir(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatalf("create tools dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	sandbox := project.Config.Sandbox
	if sandbox == nil {
		t.Fatal("sandbox config was nil")
	}
	if got := sandbox.CWD; got != "tools" {
		t.Fatalf("sandbox cwd = %q, want tools", got)
	}
	if !sandbox.Bubblewrap.Enabled {
		t.Fatal("sandbox bubblewrap enabled = false, want true")
	}
	if !sandbox.RequireForWorkers {
		t.Fatal("sandbox require_for_workers = false, want true")
	}
	if got := sandbox.Home.Mode; got != SandboxHomeModeHostPath {
		t.Fatalf("sandbox home mode = %q, want host_path", got)
	}
	if got := sandbox.Path.Mode; got != SandboxPathModeHostEntries {
		t.Fatalf("sandbox path mode = %q, want host_entries", got)
	}
	if got := sandbox.Bubblewrap.Network; !got.Set || got.Value {
		t.Fatalf("sandbox bubblewrap network = %+v, want explicit false", got)
	}
	if got := sandbox.Bubblewrap.Mounts.Beads; got != "auto" {
		t.Fatalf("sandbox beads mount = %q, want auto", got)
	}
	if got := sandbox.Env.Pass; !slices.Equal(got, []string{"TERM"}) {
		t.Fatalf("sandbox env pass = %v, want TERM", got)
	}
	if got := sandbox.Env.Set["ORC_SANDBOX"]; got != "1" {
		t.Fatalf("sandbox env set ORC_SANDBOX = %q, want 1", got)
	}
	if got := len(sandbox.Mounts); got != 2 {
		t.Fatalf("sandbox mounts length = %d, want 2", got)
	}
}

func TestLoadRejectsInvalidSandboxConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		prepare  func(t *testing.T, root string)
		contains []string
	}{
		{
			name: "missing command argv",
			config: `sandbox:
  command: {}`,
			contains: []string{"sandbox.command.argv must declare at least one argument"},
		},
		{
			name: "empty argv element",
			config: `sandbox:
  command:
    argv: ["codex", ""]`,
			contains: []string{"sandbox.command.argv[1] is empty"},
		},
		{
			name: "shell string command",
			config: `sandbox:
  command: "codex --dangerously-bypass-approvals-and-sandbox"`,
			contains: []string{"sandbox.command must use argv", "shell-string commands are not supported"},
		},
		{
			name: "invalid home mode",
			config: `sandbox:
  command:
    argv: ["codex"]
  home:
    mode: real_home`,
			contains: []string{`sandbox.home.mode "real_home" is invalid; allowed: synthetic, host_path`},
		},
		{
			name: "invalid path mode",
			config: `sandbox:
  command:
    argv: ["codex"]
  path:
    mode: all_host`,
			contains: []string{`sandbox.path.mode "all_host" is invalid; allowed: none, host_entries`},
		},
		{
			name: "bare string protected path",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - .ssh`,
			contains: []string{`sandbox.protected_paths[0] must be an object with exactly one of host_home or absolute`},
		},
		{
			name: "protected path with both forms",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: .ssh
      absolute: /home/user/.ssh`,
			contains: []string{`sandbox.protected_paths[0] must set exactly one of host_home or absolute`},
		},
		{
			name: "protected path with neither form",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - {}`,
			contains: []string{`sandbox.protected_paths[0] must set exactly one of host_home or absolute`},
		},
		{
			name: "empty protected host home",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: ""`,
			contains: []string{`sandbox.protected_paths[0].host_home is empty`},
		},
		{
			name: "dot protected host home",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: .`,
			contains: []string{`sandbox.protected_paths[0].host_home "." must be a clean relative descendant path`},
		},
		{
			name: "absolute protected host home",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: /home/user/.ssh`,
			contains: []string{`sandbox.protected_paths[0].host_home "/home/user/.ssh" must be relative`},
		},
		{
			name: "traversing protected host home",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: ../.ssh`,
			contains: []string{`sandbox.protected_paths[0].host_home "../.ssh" must be a clean relative descendant path`},
		},
		{
			name: "unclean protected host home separator",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: .config//tool`,
			contains: []string{`sandbox.protected_paths[0].host_home ".config//tool" must be a clean relative descendant path`},
		},
		{
			name: "unclean protected host home traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: .config/../.ssh`,
			contains: []string{`sandbox.protected_paths[0].host_home ".config/../.ssh" must be a clean relative descendant path`},
		},
		{
			name: "protected host home environment expansion",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: $CUSTOM_HOME/.ssh`,
			contains: []string{`sandbox.protected_paths[0].host_home "$CUSTOM_HOME/.ssh" must not use shell, environment, or tilde expansion`},
		},
		{
			name: "protected host home tilde expansion",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - host_home: ~/.ssh`,
			contains: []string{`sandbox.protected_paths[0].host_home "~/.ssh" must not use shell, environment, or tilde expansion`},
		},
		{
			name: "empty protected absolute",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: ""`,
			contains: []string{`sandbox.protected_paths[0].absolute is empty`},
		},
		{
			name: "root protected absolute",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: /`,
			contains: []string{`sandbox.protected_paths[0].absolute "/" must not be root`},
		},
		{
			name: "relative protected absolute",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: .ssh`,
			contains: []string{`sandbox.protected_paths[0].absolute ".ssh" must be absolute`},
		},
		{
			name: "unclean protected absolute",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: /var//secrets`,
			contains: []string{`sandbox.protected_paths[0].absolute "/var//secrets" must be clean`},
		},
		{
			name: "protected absolute tilde expansion",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: ~/.ssh`,
			contains: []string{`sandbox.protected_paths[0].absolute "~/.ssh" must not use shell, environment, or tilde expansion`},
		},
		{
			name: "protected absolute environment expansion",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: $CUSTOM_HOME/.ssh`,
			contains: []string{`sandbox.protected_paths[0].absolute "$CUSTOM_HOME/.ssh" must not use shell, environment, or tilde expansion`},
		},
		{
			name: "protected absolute command substitution",
			config: `sandbox:
  command:
    argv: ["codex"]
  protected_paths:
    - absolute: $(pwd)/secrets`,
			contains: []string{`sandbox.protected_paths[0].absolute "$(pwd)/secrets" must not use shell, environment, or tilde expansion`},
		},
		{
			name:     "protected absolute backtick command substitution",
			config:   "sandbox:\n  command:\n    argv: [\"codex\"]\n  protected_paths:\n    - absolute: '`pwd`/secrets'",
			contains: []string{"sandbox.protected_paths[0].absolute \"`pwd`/secrets\" must not use shell, environment, or tilde expansion"},
		},
		{
			name: "absolute cwd",
			config: `sandbox:
  command:
    argv: ["codex"]
  cwd: /tmp`,
			contains: []string{`sandbox.cwd "/tmp" must be repo-relative`},
		},
		{
			name: "traversing cwd",
			config: `sandbox:
  command:
    argv: ["codex"]
  cwd: ../outside`,
			contains: []string{`sandbox.cwd "../outside" must be clean and stay under repository root`},
		},
		{
			name: "symlink escaping cwd",
			config: `sandbox:
  command:
    argv: ["codex"]
  cwd: linked-outside`,
			prepare: func(t *testing.T, root string) {
				t.Helper()
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(root, "linked-outside")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			contains: []string{`sandbox.cwd "linked-outside"`, "path must not escape repository root"},
		},
		{
			name: "invalid extra mount mode",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: /workspace
      mode: write`,
			contains: []string{`sandbox.mounts[0].mode "write" is invalid; allowed: ro, rw`},
		},
		{
			name: "missing required mount",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: missing
      target: /workspace/missing
      mode: ro`,
			contains: []string{`sandbox.mounts[0].host "missing" does not exist`},
		},
		{
			name: "invalid preset mount mode",
			config: `sandbox:
  command:
    argv: ["codex"]
  bubblewrap:
    mounts:
      repo: auto`,
			contains: []string{`sandbox.bubblewrap.mounts.repo "auto" is invalid; allowed: ro, rw`},
		},
		{
			name: "unsafe writable mount traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: ../outside
      target: /outside
      mode: rw`,
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, "..", "outside"), 0o755); err != nil {
					t.Fatalf("create outside dir: %v", err)
				}
			},
			contains: []string{`sandbox.mounts[0].host "../outside" must not traverse outside repository root for writable mounts`},
		},
		{
			name: "unsafe optional writable mount traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: ../outside
      target: /outside
      mode: rw
      optional: true`,
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, "..", "outside"), 0o755); err != nil {
					t.Fatalf("create outside dir: %v", err)
				}
			},
			contains: []string{`sandbox.mounts[0].host "../outside" must not traverse outside repository root for writable mounts`},
		},
		{
			name: "unsafe missing optional writable mount traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: ../missing-outside
      target: /outside
      mode: rw
      optional: true`,
			contains: []string{`sandbox.mounts[0].host "../missing-outside" must not traverse outside repository root for writable mounts`},
		},
		{
			name: "unsafe writable mount symlink",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: linked-outside
      target: /outside
      mode: rw`,
			prepare: func(t *testing.T, root string) {
				t.Helper()
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(root, "linked-outside")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			contains: []string{`sandbox.mounts[0].host "linked-outside" must not escape repository root for writable mounts`},
		},
		{
			name: "empty env pass entry",
			config: `sandbox:
  command:
    argv: ["codex"]
  env:
    pass: ["TERM", ""]`,
			contains: []string{`sandbox.env.pass[1]: environment variable name is empty`},
		},
		{
			name: "invalid env pass entry",
			config: `sandbox:
  command:
    argv: ["codex"]
  env:
    pass: ["BAD-NAME"]`,
			contains: []string{`sandbox.env.pass[0]: environment variable name "BAD-NAME" is invalid`},
		},
		{
			name: "relative extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: workspace
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "workspace": must be an absolute sandbox path`},
		},
		{
			name: "literal dollar home extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: $HOME/.bun
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "$HOME/.bun": must be an absolute sandbox path`},
		},
		{
			name: "literal braced home extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: ${HOME}/.bun
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "${HOME}/.bun": must be an absolute sandbox path`},
		},
		{
			name: "literal tilde extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: ~/.bun
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "~/.bun": must be an absolute sandbox path`},
		},
		{
			name: "literal command substitution extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: $(which codex)
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "$(which codex)": must be an absolute sandbox path`},
		},
		{
			name:     "literal backtick extra mount target",
			config:   "sandbox:\n  command:\n    argv: [\"codex\"]\n  mounts:\n    - host: .\n      target: '`which codex`'\n      mode: ro",
			contains: []string{"sandbox.mounts[0].target \"`which codex`\": must be an absolute sandbox path"},
		},
		{
			name: "critical extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: /tmp/cache
      mode: rw`,
			contains: []string{`sandbox.mounts[0].target "/tmp/cache": must not override critical sandbox path /tmp`},
		},
		{
			name: "critical extra mount parent target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: /nix
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "/nix": must not override critical sandbox path /nix/store`},
		},
		{
			name: "synthetic home root extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: /home/orc
      mode: rw`,
			contains: []string{`sandbox.mounts[0].target "/home/orc": must not override critical sandbox path /home/orc`},
		},
		{
			name: "synthetic mode rejects other home target",
			config: `sandbox:
  command:
    argv: ["codex"]
  home:
    mode: synthetic
  mounts:
    - host: .
      target: /home/user/.bun
      mode: ro`,
			contains: []string{`sandbox.mounts[0].target "/home/user/.bun": must not override critical sandbox path /home`},
		},
		{
			name: "repo extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: REPO_PLACEHOLDER/cache
      mode: rw`,
			prepare: func(t *testing.T, root string) {
				t.Helper()
				configPath := filepath.Join(root, ".orc", "config.yaml")
				content, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content = []byte(strings.ReplaceAll(string(content), "REPO_PLACEHOLDER", root))
				if err := os.WriteFile(configPath, content, 0o644); err != nil {
					t.Fatalf("write config: %v", err)
				}
			},
			contains: []string{`must not override the repository mount`},
		},
		{
			name: "repo parent extra mount target",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: REPO_PARENT_PLACEHOLDER
      mode: ro`,
			prepare: func(t *testing.T, root string) {
				t.Helper()
				configPath := filepath.Join(root, ".orc", "config.yaml")
				content, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content = []byte(strings.ReplaceAll(string(content), "REPO_PARENT_PLACEHOLDER", filepath.Dir(root)))
				if err := os.WriteFile(configPath, content, 0o644); err != nil {
					t.Fatalf("write config: %v", err)
				}
			},
			contains: []string{`must not override the repository mount`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{config: configWithSandbox(tt.config)})
			if tt.prepare != nil {
				tt.prepare(t, root)
			}
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadAcceptsHostPathHomeSandboxMount(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: configWithSandbox(`sandbox:
  command:
    argv: ["codex"]
  home:
    mode: host_path
  mounts:
    - host: data
      target: /home/user/.bun
      mode: rw`)})
	if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	if _, err := Load(root); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestLoadSkipsMissingOptionalSandboxMount(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: configWithSandbox(`sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: missing
      target: /workspace/missing
      mode: rw
      optional: true`)})

	if _, err := Load(root); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestLoadRejectsEscapingAgentPath(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{
		config: "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: ../planner.md\n",
	})

	assertLoadErrorContains(t, root, "path must not escape .orc")
}

func TestLoadAcceptsSyntheticHomeSandboxMount(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: configWithSandbox(`sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: data
      target: /home/orc/.config/tool
      mode: rw`)})
	if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	if _, err := Load(root); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestLoadAcceptsDocumentedOptionalSandboxMount(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: configWithSandbox(documentedSandboxConfig(t))})

	if _, err := Load(root); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestLoadRejectsSymlinkEscapingOrc(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{})
	orcDir := filepath.Join(root, ".orc")
	outsideAgent := filepath.Join(root, "outside-agent.md")
	writeFile(t, outsideAgent, `---
id: planner
role: planner
description: Escapes the .orc directory.
---

This descriptor is outside .orc.
`)
	if err := os.Symlink(outsideAgent, filepath.Join(orcDir, "agents", "planner.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	assertLoadErrorContains(t, root, "path must not escape .orc")
}

func configWithSandbox(sandbox string) string {
	return "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n" + sandbox + "\n"
}

func documentedSandboxConfig(t *testing.T) string {
	t.Helper()

	docPath := filepath.Join("..", "..", "docs", "reference", "configuration-project.md")
	content, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read project configuration reference: %v", err)
	}

	const intro = "Project config may also declare an Orc-managed sandbox command contract:"
	afterIntro, ok := cutAfter(string(content), intro)
	if !ok {
		t.Fatalf("project configuration reference missing sandbox sample intro %q", intro)
	}

	const fence = "```yaml\n"
	afterFence, ok := cutAfter(afterIntro, fence)
	if !ok {
		t.Fatal("project configuration reference sandbox sample is missing opening YAML fence")
	}
	sample, _, ok := strings.Cut(afterFence, "\n```")
	if !ok {
		t.Fatal("project configuration reference sandbox sample is missing closing YAML fence")
	}
	return sample
}

func cutAfter(s, sep string) (string, bool) {
	_, after, ok := strings.Cut(s, sep)
	if !ok {
		return "", false
	}
	return after, true
}
