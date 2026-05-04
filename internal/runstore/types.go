package runstore

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	schemaVersion = 1

	stateRunning        = "running"
	attemptStatusFailed = "failed"

	eventRunCreated       = "run.created"
	eventStatusUpdated    = "status.updated"
	eventArtifactWritten  = "artifact.written"
	eventAttemptStarted   = "attempt.started"
	eventAttemptPrompted  = "attempt.prompted"
	eventAttemptLogged    = "attempt.logged"
	eventAttemptProcess   = "attempt.process_started"
	eventAttemptFinished  = "attempt.finished"
	eventAttemptRecovered = "attempt.recovered"

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
	ActiveAttempt *Attempt      `json:"active_attempt,omitempty"`
	Attempts      []Attempt     `json:"attempts"`
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

// Attempt is a durable worker attempt materialized from attempt events.
type Attempt struct {
	RunID            string       `json:"run_id"`
	StepID           string       `json:"step_id"`
	AgentID          string       `json:"agent_id"`
	AttemptID        string       `json:"attempt_id"`
	State            string       `json:"state"`
	Status           string       `json:"status,omitempty"`
	Result           string       `json:"result,omitempty"`
	PID              int          `json:"pid,omitempty"`
	ProcessStartTime string       `json:"process_start_time,omitempty"`
	ExitCode         *int         `json:"exit_code,omitempty"`
	ExitState        string       `json:"exit_state,omitempty"`
	Timeout          string       `json:"timeout"`
	ReportExitGrace  string       `json:"report_exit_grace"`
	PromptRef        *ArtifactRef `json:"prompt_ref,omitempty"`
	LogRef           *ArtifactRef `json:"log_ref,omitempty"`
	StartedAt        time.Time    `json:"started_at"`
	FinishedAt       *time.Time   `json:"finished_at,omitempty"`
	Recovered        bool         `json:"recovered,omitempty"`
}

const (
	AttemptStateStarting      = "starting"
	AttemptStateActive        = "active"
	AttemptStateMissingReport = "missing_report"
	AttemptStateProcessError  = "process_error"
	AttemptStateTimedOut      = "timed_out"

	AttemptResultMissingReport = "missing_report"
	AttemptResultProcessError  = "process_error"
	AttemptResultTimeout       = "timeout"
)

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

// StartAttemptRequest describes the active worker attempt to persist.
type StartAttemptRequest struct {
	StepID          string
	AgentID         string
	AttemptID       string
	Timeout         time.Duration
	ReportExitGrace time.Duration
	Time            time.Time
}

// AttemptPromptRequest links the rendered prompt artifact to the active attempt.
type AttemptPromptRequest struct {
	AttemptID string
	PromptRef ArtifactRef
	Time      time.Time
}

// AttemptLogRequest links a log artifact to the active attempt.
type AttemptLogRequest struct {
	AttemptID string
	LogRef    ArtifactRef
	Time      time.Time
}

// AttemptProcessRequest records process metadata for the active attempt.
type AttemptProcessRequest struct {
	AttemptID        string
	PID              int
	ProcessStartTime string
	Time             time.Time
}

// FinishAttemptRequest terminalizes the active attempt with a synthesized outcome.
type FinishAttemptRequest struct {
	AttemptID string
	State     string
	Status    string
	Result    string
	ExitCode  *int
	ExitState string
	LogRef    *ArtifactRef
	Time      time.Time
}

type attemptStartedPayload struct {
	Attempt Attempt `json:"attempt"`
}

type attemptPromptedPayload struct {
	AttemptID string      `json:"attempt_id"`
	PromptRef ArtifactRef `json:"prompt_ref"`
}

type attemptLoggedPayload struct {
	AttemptID string      `json:"attempt_id"`
	LogRef    ArtifactRef `json:"log_ref"`
}

type attemptProcessPayload struct {
	AttemptID        string `json:"attempt_id"`
	PID              int    `json:"pid"`
	ProcessStartTime string `json:"process_start_time,omitempty"`
}

type attemptFinishedPayload struct {
	AttemptID string       `json:"attempt_id"`
	State     string       `json:"state"`
	Status    string       `json:"status"`
	Result    string       `json:"result"`
	ExitCode  *int         `json:"exit_code,omitempty"`
	ExitState string       `json:"exit_state,omitempty"`
	LogRef    *ArtifactRef `json:"log_ref,omitempty"`
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
