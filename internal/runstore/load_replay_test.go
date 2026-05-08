package runstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadReportsMalformedStateWithArtifactPath(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "broken-run")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	if err := os.Remove(filepath.Join(run.Path, filepath.FromSlash(ref.Path))); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, ref.Path)
}

func TestLoadRejectsBootstrapFIFO(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "events-fifo")
	eventsPath := runEventsPath(run)
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	makeFIFO(t, eventsPath)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "regular file")
}

func TestLoadRejectsRunDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run := createManualRun(t, store, "run-dir-symlink")
	realRunPath := filepath.Join(root, "outside-run")
	relocatePathBehindSymlink(t, run.Path, realRunPath)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "symlink")
}

func TestLoadRejectsMissingBootstrapFiles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		remove      string
		wantContext string
	}{
		{name: "status", remove: statusName, wantContext: "status.json"},
		{name: "events", remove: eventsName, wantContext: "events.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "missing-"+tc.name)
			if err := os.Remove(filepath.Join(run.Path, tc.remove)); err != nil {
				t.Fatalf("remove %s: %v", tc.remove, err)
			}

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.wantContext)
		})
	}
}

func TestLoadRejectsBootstrapFileSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{name: "status", file: statusName},
		{name: "events", file: eventsName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "bootstrap-symlink-"+tc.name)
			path := filepath.Join(run.Path, tc.file)
			outside := filepath.Join(filepath.Dir(run.Path), "outside-"+tc.file)
			if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
				t.Fatalf("write outside %s: %v", tc.file, err)
			}
			replacePathWithSymlink(t, path, outside)

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, "symlink")
		})
	}
}

func TestLoadRejectsHalfCreatedRunDirectory(t *testing.T) {
	store := openStore(t, t.TempDir())
	runDir := filepath.Join(store.runsDir, "half-created")
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		t.Fatalf("mkdir half-created run: %v", err)
	}

	_, err := store.Load("half-created")
	requireErrorContains(t, err, "layout")
}

func TestLoadRejectsMissingInitialRunLayout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		remove      string
		wantContext string
	}{
		{name: "followups", remove: followupsName, wantContext: followupsName},
		{name: "reports", remove: "reports", wantContext: "reports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "missing-layout-"+tc.name)
			if err := os.Remove(filepath.Join(run.Path, tc.remove)); err != nil {
				t.Fatalf("remove %s: %v", tc.remove, err)
			}

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.wantContext)
		})
	}
}

func TestLoadRejectsMalformedArtifactFileShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(t *testing.T, run *Run, artifactPath string)
		want    string
	}{
		{
			name: "directory",
			corrupt: func(t *testing.T, _ *Run, artifactPath string) {
				t.Helper()
				if err := os.Remove(artifactPath); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
				if err := os.Mkdir(artifactPath, 0o750); err != nil {
					t.Fatalf("mkdir artifact path: %v", err)
				}
			},
			want: "is a directory",
		},
		{
			name: "symlink",
			corrupt: func(t *testing.T, run *Run, artifactPath string) {
				t.Helper()
				outsideFileSymlink(t, run.Path, artifactPath, "outside.md", []byte("outside\n"))
			},
			want: "symlink",
		},
		{
			name: "fifo",
			corrupt: func(t *testing.T, _ *Run, artifactPath string) {
				t.Helper()
				if err := os.Remove(artifactPath); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
				makeFIFO(t, artifactPath)
			},
			want: "regular file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run, _, artifactPath := createRunWithReportArtifact(t, store, "artifact-"+tc.name)
			tc.corrupt(t, run, artifactPath)

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadRejectsSymlinkedArtifactParent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref, artifactPath := createRunWithReportArtifact(t, store, "load-symlink-parent")
	reportsDir := filepath.Join(run.Path, "reports")
	if err := os.Remove(artifactPath); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}
	outsideDir := outsideDirSymlink(t, run.Path, reportsDir, "outside-load-reports")
	if err := os.WriteFile(filepath.Join(outsideDir, filepath.Base(ref.Path)), []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "symlink")
}

func TestLoadReportsMalformedEventLine(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "bad-events")
	if err := os.WriteFile(runEventsPath(run), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write bad events: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "events.jsonl", "line 1")
}

func TestLoadRejectsEmptyEventLog(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "empty-events")
	if err := os.WriteFile(runEventsPath(run), nil, 0o600); err != nil {
		t.Fatalf("truncate events: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "no events")
}

func TestLoadRejectsMissingEventPayload(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-event-payload")
	events := readRunEvents(t, run)
	events[0].Payload = nil
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "payload")
}

func TestLoadRejectsEventLogWithoutTrailingNewline(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-event-newline")
	eventsPath := runEventsPath(run)
	content := readFile(t, eventsPath)
	content = bytes.TrimSuffix(content, []byte("\n"))
	if err := os.WriteFile(eventsPath, content, 0o600); err != nil {
		t.Fatalf("write events without trailing newline: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "trailing newline")
}

func TestLoadReadsLargeEventPayload(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "large-event")
	payload, err := json.Marshal(map[string]string{
		"content": string(bytes.Repeat([]byte("x"), 128*1024)),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := store.AppendEvent(run.ID, Event{Type: "workflow.large-payload", Payload: payload}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.Events); got != 2 {
		t.Fatalf("loaded event count = %d, want 2", got)
	}
	if got := len(loaded.Events[1].Payload); got <= 64*1024 {
		t.Fatalf("loaded payload size = %d, want over scanner default limit", got)
	}
}

func TestLoadRejectsMissingRunCreatedEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-created")
	events := readRunEvents(t, run)
	events[0].Type = "workflow.step.selected"
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "run.created")
}

func TestLoadRejectsRunCreatedWorkflowMismatch(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "created-workflow-mismatch")
	events := readRunEvents(t, run)
	events[0].Payload = json.RawMessage(`{"workflow":"other","task_slug":""}`)
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "workflow")
}

func TestLoadRejectsDuplicateRunCreatedEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "duplicate-created")
	events := readRunEvents(t, run)
	duplicate := events[0]
	duplicate.Sequence = 2
	duplicate.Time = duplicate.Time.Add(time.Second)
	writeRunEvents(t, run, append(events, duplicate))

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "duplicate", eventRunCreated)
}

func TestLoadReplaysStatusStateWhenMaterializedStatusIsStale(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "status-mismatch")
	if _, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	status := readRunStatus(t, run)
	status.State = stateRunning
	writeRunStatus(t, run, status)

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.State != readyForHumanState {
		t.Fatalf("loaded state = %q, want replayed %s", loaded.Status.State, readyForHumanState)
	}
}

func TestLoadReplaysArtifactEventWhenMaterializedStatusIsStale(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-artifact-ref")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	status := readRunStatus(t, run)
	status.Artifacts = nil
	writeRunStatus(t, run, status)

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.Status.Artifacts); got != 1 {
		t.Fatalf("loaded artifact refs = %d, want 1", got)
	}
	if loaded.Status.Artifacts[0].Path != ref.Path {
		t.Fatalf("loaded artifact path = %q, want %q", loaded.Status.Artifacts[0].Path, ref.Path)
	}
}

func TestLoadUsesArtifactEventPayloadName(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithMutatedArtifactEvent(t, store, "artifact-payload-mismatch", func(_ *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
		payload.Artifact.Name = "other"
	})

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.Artifacts[0].Name != "other" {
		t.Fatalf("loaded artifact name = %q, want event payload name other", loaded.Status.Artifacts[0].Name)
	}
	if loaded.Status.Artifacts[0].Path != ref.Path {
		t.Fatalf("loaded artifact path = %q, want %q", loaded.Status.Artifacts[0].Path, ref.Path)
	}
}

func TestLoadRejectsMalformedArtifactPayloadRefs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*artifactWrittenPayload)
		want   func(ArtifactRef) []string
	}{
		{
			name: "event sequence mismatch",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.EventSequence = 99
			},
			want: func(ref ArtifactRef) []string {
				return []string{"event_sequence", ref.Path}
			},
		},
		{
			name: "unclean path",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.Path = "reports/../status.json"
			},
			want: func(ArtifactRef) []string {
				return []string{"must be clean"}
			},
		},
		{
			name: "backslash path",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.Path = `reports/000002-plan.md\evil.md`
			},
			want: func(ArtifactRef) []string {
				return []string{"slash separators"}
			},
		},
		{
			name: "kind path mismatch",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.Path = "prompts/000002-plan.md"
			},
			want: func(ArtifactRef) []string {
				return []string{"does not match kind"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			runID := "artifact-" + slugPart(tc.name, "ref")
			run, ref := createRunWithMutatedArtifactEvent(t, store, runID, func(_ *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
				tc.mutate(payload)
			})

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.want(ref)...)
		})
	}
}

func TestLoadRejectsNestedRepeatableArtifactPath(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, _ := createRunWithMutatedArtifactEvent(t, store, "nested-artifact-path", func(run *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
		nestedPath := "reports/000002-plan/evil.md"
		if err := os.Mkdir(filepath.Join(run.Path, "reports", "000002-plan"), 0o750); err != nil {
			t.Fatalf("mkdir nested artifact dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(run.Path, filepath.FromSlash(nestedPath)), []byte("evil\n"), 0o600); err != nil {
			t.Fatalf("write nested artifact: %v", err)
		}
		payload.Artifact.Path = nestedPath
	})

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "does not match kind")
}

func TestLoadRejectsMissingStatusTimestamps(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-status-time")
	status := readRunStatus(t, run)
	status.CreatedAt = time.Time{}
	writeRunStatus(t, run, status)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "created_at")
}

func TestLoadRejectsMissingEventTime(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-event-time")
	events := readRunEvents(t, run)
	events[0].Time = time.Time{}
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "time")
}

func TestLoadRejectsUnsupportedArtifactKind(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithMutatedArtifactEvent(t, store, "unsupported-artifact-kind", func(_ *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
		payload.Artifact.Kind = ArtifactKind("unknown")
	})

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "unsupported artifact kind", ref.Path)
}

func TestLoadIgnoresStatusRefMissingArtifactEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-artifact-event")
	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	events := readRunEvents(t, run)
	if err := writeInitialEventLog(filepath.Join(run.Path, eventsName+".next"), events[0]); err != nil {
		t.Fatalf("write replacement events: %v", err)
	}
	if err := os.Rename(filepath.Join(run.Path, eventsName+".next"), runEventsPath(run)); err != nil {
		t.Fatalf("replace events: %v", err)
	}
	status := readRunStatus(t, run)
	status.LastSequence = 1
	writeRunStatus(t, run, status)

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.Status.Artifacts); got != 0 {
		t.Fatalf("loaded artifact refs = %d, want 0 from event replay", got)
	}
}

func TestLoadRejectsUnsafeRunID(t *testing.T) {
	store := openStore(t, t.TempDir())
	_, err := store.Load("../escape")
	requireErrorContains(t, err, "path separators")
}
