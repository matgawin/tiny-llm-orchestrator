package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCreateRunCreatesInitialArtifacts(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	now := time.Date(2026, 5, 2, 14, 30, 22, 0, time.UTC)

	run, err := store.Create(CreateRunRequest{
		Workflow: "implementation",
		TaskSlug: "main-997",
		Time:     now,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if ok := regexp.MustCompile(`^20260502T143022Z-implementation-main-997-[0-9a-f]{6}$`).MatchString(run.ID); !ok {
		t.Fatalf("run id %q did not match expected safe generated shape", run.ID)
	}
	assertDir(t, filepath.Join(root, ".orc", "runs", run.ID))
	for _, rel := range artifactDirs() {
		assertDir(t, filepath.Join(run.Path, rel))
	}
	assertDir(t, filepath.Join(run.Path, configDirName))
	assertFile(t, filepath.Join(run.Path, followupsName))
	status := readRunStatus(t, run)
	if status.RunID != run.ID || status.Workflow != "implementation" || status.State != stateRunning {
		t.Fatalf("status = %+v, want run/workflow/running state", status)
	}
	if status.LastSequence != 1 {
		t.Fatalf("last sequence = %d, want 1", status.LastSequence)
	}
	events := readRunEvents(t, run)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Type != eventRunCreated || events[0].Sequence != 1 || events[0].RunID != run.ID {
		t.Fatalf("event = %+v, want initial run.created event", events[0])
	}
}

func TestCreateRunPersistsInitialWorkflowStateEntry(t *testing.T) {
	store := openStore(t, t.TempDir())
	now := time.Date(2026, 5, 2, 14, 30, 22, 0, time.UTC)

	run, err := store.Create(CreateRunRequest{
		RunID:        "loop-run",
		Workflow:     "implementation",
		InitialState: "code",
		Time:         now,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if got := run.Status.WorkflowLoop.Counts["code"]; got != 1 {
		t.Fatalf("code count = %d, want 1", got)
	}
	if got := len(run.Status.WorkflowLoop.Entries); got != 1 {
		t.Fatalf("entry count = %d, want 1", got)
	}
	entry := run.Status.WorkflowLoop.Entries[0]
	if entry.Workflow != "implementation" || entry.State != "code" || entry.Count != 1 || entry.Repeated {
		t.Fatalf("entry = %+v, want initial code count", entry)
	}

	events := readRunEvents(t, run)
	var payload createRunPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal run.created payload: %v", err)
	}
	if payload.WorkflowStateEntry == nil || *payload.WorkflowStateEntry != entry {
		t.Fatalf("payload entry = %+v, want %+v", payload.WorkflowStateEntry, entry)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := loaded.Status.WorkflowLoop.Counts["code"]; got != 1 {
		t.Fatalf("loaded code count = %d, want 1", got)
	}
}

func TestCreateRunRejectsExplicitIDCollision(t *testing.T) {
	store := openStore(t, t.TempDir())
	req := CreateRunRequest{RunID: "manual-run", Workflow: "implementation"}
	if _, err := store.Create(req); err != nil {
		t.Fatalf("first Create returned error: %v", err)
	}
	_, err := store.Create(req)
	requireErrorContains(t, err, "already exists")
}

func TestCreateRunRejectsExplicitIDEmptyDirectoryCollision(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".orc", "runs", "manual-run"), 0o750); err != nil {
		t.Fatalf("create empty run directory collision: %v", err)
	}

	_, err := store.Create(CreateRunRequest{RunID: "manual-run", Workflow: "implementation"})
	requireErrorContains(t, err, "already exists")
}

func TestCreateRunExplicitIDConcurrentCollision(t *testing.T) {
	store := openStore(t, t.TempDir())
	req := CreateRunRequest{RunID: "manual-run", Workflow: "implementation"}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			_, err := store.Create(req)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	failures := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("Create error = %v, want already exists", err)
		}
		failures++
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("Create results = %d successes %d failures, want one of each", successes, failures)
	}
	if _, err := store.Load(req.RunID); err != nil {
		t.Fatalf("Load returned error after concurrent create: %v", err)
	}
}

func TestCreateRunIDSlugFallbackAndTruncation(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, err := store.Create(CreateRunRequest{
		Workflow: "Implementation!!! Workflow",
		TaskSlug: "!!!",
		Time:     time.Date(2026, 5, 2, 14, 30, 22, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if ok := regexp.MustCompile(`^20260502T143022Z-implementation-workflow-task-[0-9a-f]{6}$`).MatchString(run.ID); !ok {
		t.Fatalf("run id %q did not use sanitized workflow and task fallback", run.ID)
	}

	longRun, err := store.Create(CreateRunRequest{
		Workflow: strings.Repeat("a", 80),
		TaskSlug: "task",
		Time:     time.Date(2026, 5, 2, 14, 31, 22, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create with long workflow returned error: %v", err)
	}
	parts := strings.Split(longRun.ID, "-")
	if len(parts[1]) != 48 {
		t.Fatalf("workflow slug len = %d, want 48 in %q", len(parts[1]), longRun.ID)
	}
}

func TestCreateRejectsUnsluggableWorkflowForGeneratedRunID(t *testing.T) {
	store := openStore(t, t.TempDir())

	_, err := store.Create(CreateRunRequest{Workflow: "!!!", TaskSlug: "main-997"})
	requireErrorContains(t, err, "workflow slug")
}

func TestCreateRejectsInvalidExplicitRunIDs(t *testing.T) {
	store := openStore(t, t.TempDir())
	for _, id := range []string{".", "..", "a/b", `a\b`, "has space"} {
		t.Run(id, func(t *testing.T) {
			_, err := store.Create(CreateRunRequest{RunID: id, Workflow: "implementation"})
			requireErrorContains(t, err)
		})
	}
}

func TestCreateGeneratesRunIDWhenExplicitIDEmpty(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.Create(CreateRunRequest{RunID: "", Workflow: "implementation"})
	if err != nil {
		t.Fatalf("Create with empty id returned error: %v", err)
	}
	if run.ID == "" {
		t.Fatal("run id is empty, want generated id")
	}
}

func TestCreateRejectsSymlinkedStoreParents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "orc",
			setup: func(t *testing.T, root string) {
				t.Helper()
				outside := filepath.Join(root, "outside-orc")
				if err := os.Mkdir(outside, 0o750); err != nil {
					t.Fatalf("mkdir outside .orc: %v", err)
				}
				symlinkPath(t, filepath.Join(root, orcDirName), outside)
			},
		},
		{
			name: "runs",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, orcDirName), 0o750); err != nil {
					t.Fatalf("mkdir .orc: %v", err)
				}
				outside := filepath.Join(root, "outside-runs")
				if err := os.Mkdir(outside, 0o750); err != nil {
					t.Fatalf("mkdir outside runs: %v", err)
				}
				symlinkPath(t, filepath.Join(root, orcDirName, runsDirName), outside)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			store := openStore(t, root)

			_, err := store.Create(CreateRunRequest{RunID: "parent-symlink", Workflow: "implementation"})
			requireErrorContains(t, err, "symlink")
		})
	}
}
