package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
)

const (
	testBwrapPath      = "/usr/bin/bwrap"
	testCustomHomePath = "/opt/custom-home"
	testHomePath       = "/home/user"
)

func TestBuildSpecCreatesHomeDirsBeforeRepoBind(t *testing.T) {
	root := "/home/tester/project"
	codexHome := "/home/tester/.codex"
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS:  "linux",
		LookPath:     foundBwrap,
		PathExists:   onlyHostPaths(codexHome),
		Stat:         allPathsAreDirs,
		EvalSymlinks: identityEvalSymlinks,
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=/home/tester", "CODEX_HOME=" + codexHome}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	repoBind := indexSequence(spec.Args, "--bind", root, root)
	homeDir := indexSequence(spec.Args, "--dir", "/home")
	userHomeDir := indexSequence(spec.Args, "--dir", "/home/tester")
	if repoBind < 0 || homeDir < 0 || userHomeDir < 0 {
		t.Fatalf("bwrap args = %#v, want /home setup dirs and repo bind", spec.Args)
	}
	if homeDir > repoBind || userHomeDir > repoBind {
		t.Fatalf("bwrap args = %#v, want /home setup dirs before repo bind", spec.Args)
	}
}

func TestBuildSpecNetworkFalseAddsUnshareNet(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command: config.SandboxCommand{Argv: []string{"sh"}},
		CWD:     ".",
		Bubblewrap: config.BubblewrapConfig{
			Enabled: true,
			Network: config.RequiredBool{
				Value: false,
				Set:   true,
			},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ:     testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsArg(spec.Args, "--unshare-net") {
		t.Fatalf("bwrap args = %#v, want --unshare-net", spec.Args)
	}
}

func TestBuildSpecSkipsMissingOptionalBeadsDir(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Stat:        allPathsAreDirs,
		Environ:     testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if containsSequence(spec.Args, "--bind", filepath.Clean(filepath.Join(root, "..", ".beads")), filepath.Clean(filepath.Join(root, "..", ".beads"))) {
		t.Fatalf("bwrap args = %#v, want missing beads dir skipped", spec.Args)
	}
}

func TestBuildSpecCreatesDefaultCodexHomeUnderSyntheticHome(t *testing.T) {
	root := t.TempDir()
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	var created string

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ:     testEnv(root),
		MkdirAll: func(path string, _ os.FileMode) error {
			created = path
			return os.MkdirAll(path, 0o700)
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	hostCodex := filepath.Join(root, ".codex")
	if created != hostCodex {
		t.Fatalf("created codex home = %q, want %q", created, hostCodex)
	}
	if !containsSequence(spec.Args, "--bind", hostCodex, "/home/orc/.codex") {
		t.Fatalf("bwrap args = %#v, want default codex home mounted into synthetic home", spec.Args)
	}
	assertEnvContains(t, spec.Env, "HOME=/home/orc")
	assertEnvContains(t, spec.Env, "CODEX_HOME=/home/orc/.codex")
}

func TestBuildSpecExplicitSyntheticHomePreservesDefaultCodexHomeTarget(t *testing.T) {
	root := t.TempDir()
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeSynthetic},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS:  "linux",
		LookPath:     foundBwrap,
		PathExists:   onlyHostPaths(filepath.Join(root, ".codex")),
		Stat:         allPathsAreDirs,
		EvalSymlinks: identityEvalSymlinks,
		Environ:      testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--bind", filepath.Join(root, ".codex"), "/home/orc/.codex") {
		t.Fatalf("bwrap args = %#v, want default codex home mounted into synthetic home", spec.Args)
	}
	assertEnvContains(t, spec.Env, "HOME=/home/orc")
	assertEnvContains(t, spec.Env, "CODEX_HOME=/home/orc/.codex")
}

func TestBuildSpecHostPathHomeUsesHostHomeEnvAndSamePathDefaultCodexHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "host-home")
	codexHome := filepath.Join(home, ".codex")
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS:  "linux",
		LookPath:     foundBwrap,
		PathExists:   onlyHostPaths(codexHome),
		Stat:         allPathsAreDirs,
		EvalSymlinks: identityEvalSymlinks,
		Environ:      testEnv(home),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--dir", filepath.Dir(home)) || !containsSequence(spec.Args, "--dir", home) {
		t.Fatalf("bwrap args = %#v, want host HOME setup dirs", spec.Args)
	}
	if containsSequence(spec.Args, "--bind", home, home) {
		t.Fatalf("bwrap args = %#v, must not bind whole host HOME", spec.Args)
	}
	if !containsSequence(spec.Args, "--bind", codexHome, codexHome) {
		t.Fatalf("bwrap args = %#v, want default codex home mounted at same host path", spec.Args)
	}
	assertEnvContains(t, spec.Env, "HOME="+home)
	assertEnvContains(t, spec.Env, "CODEX_HOME="+codexHome)
}

func TestBuildSpecHostPathHomeFallsBackToUserHomeDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "fallback-home")
	codexHome := filepath.Join(home, ".codex")
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS:  "linux",
		LookPath:     foundBwrap,
		PathExists:   onlyHostPaths(codexHome),
		Stat:         allPathsAreDirs,
		EvalSymlinks: identityEvalSymlinks,
		Environ: func() []string {
			return []string{"PATH=/usr/bin"}
		},
		UserHomeDir: func() (string, error) {
			return home, nil
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertEnvContains(t, spec.Env, "HOME="+home)
	assertEnvContains(t, spec.Env, "CODEX_HOME="+codexHome)
}

func TestBuildSpecHostPathHomeRejectsNonAbsoluteResolvedHome(t *testing.T) {
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=relative-home"}
		},
	})
	if err == nil || !strings.Contains(err.Error(), `host HOME "relative-home" must resolve to an absolute path`) {
		t.Fatalf("BuildSpec error = %v, want non-absolute host HOME error", err)
	}
}

func TestBuildSpecExplicitCodexHomeUsesSamePathInBothHomeModes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mode     string
		wantHome string
	}{
		{name: "synthetic", mode: config.SandboxHomeModeSynthetic, wantHome: "/home/orc"},
		{name: "host path", mode: config.SandboxHomeModeHostPath, wantHome: "/home/tester"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			codexHome := "/home/tester/explicit-codex-home"
			project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Home:       config.SandboxHomeConfig{Mode: tt.mode},
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			})

			spec, err := BuildSpec(project, Options{
				RuntimeGOOS:  "linux",
				LookPath:     foundBwrap,
				PathExists:   onlyHostPaths(codexHome),
				Stat:         allPathsAreDirs,
				EvalSymlinks: identityEvalSymlinks,
				Environ: func() []string {
					return []string{"PATH=/usr/bin", "HOME=/home/tester", "CODEX_HOME=" + codexHome}
				},
			})
			if err != nil {
				t.Fatalf("BuildSpec returned error: %v", err)
			}
			if !containsSequence(spec.Args, "--bind", codexHome, codexHome) {
				t.Fatalf("bwrap args = %#v, want explicit codex home same-path bind", spec.Args)
			}
			assertEnvContains(t, spec.Env, "HOME="+tt.wantHome)
			assertEnvContains(t, spec.Env, "CODEX_HOME="+codexHome)
		})
	}
}

func TestBuildSpecRelativeCodexHomeFallsBackToHostHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	codexHome := filepath.Join(home, ".codex")
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS:  "linux",
		LookPath:     foundBwrap,
		PathExists:   onlyHostPaths(codexHome),
		Stat:         allPathsAreDirs,
		EvalSymlinks: identityEvalSymlinks,
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + home, "CODEX_HOME=.codex"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--bind", codexHome, "/home/orc/.codex") {
		t.Fatalf("bwrap args = %#v, want fallback codex home mounted into synthetic home", spec.Args)
	}
	assertEnvContains(t, spec.Env, "CODEX_HOME=/home/orc/.codex")
}

func TestBuildSpecManagedHomeAndCodexHomeOverrideSandboxEnvSet(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "host-home")
	codexHome := filepath.Join(home, ".codex")
	project := sandboxProjectWithCodexRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Env: config.SandboxEnvConfig{
			Pass: []string{"HOME"},
			Set: map[string]string{
				"HOME": "/wrong/home",
			},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS:  "linux",
		LookPath:     foundBwrap,
		PathExists:   onlyHostPaths(codexHome),
		Stat:         allPathsAreDirs,
		EvalSymlinks: identityEvalSymlinks,
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + home, "CODEX_HOME=" + codexHome}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertEnvContains(t, spec.Env, "HOME="+home)
	assertEnvContains(t, spec.Env, "CODEX_HOME="+codexHome)
	assertEnvMissing(t, spec.Env, "HOME=/wrong/home")
}

func TestBuildSpecNonCodexRuntimeDoesNotAddCodexMountOrEnv(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, ".codex")
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "recorder"},
			Steps: map[string]config.Step{
				"code": {Agent: "coder"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"recorder": {ID: "recorder", Sandbox: config.RuntimeSandbox{Supported: true}},
	}

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(codexHome),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + root, "CODEX_HOME=" + codexHome}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if containsSequence(spec.Args, "--bind", codexHome, "/home/orc/.codex") || containsSequence(spec.Args, "--bind", codexHome, codexHome) {
		t.Fatalf("bwrap args = %#v, want no Codex mount for non-Codex runtime", spec.Args)
	}
	assertEnvMissing(t, spec.Env, "CODEX_HOME=")
}

func TestBuildSpecHostPathHomeAllowsExplicitSubpathMount(t *testing.T) {
	root := t.TempDir()
	home := testHomePath
	hostBun := filepath.Join(root, "bun")
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Mounts: []config.SandboxMount{
			{Host: hostBun, Target: "/home/user/.bun", Mode: "rw"},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(hostBun),
		Environ:     testEnv(home),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--bind", hostBun, "/home/user/.bun") {
		t.Fatalf("bwrap args = %#v, want explicit home subpath mount", spec.Args)
	}
}

func TestBuildSpecHostPathHomeRejectsHomeTargetAndAncestors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "exact home", target: testHomePath, want: "must not override active sandbox HOME"},
		{name: "ancestor", target: "/home", want: "must not override ancestor of active sandbox HOME"},
		{name: "sibling home", target: "/home/other/.cache", want: "must not override critical sandbox path /home"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := testHomePath
			hostMount := filepath.Join(root, "mount")
			project := sandboxProject(root, config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
				Mounts: []config.SandboxMount{
					{Host: hostMount, Target: tt.target, Mode: "rw"},
				},
			})

			_, err := BuildSpec(project, Options{
				RuntimeGOOS: "linux",
				LookPath:    foundBwrap,
				PathExists:  onlyHostPaths(hostMount),
				Environ:     testEnv(home),
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildSpec error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBuildSpecDefaultPathModeDoesNotAddAutomaticPathMounts(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Stat:        fakePathStat(map[string]bool{"/opt/tool/bin": true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/opt/tool/bin": "/opt/tool/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/opt/tool/bin", "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if containsSequence(spec.Args, "--ro-bind", "/opt/tool/bin", "/opt/tool/bin") {
		t.Fatalf("bwrap args = %#v, want no automatic PATH mount in default none mode", spec.Args)
	}
	assertEnvContains(t, spec.Env, "PATH=/opt/tool/bin")
}

func TestBuildSpecPathHostEntriesMountsEffectivePathEntries(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	pathValue := strings.Join([]string{
		"/opt/tool/bin",
		"",
		"relative/bin",
		"/missing/bin",
		"/profile/bin",
		"/not-dir",
		"/bad-symlink",
		"/same",
		"/same",
		"/alt",
		"/usr/bin",
	}, string(os.PathListSeparator))

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Stat: fakePathStat(map[string]bool{
			"/opt/tool/bin":          true,
			"/profile/bin":           true,
			"/nix/store/profile-bin": true,
			"/not-dir":               false,
			"/bad-symlink":           true,
			"/same":                  true,
			"/alt":                   true,
			"/resolved/same":         true,
			"/usr":                   true,
			"/usr/bin":               true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/opt/tool/bin": "/opt/tool/bin",
			"/profile/bin":  "/nix/store/profile-bin",
			"/not-dir":      "/not-dir",
			"/same":         "/resolved/same",
			"/alt":          "/resolved/same",
			"/usr/bin":      "/usr/bin",
		}, map[string]error{
			"/bad-symlink": os.ErrNotExist,
		}),
		Environ: func() []string {
			return []string{"PATH=" + pathValue, "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertEnvContains(t, spec.Env, "PATH="+pathValue)
	assertPathMount(t, spec.Args, "/opt/tool/bin", "/opt/tool/bin")
	assertPathMount(t, spec.Args, "/nix/store/profile-bin", "/profile/bin")
	assertPathMount(t, spec.Args, "/resolved/same", "/same")
	assertPathMount(t, spec.Args, "/resolved/same", "/alt")
	assertNoPathMount(t, spec.Args, "/usr/bin", "/usr/bin")
	assertSequenceCount(t, spec.Args, []string{"--ro-bind", "/resolved/same", "/same"}, 1)
	assertNoPathMount(t, spec.Args, "relative/bin", "relative/bin")
	assertNoPathMount(t, spec.Args, "/missing/bin", "/missing/bin")
	assertNoPathMount(t, spec.Args, "/not-dir", "/not-dir")
	assertNoPathMount(t, spec.Args, "/bad-symlink", "/bad-symlink")
}

func TestBuildSpecPathHostEntriesWorksInHostPathHomeMode(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeHostPath},
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Stat: fakePathStat(map[string]bool{
			"/home/user/.bun/bin": true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/home/user/.bun/bin": "/home/user/.bun/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/home/user/.bun/bin", "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertPathMount(t, spec.Args, "/home/user/.bun/bin", "/home/user/.bun/bin")
	if containsSequence(spec.Args, "--bind", testHomePath, testHomePath) {
		t.Fatalf("bwrap args = %#v, must not bind whole host HOME for PATH mount", spec.Args)
	}
}

func TestBuildSpecPathHostEntriesUsesSandboxEnvSetPath(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Env: config.SandboxEnvConfig{Set: map[string]string{
			"PATH": "/custom/bin::relative",
		}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Stat: fakePathStat(map[string]bool{
			"/host/bin":   true,
			"/custom/bin": true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/host/bin":   "/host/bin",
			"/custom/bin": "/custom/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/host/bin", "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertEnvContains(t, spec.Env, "PATH=/custom/bin::relative")
	assertPathMount(t, spec.Args, "/custom/bin", "/custom/bin")
	assertNoPathMount(t, spec.Args, "/host/bin", "/host/bin")
}

func TestBuildSpecPathHostEntriesRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "active sandbox home", path: "/home/orc", want: "must not mount active sandbox HOME"},
		{name: "resolved host home", path: testHomePath, want: "must not mount resolved host HOME"},
		{name: "home ancestor", path: "/home", want: "must not mount ancestor of active sandbox HOME"},
		{name: "repository target", path: "/repo/project", want: "must not override the repository mount"},
		{name: "repository ancestor", path: "/repo", want: "must not override the repository mount"},
		{name: "beads target", path: "/repo/.beads", want: "must not override the Beads mount"},
		{name: "protected tmp target", path: "/tmp/tools/bin", want: "must not override protected sandbox path /tmp"},
		{name: "broad nix target", path: "/nix", want: "must not override protected sandbox path /nix/store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := sandboxProject("/repo/project", config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			})

			_, err := BuildSpec(project, Options{
				RuntimeGOOS: "linux",
				LookPath:    foundBwrap,
				PathExists:  noHostPaths,
				Stat:        fakePathStat(map[string]bool{tt.path: true}),
				EvalSymlinks: fakeEvalSymlinks(map[string]string{
					tt.path: tt.path,
				}, nil),
				Environ: func() []string {
					return []string{"PATH=" + tt.path, "HOME=/home/user"}
				},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildSpec error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBuildSpecPathHostEntriesSkipsPathsAlreadyUnderSystemMounts(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Stat: fakePathStat(map[string]bool{
			"/etc":                            true,
			"/etc/profiles/per-user/matt/bin": true,
			"/usr":                            true,
			"/usr/bin":                        true,
			"/external/bin":                   true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/etc/profiles/per-user/matt/bin": "/etc/profiles/per-user/matt/bin",
			"/usr/bin":                        "/usr/bin",
			"/external/bin":                   "/external/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/etc/profiles/per-user/matt/bin:/usr/bin:/external/bin", "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertNoPathMount(t, spec.Args, "/etc/profiles/per-user/matt/bin", "/etc/profiles/per-user/matt/bin")
	assertNoPathMount(t, spec.Args, "/usr/bin", "/usr/bin")
	assertPathMount(t, spec.Args, "/external/bin", "/external/bin")
}

func TestBuildSpecPathHostEntriesSkipsPathsAlreadyUnderMountedTrees(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths("/repo/.beads"),
		Stat: fakePathStat(map[string]bool{
			"/repo/project/.direnv/bin": true,
			"/repo/.beads/bin":          true,
			"/external/bin":             true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/repo/project/.direnv/bin": "/repo/project/.direnv/bin",
			"/repo/.beads/bin":          "/repo/.beads/bin",
			"/external/bin":             "/external/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/repo/project/.direnv/bin:/repo/.beads/bin:/external/bin", "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertNoPathMount(t, spec.Args, "/repo/project/.direnv/bin", "/repo/project/.direnv/bin")
	assertNoPathMount(t, spec.Args, "/repo/.beads/bin", "/repo/.beads/bin")
	assertPathMount(t, spec.Args, "/external/bin", "/external/bin")
}

func TestBuildSpecPathHostEntriesRejectsExplicitMountConflict(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Mounts: []config.SandboxMount{
			{Host: "/elsewhere/bin", Target: "/opt/tool/bin", Mode: "ro"},
		},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths("/elsewhere/bin"),
		Stat:        fakePathStat(map[string]bool{"/opt/tool/bin": true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/opt/tool/bin": "/opt/tool/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/opt/tool/bin", "HOME=/home/user"}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with explicit sandbox mount target") {
		t.Fatalf("BuildSpec error = %v, want explicit mount conflict", err)
	}
}

func TestBuildSpecPathHostEntriesEmitsAutomaticMountsBeforeExplicitMounts(t *testing.T) {
	project := sandboxProject("/repo/project", config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Mounts: []config.SandboxMount{
			{Host: "/elsewhere/bin", Target: "/tools/bin", Mode: "ro"},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths("/elsewhere/bin"),
		Stat:        fakePathStat(map[string]bool{"/opt/tool/bin": true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			"/opt/tool/bin": "/opt/tool/bin",
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/opt/tool/bin", "HOME=/home/user"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	pathMount := indexSequence(spec.Args, "--ro-bind", "/opt/tool/bin", "/opt/tool/bin")
	explicitMount := indexSequence(spec.Args, "--ro-bind", "/elsewhere/bin", "/tools/bin")
	if pathMount < 0 || explicitMount < 0 || pathMount > explicitMount {
		t.Fatalf("bwrap args = %#v, want automatic PATH mount before explicit mount", spec.Args)
	}
}

func TestBuildSpecSkipsMissingOptionalExtraMount(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Mounts: []config.SandboxMount{
			{Host: "missing", Target: "/workspace/missing", Mode: "rw", Optional: config.RequiredBool{Value: true, Set: true}},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ:     testEnv(root),
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if containsSequence(spec.Args, "--bind", filepath.Join(root, "missing"), "/workspace/missing") {
		t.Fatalf("bwrap args = %#v, want optional missing mount skipped", spec.Args)
	}
}

func TestBuildSpecIncludesSelectedRuntimeSandboxRequirements(t *testing.T) {
	root := t.TempDir()
	runtimeCache := filepath.Join(root, ".orc", "cache", "custom")
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "custom"},
			Steps: map[string]config.Step{
				"code": {Agent: "coder"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"custom": {
			ID: "custom",
			Sandbox: config.RuntimeSandbox{
				Supported: true,
				Requirements: config.RuntimeSandboxRequirements{
					Env: config.RuntimeSandboxEnvConfig{
						Pass: []string{"CUSTOM_TOKEN"},
						Set:  map[string]string{"ORC_RUNTIME": "custom"},
					},
					Mounts: []config.RuntimeSandboxMount{
						{Host: ".orc/cache/custom", Target: config.RuntimeSandboxMountTarget{Path: "/workspace/.orc/cache/custom"}, Mode: "rw"},
					},
				},
			},
		},
	}

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(runtimeCache),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			root:         root,
			runtimeCache: runtimeCache,
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + root, "CUSTOM_TOKEN=secret"}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--bind", runtimeCache, "/workspace/.orc/cache/custom") {
		t.Fatalf("bwrap args = %#v, want runtime-required mount", spec.Args)
	}
	assertEnvContains(t, spec.Env, "CUSTOM_TOKEN=secret")
	assertEnvContains(t, spec.Env, "ORC_RUNTIME=custom")
}

func TestBuildSpecRejectsMissingRequiredRuntimeSandboxMount(t *testing.T) {
	root := t.TempDir()
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "custom"},
			Steps: map[string]config.Step{
				"code": {Agent: "coder"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"custom": {
			ID: "custom",
			Sandbox: config.RuntimeSandbox{
				Supported: true,
				Requirements: config.RuntimeSandboxRequirements{
					Mounts: []config.RuntimeSandboxMount{
						{Host: ".orc/cache/missing", Target: config.RuntimeSandboxMountTarget{Path: "/workspace/.orc/cache/missing"}, Mode: "ro"},
					},
				},
			},
		},
	}

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  noHostPaths,
		Environ:     testEnv(root),
	})
	if err == nil || !strings.Contains(err.Error(), `runtime "custom" sandbox.requirements.mounts[0].host ".orc/cache/missing" does not exist`) {
		t.Fatalf("BuildSpec error = %v, want missing runtime mount error", err)
	}
}

func TestBuildSpecRejectsRuntimeSandboxMountProtectedTargets(t *testing.T) {
	tests := []struct {
		name   string
		target func(root string) string
		want   string
	}{
		{
			name:   "repository target",
			target: func(root string) string { return root },
			want:   "must not override the repository mount",
		},
		{
			name:   "repository child target",
			target: func(root string) string { return filepath.Join(root, ".orc", "cache") },
			want:   "must not override the repository mount",
		},
		{
			name:   "repository ancestor target",
			target: filepath.Dir,
			want:   "must not override the repository mount",
		},
		{
			name:   "beads target",
			target: func(root string) string { return filepath.Clean(filepath.Join(root, "..", ".beads")) },
			want:   "must not override the Beads mount",
		},
		{
			name:   "protected tmp target",
			target: func(string) string { return "/tmp/cache" },
			want:   "must not override protected sandbox path /tmp",
		},
		{
			name:   "protected system ancestor target",
			target: func(string) string { return "/nix" },
			want:   "must not override protected sandbox path /nix/store",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			runtimeCache := filepath.Join(root, ".orc", "cache", "custom")
			project := sandboxProject(root, config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			})
			project.Workflows = map[string]config.Workflow{
				"implementation": {
					Defaults: config.Defaults{Runtime: "custom"},
					Steps: map[string]config.Step{
						"code": {Agent: "coder"},
					},
				},
			}
			project.Runtimes = map[string]config.Runtime{
				"custom": {
					ID: "custom",
					Sandbox: config.RuntimeSandbox{
						Supported: true,
						Requirements: config.RuntimeSandboxRequirements{
							Mounts: []config.RuntimeSandboxMount{
								{Host: ".orc/cache/custom", Target: config.RuntimeSandboxMountTarget{Path: tt.target(root)}, Mode: "ro"},
							},
						},
					},
				},
			}

			_, err := BuildSpec(project, Options{
				RuntimeGOOS: "linux",
				LookPath:    foundBwrap,
				PathExists:  onlyHostPaths(runtimeCache),
				Environ:     testEnv(root),
			})
			if err == nil || !strings.Contains(err.Error(), `runtime "custom" sandbox.requirements.mounts[0].target`) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildSpec error = %v, want runtime protected target error containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildSpecResolvesEnvSourcedRuntimeSandboxMount(t *testing.T) {
	root := t.TempDir()
	source := testCustomHomePath
	project := sandboxProjectWithRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	}, config.RuntimeSandboxRequirements{
		Env: config.RuntimeSandboxEnvConfig{
			SetFromMount: map[string]config.RuntimeEnvFromMountRef{
				"CUSTOM_HOME": {Mount: "config_home", Value: "target"},
			},
		},
		Mounts: []config.RuntimeSandboxMount{
			{
				ID: "config_home",
				Source: config.RuntimeSandboxMountSource{
					Env: "CUSTOM_HOME",
					Fallback: config.RuntimeSandboxMountSourceFallback{
						HostHome: ".custom",
					},
				},
				Target: config.RuntimeSandboxMountTarget{
					EnvSameAsSource: true,
					Fallback:        config.RuntimeSandboxMountTargetFallback{SandboxHome: ".custom"},
				},
				Mode: "rw",
			},
		},
	})

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(source),
		Stat:        fakePathStat(map[string]bool{source: true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			source: source,
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + root, "CUSTOM_HOME=" + source}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--bind", source, source) {
		t.Fatalf("bwrap args = %#v, want env-sourced mount at same target", spec.Args)
	}
	assertEnvContains(t, spec.Env, "CUSTOM_HOME="+source)
}

func TestBuildSpecAllowsEnvSourcedSamePathRuntimeSandboxMountUnderHome(t *testing.T) {
	root := t.TempDir()
	source := "/home/user/.custom"
	project := sandboxProjectWithRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeSynthetic},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	}, runtimeRequirementsWithEnvMount("CUSTOM_HOME"))

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(source),
		Stat:        fakePathStat(map[string]bool{source: true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			source: source,
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + root, "CUSTOM_HOME=" + source}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	if !containsSequence(spec.Args, "--bind", source, source) {
		t.Fatalf("bwrap args = %#v, want env-sourced /home mount at same target", spec.Args)
	}
	assertEnvContains(t, spec.Env, "CUSTOM_HOME="+source)
}

func TestBuildSpecRejectsEnvSourcedSamePathRuntimeSandboxMountOverRepository(t *testing.T) {
	root := "/home/user/project"
	source := testHomePath
	project := sandboxProjectWithRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Home:       config.SandboxHomeConfig{Mode: config.SandboxHomeModeSynthetic},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	}, runtimeRequirementsWithEnvMount("CUSTOM_HOME"))

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(source),
		Stat:        fakePathStat(map[string]bool{source: true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			source: source,
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=/tmp/host-home", "CUSTOM_HOME=" + source}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must not override the repository mount") {
		t.Fatalf("BuildSpec error = %v, want repository mount conflict", err)
	}
}

func TestBuildSpecEnvFromMountUsesRuntimeScopedMountID(t *testing.T) {
	root := t.TempDir()
	recorderSource := "/opt/recorder-home"
	customSource := testCustomHomePath
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "recorder"},
			Steps: map[string]config.Step{
				"code":   {Agent: "coder"},
				"review": {Agent: "reviewer", Runtime: "custom"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"custom": {
			ID: "custom",
			Sandbox: config.RuntimeSandbox{
				Supported:    true,
				Requirements: runtimeRequirementsWithEnvMount("CUSTOM_HOME"),
			},
		},
		"recorder": {
			ID: "recorder",
			Sandbox: config.RuntimeSandbox{
				Supported:    true,
				Requirements: runtimeRequirementsWithEnvMount("RECORDER_HOME"),
			},
		},
	}

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(customSource, recorderSource),
		Stat: fakePathStat(map[string]bool{
			customSource:   true,
			recorderSource: true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			customSource:   customSource,
			recorderSource: recorderSource,
		}, nil),
		Environ: func() []string {
			return []string{
				"PATH=/usr/bin",
				"HOME=" + root,
				"CUSTOM_HOME=" + customSource,
				"RECORDER_HOME=" + recorderSource,
			}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertEnvContains(t, spec.Env, "CUSTOM_HOME="+customSource)
	assertEnvContains(t, spec.Env, "RECORDER_HOME="+recorderSource)
}

func TestBuildSpecRejectsConflictingEnvFromMountNamesAcrossRuntimes(t *testing.T) {
	root := t.TempDir()
	recorderSource := "/opt/recorder-home"
	customSource := testCustomHomePath
	project := sandboxProject(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	})
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "recorder"},
			Steps: map[string]config.Step{
				"code":   {Agent: "coder"},
				"review": {Agent: "reviewer", Runtime: "custom"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"custom": {
			ID: "custom",
			Sandbox: config.RuntimeSandbox{
				Supported:    true,
				Requirements: runtimeRequirementsWithEnvMountSource("CUSTOM_HOME", "CUSTOM_SOURCE", ".custom"),
			},
		},
		"recorder": {
			ID: "recorder",
			Sandbox: config.RuntimeSandbox{
				Supported:    true,
				Requirements: runtimeRequirementsWithEnvMountSource("CUSTOM_HOME", "RECORDER_SOURCE", ".recorder"),
			},
		},
	}

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(customSource, recorderSource),
		Stat: fakePathStat(map[string]bool{
			customSource:   true,
			recorderSource: true,
		}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			customSource:   customSource,
			recorderSource: recorderSource,
		}, nil),
		Environ: func() []string {
			return []string{
				"PATH=/usr/bin",
				"HOME=" + root,
				"CUSTOM_SOURCE=" + customSource,
				"RECORDER_SOURCE=" + recorderSource,
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), `runtime "`) || !strings.Contains(err.Error(), `sandbox.requirements.env.set_from_mount["CUSTOM_HOME"] conflicts with another sandbox environment value for CUSTOM_HOME`) {
		t.Fatalf("BuildSpec error = %v, want conflicting env-from-mount name error", err)
	}
}

func TestBuildSpecEnvFromMountResolvesDeduplicatedRuntimeMount(t *testing.T) {
	root := t.TempDir()
	source := "/opt/dedup-home"
	project := sandboxProjectWithRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		Mounts: []config.SandboxMount{
			{Host: source, Target: source, Mode: "rw"},
		},
	}, runtimeRequirementsWithEnvMount("CUSTOM_HOME"))

	spec, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(source),
		Stat:        fakePathStat(map[string]bool{source: true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			source: source,
		}, nil),
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + root, "CUSTOM_HOME=" + source}
		},
	})
	if err != nil {
		t.Fatalf("BuildSpec returned error: %v", err)
	}
	assertEnvContains(t, spec.Env, "CUSTOM_HOME="+source)
}

func TestBuildSpecRuntimeSandboxMountFallbackSources(t *testing.T) {
	for _, tt := range []struct {
		name     string
		envValue string
	}{
		{name: "unset"},
		{name: "empty", envValue: ""},
		{name: "relative", envValue: "relative-home"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			source := filepath.Join(home, ".custom")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatalf("create fallback source: %v", err)
			}
			project := sandboxProjectWithRuntime(root, config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			}, fallbackRuntimeRequirements(false))
			env := []string{"PATH=/usr/bin", "HOME=" + home}
			if tt.name != "unset" {
				env = append(env, "CUSTOM_HOME="+tt.envValue)
			}

			spec, err := BuildSpec(project, Options{
				RuntimeGOOS: "linux",
				LookPath:    foundBwrap,
				Environ:     func() []string { return env },
			})
			if err != nil {
				t.Fatalf("BuildSpec returned error: %v", err)
			}
			if !containsSequence(spec.Args, "--bind", source, "/home/orc/.custom") {
				t.Fatalf("bwrap args = %#v, want fallback source mounted under sandbox home", spec.Args)
			}
			assertEnvContains(t, spec.Env, "CUSTOM_HOME=/home/orc/.custom")
		})
	}
}

func TestBuildSpecRuntimeSandboxMountCreateAndMissingBehavior(t *testing.T) {
	t.Run("create true creates missing source", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		source := filepath.Join(home, ".custom")
		project := sandboxProjectWithRuntime(root, config.SandboxConfig{
			Command:    config.SandboxCommand{Argv: []string{"sh"}},
			CWD:        ".",
			Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		}, fallbackRuntimeRequirements(true))

		spec, err := BuildSpec(project, Options{
			RuntimeGOOS: "linux",
			LookPath:    foundBwrap,
			Environ:     func() []string { return []string{"PATH=/usr/bin", "HOME=" + home} },
		})
		if err != nil {
			t.Fatalf("BuildSpec returned error: %v", err)
		}
		if info, err := os.Stat(source); err != nil || !info.IsDir() {
			t.Fatalf("created source stat = %v, %v; want directory", info, err)
		}
		if !containsSequence(spec.Args, "--bind", source, "/home/orc/.custom") {
			t.Fatalf("bwrap args = %#v, want created source mount", spec.Args)
		}
	})

	t.Run("create false rejects missing source", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		project := sandboxProjectWithRuntime(root, config.SandboxConfig{
			Command:    config.SandboxCommand{Argv: []string{"sh"}},
			CWD:        ".",
			Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
		}, fallbackRuntimeRequirements(false))

		_, err := BuildSpec(project, Options{
			RuntimeGOOS: "linux",
			LookPath:    foundBwrap,
			Environ:     func() []string { return []string{"PATH=/usr/bin", "HOME=" + home} },
		})
		if err == nil || !strings.Contains(err.Error(), `source resolved path`) || !strings.Contains(err.Error(), `does not exist`) {
			t.Fatalf("BuildSpec error = %v, want missing source error", err)
		}
	})
}

func TestBuildSpecRejectsRuntimeSandboxMountSourceFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "custom-file")
	if err := os.WriteFile(source, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	project := sandboxProjectWithRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	}, fallbackRuntimeRequirements(false))

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + root, "CUSTOM_HOME=" + source}
		},
	})
	if err == nil || !strings.Contains(err.Error(), `must be a directory`) {
		t.Fatalf("BuildSpec error = %v, want source file rejection", err)
	}
}

func TestBuildSpecRuntimeSandboxMountFallbackTargetsByHomeMode(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mode     string
		wantHome func(string) string
	}{
		{name: "synthetic", mode: config.SandboxHomeModeSynthetic, wantHome: func(string) string { return "/home/orc/.custom" }},
		{name: "host path", mode: config.SandboxHomeModeHostPath, wantHome: func(home string) string { return filepath.Join(home, ".custom") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			source := filepath.Join(home, ".custom")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatalf("create fallback source: %v", err)
			}
			project := sandboxProjectWithRuntime(root, config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Home:       config.SandboxHomeConfig{Mode: tt.mode},
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			}, fallbackRuntimeRequirements(false))

			spec, err := BuildSpec(project, Options{
				RuntimeGOOS: "linux",
				LookPath:    foundBwrap,
				Environ:     func() []string { return []string{"PATH=/usr/bin", "HOME=" + home} },
			})
			if err != nil {
				t.Fatalf("BuildSpec returned error: %v", err)
			}
			if !containsSequence(spec.Args, "--bind", source, tt.wantHome(home)) {
				t.Fatalf("bwrap args = %#v, want fallback target %s", spec.Args, tt.wantHome(home))
			}
		})
	}
}

func TestBuildSpecRejectsRuntimeSandboxMountActiveConflicts(t *testing.T) {
	tests := []struct {
		name    string
		sandbox config.SandboxConfig
		want    string
	}{
		{
			name: "project mount",
			sandbox: config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
				Mounts: []config.SandboxMount{
					{Host: ".", Target: "/home/orc/.custom", Mode: "ro"},
				},
			},
			want: "conflicts with explicit sandbox mount target",
		},
		{
			name: "home policy",
			sandbox: config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			},
			want: "must not override ancestor of active sandbox HOME",
		},
		{
			name: "protected system",
			sandbox: config.SandboxConfig{
				Command:    config.SandboxCommand{Argv: []string{"sh"}},
				CWD:        ".",
				Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
			},
			want: "must not override protected sandbox path /tmp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			source := filepath.Join(home, ".custom")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatalf("create fallback source: %v", err)
			}
			requirements := fallbackRuntimeRequirements(false)
			switch tt.name {
			case "home policy":
				requirements.Mounts[0].Target.Fallback.SandboxHome = ".."
				tt.sandbox.Home.Mode = config.SandboxHomeModeHostPath
			case "protected system":
				requirements.Mounts[0].Target.Fallback.SandboxHome = "../../tmp/custom"
			}
			project := sandboxProjectWithRuntime(root, tt.sandbox, requirements)

			_, err := BuildSpec(project, Options{
				RuntimeGOOS: "linux",
				LookPath:    foundBwrap,
				Environ:     func() []string { return []string{"PATH=/usr/bin", "HOME=" + home} },
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildSpec error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBuildSpecRejectsRuntimeSandboxMountAutomaticPathConflict(t *testing.T) {
	root := t.TempDir()
	source := "/opt/custom/bin"
	project := sandboxProjectWithRuntime(root, config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Path:       config.SandboxPathConfig{Mode: config.SandboxPathModeHostEntries},
		Bubblewrap: config.BubblewrapConfig{Enabled: true, Network: config.RequiredBool{Value: true, Set: true}},
	}, config.RuntimeSandboxRequirements{
		Mounts: []config.RuntimeSandboxMount{
			{
				Source: config.RuntimeSandboxMountSource{Env: "CUSTOM_BIN"},
				Target: config.RuntimeSandboxMountTarget{
					EnvSameAsSource: true,
				},
				Mode: "ro",
			},
		},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath:    foundBwrap,
		PathExists:  onlyHostPaths(source),
		Stat:        fakePathStat(map[string]bool{source: true}),
		EvalSymlinks: fakeEvalSymlinks(map[string]string{
			source: source,
		}, nil),
		Environ: func() []string {
			return []string{"PATH=" + source, "HOME=" + root, "CUSTOM_BIN=" + source}
		},
	})
	if err == nil || !strings.Contains(err.Error(), `conflicts with explicit sandbox mount target`) {
		t.Fatalf("BuildSpec error = %v, want automatic PATH conflict", err)
	}
}

func TestBuildSpecRejectsMissingSandboxConfig(t *testing.T) {
	_, err := BuildSpec(&config.Project{Root: t.TempDir()}, Options{RuntimeGOOS: "linux", LookPath: foundBwrap})
	if err == nil || !strings.Contains(err.Error(), "sandbox config is required") {
		t.Fatalf("BuildSpec error = %v, want missing sandbox config error", err)
	}
}

func TestBuildSpecRejectsUnsupportedPlatformBeforeBwrapLookup(t *testing.T) {
	called := false
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "darwin",
		LookPath: func(string) (string, error) {
			called = true
			return "", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires Linux") {
		t.Fatalf("BuildSpec error = %v, want unsupported platform error", err)
	}
	if called {
		t.Fatal("LookPath was called on unsupported platform")
	}
}

func TestBuildSpecRejectsMissingBwrap(t *testing.T) {
	project := sandboxProject(t.TempDir(), config.SandboxConfig{
		Command:    config.SandboxCommand{Argv: []string{"sh"}},
		CWD:        ".",
		Bubblewrap: config.BubblewrapConfig{Enabled: true},
	})

	_, err := BuildSpec(project, Options{
		RuntimeGOOS: "linux",
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	})
	if err == nil || !strings.Contains(err.Error(), "install bubblewrap") {
		t.Fatalf("BuildSpec error = %v, want install guidance", err)
	}
}

func TestRunSpecReturnsChildExitStatus(t *testing.T) {
	err := runSpec(context.Background(), BwrapSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "exit 7"},
		CWD:  t.TempDir(),
		Env:  os.Environ(),
	}, Options{})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runSpec error = %T %v, want ExitError", err, err)
	}
	if exitErr.Code != 7 {
		t.Fatalf("exit code = %d, want 7", exitErr.Code)
	}
}

func TestRunSpecReturnsSignalExitStatus(t *testing.T) {
	err := runSpec(context.Background(), BwrapSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "kill -TERM $$"},
		CWD:  t.TempDir(),
		Env:  os.Environ(),
	}, Options{})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runSpec error = %T %v, want ExitError", err, err)
	}
	if exitErr.Code != 143 {
		t.Fatalf("exit code = %d, want 143", exitErr.Code)
	}
}

func TestRunSpecCancelsInteractiveProcess(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runSpec(ctx, BwrapSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "while :; do sleep 1; done"},
			CWD:  root,
			Env:  os.Environ(),
		}, Options{})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("runSpec error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSpec did not return after context cancellation")
	}
}

func sandboxProject(root string, sandboxConfig config.SandboxConfig) *config.Project {
	return &config.Project{
		Root: root,
		Config: config.ProjectConfig{
			Sandbox: &sandboxConfig,
		},
	}
}

func sandboxProjectWithCodexRuntime(root string, sandboxConfig config.SandboxConfig) *config.Project {
	project := sandboxProject(root, sandboxConfig)
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "codex"},
			Steps: map[string]config.Step{
				"code": {Agent: "coder"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"codex": {
			ID: "codex",
			Sandbox: config.RuntimeSandbox{
				Supported:    true,
				Requirements: codexRuntimeRequirements(),
			},
		},
	}
	return project
}

func sandboxProjectWithRuntime(root string, sandboxConfig config.SandboxConfig, requirements config.RuntimeSandboxRequirements) *config.Project {
	project := sandboxProject(root, sandboxConfig)
	project.Workflows = map[string]config.Workflow{
		"implementation": {
			Defaults: config.Defaults{Runtime: "custom"},
			Steps: map[string]config.Step{
				"code": {Agent: "coder"},
			},
		},
	}
	project.Runtimes = map[string]config.Runtime{
		"custom": {
			ID: "custom",
			Sandbox: config.RuntimeSandbox{
				Supported:    true,
				Requirements: requirements,
			},
		},
	}
	return project
}

func codexRuntimeRequirements() config.RuntimeSandboxRequirements {
	return config.RuntimeSandboxRequirements{
		Env: config.RuntimeSandboxEnvConfig{
			SetFromMount: map[string]config.RuntimeEnvFromMountRef{
				"CODEX_HOME": {Mount: "config_home", Value: "target"},
			},
		},
		Mounts: []config.RuntimeSandboxMount{
			{
				ID: "config_home",
				Source: config.RuntimeSandboxMountSource{
					Env:    "CODEX_HOME",
					Create: true,
					Fallback: config.RuntimeSandboxMountSourceFallback{
						HostHome: ".codex",
					},
				},
				Target: config.RuntimeSandboxMountTarget{
					EnvSameAsSource: true,
					Fallback:        config.RuntimeSandboxMountTargetFallback{SandboxHome: ".codex"},
				},
				Mode: "rw",
			},
		},
	}
}

func runtimeRequirementsWithEnvMount(envName string) config.RuntimeSandboxRequirements {
	return runtimeRequirementsWithEnvMountSource(envName, envName, ".custom")
}

func runtimeRequirementsWithEnvMountSource(envName, sourceEnv, fallback string) config.RuntimeSandboxRequirements {
	return config.RuntimeSandboxRequirements{
		Env: config.RuntimeSandboxEnvConfig{
			SetFromMount: map[string]config.RuntimeEnvFromMountRef{
				envName: {Mount: "config_home", Value: "target"},
			},
		},
		Mounts: []config.RuntimeSandboxMount{
			{
				ID: "config_home",
				Source: config.RuntimeSandboxMountSource{
					Env: sourceEnv,
					Fallback: config.RuntimeSandboxMountSourceFallback{
						HostHome: fallback,
					},
				},
				Target: config.RuntimeSandboxMountTarget{
					EnvSameAsSource: true,
					Fallback:        config.RuntimeSandboxMountTargetFallback{SandboxHome: fallback},
				},
				Mode: "rw",
			},
		},
	}
}

func fallbackRuntimeRequirements(create bool) config.RuntimeSandboxRequirements {
	return config.RuntimeSandboxRequirements{
		Env: config.RuntimeSandboxEnvConfig{
			SetFromMount: map[string]config.RuntimeEnvFromMountRef{
				"CUSTOM_HOME": {Mount: "config_home", Value: "target"},
			},
		},
		Mounts: []config.RuntimeSandboxMount{
			{
				ID: "config_home",
				Source: config.RuntimeSandboxMountSource{
					Env:    "CUSTOM_HOME",
					Create: create,
					Fallback: config.RuntimeSandboxMountSourceFallback{
						HostHome: ".custom",
					},
				},
				Target: config.RuntimeSandboxMountTarget{
					EnvSameAsSource: true,
					Fallback:        config.RuntimeSandboxMountTargetFallback{SandboxHome: ".custom"},
				},
				Mode: "rw",
			},
		},
	}
}

func foundBwrap(string) (string, error) {
	return testBwrapPath, nil
}

func noHostPaths(string) bool {
	return false
}

func onlyHostPaths(paths ...string) func(string) bool {
	return func(path string) bool {
		return slices.Contains(paths, path)
	}
}

func allPathsAreDirs(path string) (os.FileInfo, error) {
	return fakeFileInfo{name: filepath.Base(path), dir: true}, nil
}

func identityEvalSymlinks(path string) (string, error) {
	return path, nil
}

type fakeFileInfo struct {
	name string
	dir  bool
}

func (i fakeFileInfo) Name() string       { return i.name }
func (i fakeFileInfo) Size() int64        { return 0 }
func (i fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return i.dir }
func (i fakeFileInfo) Sys() any           { return nil }

func fakePathStat(paths map[string]bool) func(string) (os.FileInfo, error) {
	return func(path string) (os.FileInfo, error) {
		isDir, ok := paths[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo{name: filepath.Base(path), dir: isDir}, nil
	}
}

func fakeEvalSymlinks(resolved map[string]string, failures map[string]error) func(string) (string, error) {
	return func(path string) (string, error) {
		if err, ok := failures[path]; ok {
			return "", err
		}
		if target, ok := resolved[path]; ok {
			return target, nil
		}
		return path, nil
	}
}

func testEnv(home string) func() []string {
	return func() []string {
		return []string{"PATH=/usr/bin", "HOME=" + home}
	}
}

func containsArg(args []string, want string) bool {
	return slices.Contains(args, want)
}

func containsSequence(args []string, want ...string) bool {
	return indexSequence(args, want...) >= 0
}

func indexSequence(args []string, want ...string) int {
	if len(want) == 0 || len(want) > len(args) {
		return -1
	}
	for i := 0; i <= len(args)-len(want); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return i
		}
	}
	return -1
}

func countSequence(args []string, want ...string) int {
	count := 0
	if len(want) == 0 || len(want) > len(args) {
		return count
	}
	for i := 0; i <= len(args)-len(want); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			count++
		}
	}
	return count
}

func assertPathMount(t *testing.T, args []string, host, target string) {
	t.Helper()
	if !containsSequence(args, "--ro-bind", host, target) {
		t.Fatalf("bwrap args = %#v, want PATH ro-bind %s -> %s", args, host, target)
	}
}

func assertNoPathMount(t *testing.T, args []string, host, target string) {
	t.Helper()
	if containsSequence(args, "--ro-bind", host, target) {
		t.Fatalf("bwrap args = %#v, want no PATH ro-bind %s -> %s", args, host, target)
	}
}

func assertSequenceCount(t *testing.T, args, want []string, count int) {
	t.Helper()
	if got := countSequence(args, want...); got != count {
		t.Fatalf("sequence %v count = %d in %#v, want %d", want, got, args, count)
	}
}

func assertEnvContains(t *testing.T, env []string, want string) {
	t.Helper()
	if slices.Contains(env, want) {
		return
	}
	t.Fatalf("env missing %q: %#v", want, env)
}

func assertEnvMissing(t *testing.T, env []string, prefix string) {
	t.Helper()
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			t.Fatalf("env contains %q with prefix %q: %#v", entry, prefix, env)
		}
	}
}
