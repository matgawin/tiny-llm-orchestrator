package initconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
)

func TestRunDryRunReportsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer

	if err := Run(Options{Root: root, DryRun: true, Stdout: &stdout}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range append([]string{
		"orc init dry-run:",
		"would create .orc/runs",
		"would create .gitignore with " + runsIgnoreEntry,
		"would prompt before creating AGENTS.md",
	}, dryRunScaffoldLines()...) {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".orc")); !os.IsNotExist(err) {
		t.Fatalf(".orc stat err = %v, want not exist", err)
	}
	for _, path := range []string{".gitignore", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s stat err = %v, want not exist", path, err)
		}
	}
}

func TestRunYesCreatesValidScaffoldAndIgnoreEntry(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer

	if err := Run(Options{Root: root, Yes: true, Stdout: &stdout}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if _, err := config.Load(root); err != nil {
		t.Fatalf("generated config did not validate: %v", err)
	}
	assertGeneratedScaffoldMatchesFixture(t, root)
	assertFileContains(t, filepath.Join(root, ".orc", "config.yaml"), "  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4")
	assertFileContains(t, filepath.Join(root, ".orc", "config.yaml"), "# Optional sandbox runner for Codex yolo mode.")
	assertFileContains(t, filepath.Join(root, ".orc", "config.yaml"), "#   require_for_workers: true")
	assertFileContains(t, filepath.Join(root, ".orc", "config.yaml"), "#     network: true")
	assertFileContains(t, filepath.Join(root, ".gitignore"), runsIgnoreEntry)
	if info, err := os.Stat(filepath.Join(root, ".orc", "runs")); err != nil {
		t.Fatalf(".orc/runs stat err = %v, want directory", err)
	} else if !info.IsDir() {
		t.Fatal(".orc/runs is not a directory")
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md stat err = %v, want not exist with --yes", err)
	}
	if output := stdout.String(); !strings.Contains(output, "skipped AGENTS.md creation") {
		t.Fatalf("output = %q, want AGENTS.md skip", output)
	}
}

func TestRunYesLeavesExistingInstructionsUnchanged(t *testing.T) {
	root := t.TempDir()
	instructionsPath := filepath.Join(root, "AGENTS.md")
	original := []byte("# Existing Instructions\n\nKeep this exact text.\n")
	if err := os.WriteFile(instructionsPath, original, 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	var stdout bytes.Buffer
	if err := Run(Options{Root: root, Yes: true, Stdout: &stdout}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertInstructionsContent(t, instructionsPath, original)
	if output := stdout.String(); !strings.Contains(output, "skipped AGENTS.md update") {
		t.Fatalf("output = %q, want AGENTS.md update skip", output)
	}
}

func TestRunYesIsIdempotentForMatchingFiles(t *testing.T) {
	root := t.TempDir()
	if err := Run(Options{Root: root, Yes: true}); err != nil {
		t.Fatalf("first Run returned error: %v", err)
	}
	before := snapshotFiles(t, root, scaffoldPaths())

	var stdout bytes.Buffer
	if err := Run(Options{Root: root, Yes: true, Stdout: &stdout}); err != nil {
		t.Fatalf("second Run returned error: %v", err)
	}
	after := snapshotFiles(t, root, scaffoldPaths())
	for path, want := range before {
		if got := after[path]; !bytes.Equal(got, want) {
			t.Fatalf("%s changed on idempotent run", path)
		}
	}

	output := stdout.String()
	for _, want := range []string{
		"exists .orc/config.yaml",
		"exists .gitignore entry " + runsIgnoreEntry,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("second output missing %q:\n%s", want, output)
		}
	}
}

func TestRunFailsBeforeWritingWhenRuntimePathIsFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".orc"), 0o755); err != nil {
		t.Fatalf("create .orc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".orc", "runs"), []byte("not a dir\n"), 0o644); err != nil {
		t.Fatalf("write .orc/runs file: %v", err)
	}

	err := Run(Options{Root: root, Yes: true})
	if err == nil {
		t.Fatal("Run returned nil error, want runtime path error")
	}
	if !strings.Contains(err.Error(), "already exists and is not a directory") {
		t.Fatalf("error = %q, want runtime path error", err)
	}
	assertScaffoldFilesDoNotExist(t, root)
}

func TestRunYesRejectsDifferingExistingScaffoldFile(t *testing.T) {
	root, _ := projectWithCustomConfig(t, []byte("custom: true\n"))

	err := Run(Options{Root: root, Yes: true})
	if err == nil {
		t.Fatal("Run returned nil error, want conflict")
	}
	if !strings.Contains(err.Error(), "already exists with different content") {
		t.Fatalf("error = %q, want different content", err)
	}
}

func TestRunInteractivePromptsForConflictsAndInstructionFile(t *testing.T) {
	root, _ := projectWithCustomConfig(t, []byte("custom: true\n"))

	stdin := confirmOverwriteConfigCreateGitignoreAndInstructions()
	var stdout bytes.Buffer
	if err := Run(Options{Root: root, Stdin: stdin, Stdout: &stdout}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if _, err := config.Load(root); err != nil {
		t.Fatalf("generated config did not validate: %v", err)
	}
	assertFileContains(t, filepath.Join(root, ".gitignore"), runsIgnoreEntry)
	assertFileContains(t, filepath.Join(root, "AGENTS.md"), "## Tiny Orc")
	output := stdout.String()
	for _, want := range []string{
		"Overwrite .orc/config.yaml?",
		"Create .gitignore with " + runsIgnoreEntry + "?",
		"Create AGENTS.md with Tiny Orc guidance?",
		"updated .orc/config.yaml",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("interactive output missing %q:\n%s", want, output)
		}
	}
}

func TestRunDoesNotPromptForExistingInstructionsSection(t *testing.T) {
	original := "# Existing\n\n## Tiny Orc\n\nAlready configured.\n"
	root, instructionsPath := projectWithGitignoreAndInstructions(t, original)

	var stdout bytes.Buffer
	if err := Run(Options{Root: root, Stdin: strings.NewReader(""), Stdout: &stdout}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertInstructionsContent(t, instructionsPath, []byte(original))
	output := stdout.String()
	if strings.Contains(output, "Append Tiny Orc guidance") {
		t.Fatalf("output prompted for existing section:\n%s", output)
	}
	if !strings.Contains(output, "exists AGENTS.md Tiny Orc section") {
		t.Fatalf("output = %q, want existing section report", output)
	}
}

func TestRunAppendsInstructionsWhenConfirmed(t *testing.T) {
	original := "# Existing Instructions\n\nKeep this first.\n"
	root, instructionsPath := projectWithGitignoreAndInstructions(t, original)

	var stdout bytes.Buffer
	if err := Run(Options{Root: root, Stdin: strings.NewReader("yes\n"), Stdout: &stdout}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	content, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if got := string(content); !strings.HasPrefix(got, original) || !strings.Contains(got, "## Tiny Orc") {
		t.Fatalf("AGENTS.md content did not preserve and append guidance:\n%s", got)
	}
	if output := stdout.String(); !strings.Contains(output, "updated AGENTS.md") {
		t.Fatalf("output = %q, want AGENTS.md update", output)
	}
}

func TestRunAppendsExistingGitignoreWithoutLosingEntries(t *testing.T) {
	result := runWithGitignore(t, "bin/\n*.log", Options{Yes: true})
	if result.err != nil {
		t.Fatalf("Run returned error: %v", result.err)
	}

	assertGitignoreContent(t, result.gitignorePath, "bin/\n*.log\n"+runsIgnoreEntry+"\n")
}

func TestRunDoesNotDuplicateExistingGitignoreEntry(t *testing.T) {
	result := runWithGitignore(t, runsIgnoreEntry+"\n", Options{Yes: true})
	if result.err != nil {
		t.Fatalf("Run returned error: %v", result.err)
	}
	content := readGitignore(t, result.gitignorePath)
	if got := strings.Count(string(content), runsIgnoreEntry); got != 1 {
		t.Fatalf(".gitignore contains %s %d times, want 1:\n%s", runsIgnoreEntry, got, string(content))
	}
}

func TestRunRecognizesEquivalentGitignorePatterns(t *testing.T) {
	tests := []string{
		".orc/runs\n",
		"/.orc/runs/\n",
		".orc/runs/**\n",
	}

	for _, existing := range tests {
		t.Run(strings.TrimSpace(existing), func(t *testing.T) {
			result := runWithGitignore(t, existing, Options{Yes: true})
			if result.err != nil {
				t.Fatalf("Run returned error: %v", result.err)
			}

			assertGitignoreContent(t, result.gitignorePath, existing)
			if output := result.stdout.String(); !strings.Contains(output, "exists .gitignore entry "+runsIgnoreEntry) {
				t.Fatalf("output = %q, want existing ignore entry", output)
			}
		})
	}
}

func TestRunRejectsBroadOrcGitignoreBeforeWriting(t *testing.T) {
	tests := []string{
		".orc\n",
		".orc/\n",
		"/.orc/\n",
		".orc/*\n",
		"/.orc/*\n",
		".orc/**\n",
		"/.orc/**\n",
	}

	for _, existing := range tests {
		t.Run(strings.TrimSpace(existing), func(t *testing.T) {
			result := runWithGitignore(t, existing, Options{Yes: true})

			if result.err == nil {
				t.Fatal("Run returned nil error, want broad .orc ignore error")
			}
			for _, want := range []string{"ignores all persistent .orc config", runsIgnoreEntry} {
				if !strings.Contains(result.err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", result.err, want)
				}
			}
			assertGitignoreContent(t, result.gitignorePath, existing)
			assertScaffoldFilesDoNotExist(t, result.root)
		})
	}
}

func TestRunDeclinesDifferingScaffoldOverwriteByDefault(t *testing.T) {
	original := []byte("custom: true\n")
	root, configPath := projectWithCustomConfig(t, original)

	err := Run(Options{Root: root, Stdin: strings.NewReader("\n")})
	if err == nil {
		t.Fatal("Run returned nil error, want declined overwrite")
	}
	if !strings.Contains(err.Error(), "user declined") {
		t.Fatalf("error = %q, want user declined", err)
	}
	content, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if !bytes.Equal(content, original) {
		t.Fatalf("config changed:\n%s", string(content))
	}
}

func TestRunDeclinesMissingGitignoreCreationByDefault(t *testing.T) {
	root := t.TempDir()

	err := Run(Options{Root: root, Stdin: strings.NewReader("\n")})
	if err == nil {
		t.Fatal("Run returned nil error, want declined .gitignore")
	}
	if !strings.Contains(err.Error(), "user declined") {
		t.Fatalf("error = %q, want user declined", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(statErr) {
		t.Fatalf(".gitignore stat err = %v, want not exist", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".orc")); !os.IsNotExist(statErr) {
		t.Fatalf(".orc stat err = %v, want not exist after failed preflight", statErr)
	}
}

func TestRunSkipsMissingInstructionsByDefaultNo(t *testing.T) {
	root, _ := projectWithGitignore(t, []byte(runsIgnoreEntry+"\n"))
	instructionsPath := filepath.Join(root, instructionsName)

	if err := Run(Options{Root: root, Stdin: strings.NewReader("\n")}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if _, err := os.ReadFile(instructionsPath); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md read err = %v, want not exist", err)
	}
}

func TestRunSkipsExistingInstructionsByDefaultNo(t *testing.T) {
	original := "# Existing\n"
	root, instructionsPath := projectWithGitignoreAndInstructions(t, original)

	if err := Run(Options{Root: root, Stdin: strings.NewReader("\n")}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	assertInstructionsContent(t, instructionsPath, []byte(original))
}

func TestRunRejectsDryRunWithYes(t *testing.T) {
	err := Run(Options{Root: t.TempDir(), DryRun: true, Yes: true})
	if err == nil {
		t.Fatal("Run returned nil error, want invalid flags")
	}
	if !strings.Contains(err.Error(), "--dry-run and --yes cannot be used together") {
		t.Fatalf("error = %q, want invalid flag combination", err)
	}
}

func TestRunRejectsOrcSymlinkEscapingRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".orc")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	assertRunYesRejectsAndPathAbsent(t, root, "path must not escape project root", filepath.Join(outside, "config.yaml"))
}

func TestRunRejectsOrcSubdirSymlinkEscapingOrc(t *testing.T) {
	tests := []struct {
		name    string
		link    string
		target  string
		outside string
	}{
		{name: "agents", link: filepath.Join(".orc", "agents"), target: "agent-target", outside: filepath.Join("agent-target", "planner.md")},
		{name: "workflows", link: filepath.Join(".orc", "workflows"), target: "workflow-target", outside: filepath.Join("workflow-target", "implementation.yaml")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".orc"), 0o755); err != nil {
				t.Fatalf("create .orc: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(root, tt.target), 0o755); err != nil {
				t.Fatalf("create target: %v", err)
			}
			if err := os.Symlink(filepath.Join("..", tt.target), filepath.Join(root, tt.link)); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}

			assertRunYesRejectsAndPathAbsent(t, root, "path must not escape project root", filepath.Join(root, tt.outside))
		})
	}
}

func TestRunRejectsDanglingLeafSymlinkEscapingRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".orc"), 0o755); err != nil {
		t.Fatalf("create .orc: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "config.yaml"), filepath.Join(root, ".orc", "config.yaml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	assertRunYesRejectsAndPathAbsent(t, root, "resolve symlink", filepath.Join(outside, "config.yaml"))
}

func TestRunRejectsDanglingGitignoreSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "gitignore"), filepath.Join(root, ".gitignore")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	assertRunYesRejectsAndPathAbsent(t, root, "resolve symlink", filepath.Join(outside, "gitignore"))
}

func TestWriteNewFileRejectsExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	original := []byte("existing\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	assertChangedDuringInitDoesNotOverwrite(t, path, original, func() error {
		return writeNewFile(path, []byte("new\n"))
	})
}

func TestWriteFileIfUnchangedRejectsChangedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	planned := []byte("planned\n")
	changed := []byte("changed\n")
	if err := os.WriteFile(path, changed, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	assertChangedDuringInitDoesNotOverwrite(t, path, changed, func() error {
		return writeFileIfUnchanged(path, planned, []byte("next\n"))
	})
}

func assertChangedDuringInitDoesNotOverwrite(t *testing.T, path string, wantContent []byte, write func() error) {
	t.Helper()
	err := write()
	if err == nil {
		t.Fatal("write returned nil error, want changed during init")
	}
	if !strings.Contains(err.Error(), "changed during init") {
		t.Fatalf("error = %q, want changed during init", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	if !bytes.Equal(content, wantContent) {
		t.Fatalf("file changed to %q, want %q", string(content), string(wantContent))
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(content), want) {
		t.Fatalf("%s = %q, want substring %q", path, string(content), want)
	}
}

func assertGeneratedScaffoldMatchesFixture(t *testing.T, root string) {
	t.Helper()
	for _, path := range scaffoldPaths() {
		assertFileMatchesFixture(t, root, path)
	}
}

func assertFileMatchesFixture(t *testing.T, root, relPath string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read generated %s: %v", relPath, err)
	}
	want, err := os.ReadFile(filepath.Join("scaffold", filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read scaffold source %s: %v", relPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s does not match scaffold source\ngot:\n%s\nwant:\n%s", relPath, string(got), string(want))
	}
}

func projectWithGitignoreAndInstructions(t *testing.T, instructions string) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(runsIgnoreEntry+"\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	instructionsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(instructionsPath, []byte(instructions), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	return root, instructionsPath
}

func projectWithCustomConfig(t *testing.T, content []byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, ".orc", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("create .orc: %v", err)
	}
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return root, configPath
}

func projectWithGitignore(t *testing.T, content []byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	gitignorePath := filepath.Join(root, gitignoreName)
	if err := os.WriteFile(gitignorePath, content, 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	return root, gitignorePath
}

type gitignoreRun struct {
	root          string
	gitignorePath string
	stdout        bytes.Buffer
	err           error
}

func runWithGitignore(t *testing.T, content string, opts Options) gitignoreRun {
	t.Helper()
	root, gitignorePath := projectWithGitignore(t, []byte(content))
	var stdout bytes.Buffer
	opts.Root = root
	if opts.Stdout == nil {
		opts.Stdout = &stdout
	}
	err := Run(opts)
	return gitignoreRun{
		root:          root,
		gitignorePath: gitignorePath,
		stdout:        stdout,
		err:           err,
	}
}

func confirmOverwriteConfigCreateGitignoreAndInstructions() *strings.Reader {
	return strings.NewReader("yes\nyes\nyes\n")
}

func readGitignore(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	return content
}

func assertGitignoreContent(t *testing.T, path, want string) {
	t.Helper()
	if got := string(readGitignore(t, path)); got != want {
		t.Fatalf(".gitignore = %q, want %q", got, want)
	}
}

func assertRunYesRejectsAndPathAbsent(t *testing.T, root, wantErr, path string) {
	t.Helper()
	err := Run(Options{Root: root, Yes: true})
	if err == nil {
		t.Fatal("Run returned nil error, want rejection")
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("error = %q, want substring %q", err, wantErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("%s stat err = %v, want not exist", path, statErr)
	}
}

func assertInstructionsContent(t *testing.T, path string, want []byte) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !bytes.Equal(content, want) {
		t.Fatalf("AGENTS.md = %q, want %q", string(content), string(want))
	}
}

func dryRunScaffoldLines() []string {
	paths := scaffoldPaths()
	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		lines = append(lines, "would create "+path)
	}
	return lines
}

func assertScaffoldFilesDoNotExist(t *testing.T, root string) {
	t.Helper()
	for _, path := range scaffoldPaths() {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !os.IsNotExist(statErr) {
			t.Fatalf("%s stat err = %v, want not exist after failed preflight", path, statErr)
		}
	}
}

func snapshotFiles(t *testing.T, root string, paths []string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		snapshot[path] = content
	}
	return snapshot
}
