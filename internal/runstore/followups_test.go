package runstore

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArtifactAppendsFollowupsToSingleFile(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "followup-append")

	first, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "first", Content: []byte("- first\n")})
	if err != nil {
		t.Fatalf("WriteArtifact first followup returned error: %v", err)
	}

	second, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "second", Content: []byte("- second\n")})
	if err != nil {
		t.Fatalf("WriteArtifact second followup returned error: %v", err)
	}

	if first.Path != followupsName || second.Path != followupsName {
		t.Fatalf("followup paths = %q, %q; want %s", first.Path, second.Path, followupsName)
	}

	if first.EventSequence == second.EventSequence {
		t.Fatalf("followup event sequences both = %d, want distinct sequences", first.EventSequence)
	}

	content := readFile(t, filepath.Join(run.Path, followupsName))
	if got, want := string(content), "- first\n- second\n"; got != want {
		t.Fatalf("followups.md content = %q, want %q", got, want)
	}

	status := readRunStatus(t, run)
	if got := len(status.Artifacts); got != 2 {
		t.Fatalf("status artifact refs = %d, want 2", got)
	}

	if status.Artifacts[0] != first || status.Artifacts[1] != second {
		t.Fatalf("status artifact refs = %+v, want [%+v %+v]", status.Artifacts, first, second)
	}
}

func TestRecordFollowupAppendsAttributedMarkdown(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "typed-followup-append")
	recordedAt := time.Date(2026, 5, 4, 12, 3, 0, 0, time.UTC)

	first, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{
			Title:   "Document report summaries",
			Details: "Capture summary context once follow-up recording lands.",
		},
		Source:    FollowupSourceReport,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
		Time:      recordedAt,
	})
	if err != nil {
		t.Fatalf("RecordFollowup report returned error: %v", err)
	}

	second, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: "Create release note"},
		Source:   FollowupSourceOrchestrator,
		Time:     recordedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordFollowup orchestrator returned error: %v", err)
	}

	if first.Kind != KindFollowup || second.Kind != KindFollowup || first.Path != followupsName || second.Path != followupsName {
		t.Fatalf("followup refs = %+v %+v, want followups.md refs", first, second)
	}

	content := string(readFile(t, filepath.Join(run.Path, followupsName)))

	want := `## Document report summaries

Source: report
Step: plan
Agent: planner
Attempt: attempt-001
Recorded-At: 2026-05-04T12:03:00Z

Capture summary context once follow-up recording lands.

## Create release note

Source: orchestrator
Recorded-At: 2026-05-04T12:04:00Z

`
	if content != want {
		t.Fatalf("followups.md = %q, want %q", content, want)
	}

	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if got := len(loaded.Status.Artifacts); got != 2 {
		t.Fatalf("artifact count = %d, want 2", got)
	}
}

func TestRecordFollowupUsesSameDefaultTimeForMarkdownAndEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "typed-followup-zero-time")

	ref, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: "Create release note"},
		Source:   FollowupSourceOrchestrator,
	})
	if err != nil {
		t.Fatalf("RecordFollowup returned error: %v", err)
	}

	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	event := loaded.Events[len(loaded.Events)-1]
	if ref.EventSequence != event.Sequence {
		t.Fatalf("ref sequence = %d, want event sequence %d", ref.EventSequence, event.Sequence)
	}

	want := "Recorded-At: " + event.Time.Format(time.RFC3339)
	if got := string(readFile(t, filepath.Join(run.Path, followupsName))); !strings.Contains(got, want) {
		t.Fatalf("followups.md = %q, want %q", got, want)
	}
}

func TestRecordFollowupRequiresTitleAndReportAttribution(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "typed-followup-validation")

	_, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: " \t"},
		Source:   FollowupSourceOrchestrator,
	})
	requireErrorContains(t, err, "title is required")
	_, err = store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: "Report follow-up"},
		Source:   FollowupSourceReport,
		StepID:   "plan",
	})
	requireErrorContains(t, err, "agent id is required")

	if got := string(readFile(t, filepath.Join(run.Path, followupsName))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestWriteArtifactRejectsPreexistingFollowupsSymlink(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "followup-symlink")
	followupsPath := filepath.Join(run.Path, followupsName)
	outside := outsideFileSymlink(t, run.Path, followupsPath, "outside-followups.md", []byte("outside\n"))

	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "next", Content: []byte("inside\n")})
	requireErrorContains(t, err, "symlink")

	if got := string(readFile(t, outside)); got != "outside\n" {
		t.Fatalf("outside followups content = %q, want unchanged external content", got)
	}
}

func TestWriteArtifactRollsBackFollowupAppendWhenEventAppendFails(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "followup-rollback")

	first := []byte("- first\n")
	if _, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "first", Content: first}); err != nil {
		t.Fatalf("initial WriteArtifact returned error: %v", err)
	}

	eventsPath := runEventsPath(run)
	eventsBefore := readFile(t, eventsPath)
	denyFileAppendOrSkip(t, eventsPath)

	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "second", Content: []byte("- second\n")})
	if err == nil {
		t.Fatal("WriteArtifact returned nil error, want append failure")
	}

	if ref.Path != "" {
		t.Fatalf("ref = %+v, want zero ref on definite append failure", ref)
	}

	if got := readFile(t, filepath.Join(run.Path, followupsName)); !bytes.Equal(got, first) {
		t.Fatalf("followups.md content = %q, want restored original %q", string(got), string(first))
	}

	if got := readFile(t, eventsPath); !bytes.Equal(got, eventsBefore) {
		t.Fatalf("events changed after failed followup append:\nbefore: %s\nafter:  %s", eventsBefore, got)
	}
}
