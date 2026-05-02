package runstore

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	schemaVersion = 1

	stateRunning = "running"

	eventRunCreated      = "run.created"
	eventStatusUpdated   = "status.updated"
	eventArtifactWritten = "artifact.written"

	KindTaskContext  ArtifactKind = "task_context"
	KindTaskSnapshot ArtifactKind = "task_snapshot"
	KindReport       ArtifactKind = "report"
	KindPrompt       ArtifactKind = "prompt"
	KindLog          ArtifactKind = "log"
	KindSnapshot     ArtifactKind = "snapshot"
	KindSummary      ArtifactKind = "summary"
	KindFollowup     ArtifactKind = "followup"
)

// Store owns durable run state for one project root.
type Store struct {
	orcDir  string
	runsDir string
}

// CreateRunRequest describes the durable run identity to create.
type CreateRunRequest struct {
	RunID    string
	Workflow string
	TaskSlug string
	Time     time.Time
}

// Run is the structured state recovered from a run directory.
type Run struct {
	ID     string
	Path   string
	Status Status
	Events []Event
}

// Status is the materialized latest state stored in status.json.
type Status struct {
	SchemaVersion int           `json:"schema_version"`
	RunID         string        `json:"run_id"`
	Workflow      string        `json:"workflow"`
	State         string        `json:"state"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	LastSequence  int           `json:"last_sequence"`
	Artifacts     []ArtifactRef `json:"artifacts"`
}

// StatusUpdate describes latest-state fields to materialize with an event.
type StatusUpdate struct {
	State string
	Time  time.Time
}

// Event is one append-only event log entry.
type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Sequence      int             `json:"sequence"`
	Time          time.Time       `json:"time"`
	RunID         string          `json:"run_id"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
}

// ArtifactKind identifies the durable artifact namespace.
type ArtifactKind string

// Artifact is content the store should persist under a run directory.
type Artifact struct {
	Kind    ArtifactKind
	Name    string
	Content []byte
	Time    time.Time
}

// ArtifactRef points at a persisted artifact relative to the run directory.
type ArtifactRef struct {
	Kind          ArtifactKind `json:"kind"`
	Path          string       `json:"path"`
	Name          string       `json:"name,omitempty"`
	EventSequence int          `json:"event_sequence"`
}

type createRunPayload struct {
	Workflow string `json:"workflow"`
	TaskSlug string `json:"task_slug,omitempty"`
}

type statusUpdatedPayload struct {
	State string `json:"state"`
}

type artifactWrittenPayload struct {
	Artifact ArtifactRef `json:"artifact"`
}

// StatusMaterializationError means durable event/artifact state committed, but status.json was not refreshed.
type StatusMaterializationError struct {
	RunID string
	Path  string
	Err   error
}

func (e *StatusMaterializationError) Error() string {
	return fmt.Sprintf("run %q status.json materialization after commit at %s: %v", e.RunID, e.Path, e.Err)
}

func (e *StatusMaterializationError) Unwrap() error {
	return e.Err
}

// EventAppendError means appending an event failed after the append outcome became ambiguous.
type EventAppendError struct {
	Path             string
	PossiblyAppended bool
	Err              error
}

func (e *EventAppendError) Error() string {
	if e.PossiblyAppended {
		return fmt.Sprintf("%s append possibly committed: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("%s append failed: %v", e.Path, e.Err)
}

func (e *EventAppendError) Unwrap() error {
	return e.Err
}
