package runstore

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	schemaVersion = 1

	stateRunning        = "running"
	stateBlockedHuman   = "blocked_for_human"
	attemptStatusFailed = "failed"

	eventRunCreated              = "run.created"
	eventStatusUpdated           = "status.updated"
	eventArtifactWritten         = "artifact.written"
	eventAttemptStarted          = "attempt.started"
	eventAttemptPrompted         = "attempt.prompted"
	eventAttemptLogged           = "attempt.logged"
	eventAttemptProcess          = "attempt.process_started"
	eventAttemptFinished         = "attempt.finished"
	eventAttemptRecovered        = "attempt.recovered"
	eventAttemptReported         = "attempt.reported"
	eventAttemptWarning          = "attempt.warning"
	eventReportIgnored           = "report.ignored"
	eventWorkflowSoftCap         = "workflow.loop_soft_cap"
	eventWorkflowHardCap         = "workflow.loop_hard_cap"
	eventWorkflowHardCapOverride = "workflow.loop_hard_cap_override"

	KindTaskContext  ArtifactKind = "task_context"
	KindTaskSnapshot ArtifactKind = "task_snapshot"
	KindReport       ArtifactKind = "report"
	KindPrompt       ArtifactKind = "prompt"
	KindLog          ArtifactKind = "log"
	KindSnapshot     ArtifactKind = "snapshot"
	KindSummary      ArtifactKind = "summary"
	KindFollowup     ArtifactKind = "followup"

	WorkflowLoopHardCapReason = "loop_hard_cap_reached"
)

// Store owns durable run state for one project root.
type Store struct {
	orcDir  string
	runsDir string
}

// ReportTargetError describes a report that no longer targets the active
// attempt observed under the run lock.
type ReportTargetError struct {
	RunID  string
	Reason string
	Err    error
}

func (err *ReportTargetError) Error() string {
	if err == nil || err.Err == nil {
		return "report does not target current active attempt"
	}
	return err.Err.Error()
}

func (err *ReportTargetError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// StateMismatchError reports a locked run state that did not satisfy a write precondition.
type StateMismatchError struct {
	RunID string
	Got   string
	Want  string
}

func (err *StateMismatchError) Error() string {
	if err == nil {
		return "run state did not match expected state"
	}
	return fmt.Sprintf("run %q state is %q, want %q", err.RunID, err.Got, err.Want)
}

// CreateRunRequest describes the durable run identity to create.
type CreateRunRequest struct {
	RunID        string
	Workflow     string
	TaskSlug     string
	InitialState string
	Time         time.Time
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
	SchemaVersion int              `json:"schema_version"`
	RunID         string           `json:"run_id"`
	Workflow      string           `json:"workflow"`
	State         string           `json:"state"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	LastSequence  int              `json:"last_sequence"`
	Artifacts     []ArtifactRef    `json:"artifacts"`
	ActiveAttempt *Attempt         `json:"active_attempt,omitempty"`
	Attempts      []Attempt        `json:"attempts"`
	RetryLineage  *RetryLineage    `json:"retry_lineage,omitempty"`
	Warnings      []AttemptWarning `json:"warnings"`
	WorkflowLoop  WorkflowLoop     `json:"workflow_loop"`
}

// StatusUpdate describes latest-state fields to materialize with an event.
type StatusUpdate struct {
	State              string
	Time               time.Time
	WorkflowStateEntry WorkflowStateEntryRequest
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
	ReportRef        *ArtifactRef `json:"report_ref,omitempty"`
	Report           *Report      `json:"report,omitempty"`
	StartedAt        time.Time    `json:"started_at"`
	FinishedAt       *time.Time   `json:"finished_at,omitempty"`
	Recovered        bool         `json:"recovered,omitempty"`
	SupersededBy     string       `json:"superseded_by,omitempty"`
	SupersededAt     *time.Time   `json:"superseded_at,omitempty"`
	SupersededReason string       `json:"superseded_reason,omitempty"`
}

const (
	AttemptStateStarting      = "starting"
	AttemptStateActive        = "active"
	AttemptStateMissingReport = "missing_report"
	AttemptStateProcessError  = "process_error"
	AttemptStateTimedOut      = "timed_out"
	AttemptStateReported      = "reported"
	AttemptStateInvalidReport = "invalid_report"

	AttemptResultMissingReport = "missing_report"
	AttemptResultProcessError  = "process_error"
	AttemptResultTimeout       = "timeout"
	AttemptResultInvalidReport = "invalid_report"
)

// Report is the structured worker report persisted on a terminal attempt.
type Report struct {
	RunID        string       `json:"run_id"`
	StepID       string       `json:"step_id"`
	AgentID      string       `json:"agent_id"`
	AttemptID    string       `json:"attempt_id"`
	Status       string       `json:"status"`
	Result       string       `json:"result"`
	Summary      string       `json:"summary"`
	ChangedPaths []string     `json:"changed_paths,omitempty"`
	Commands     []string     `json:"commands,omitempty"`
	Tests        []string     `json:"tests,omitempty"`
	Risks        []string     `json:"risks,omitempty"`
	Followups    []Followup   `json:"followups,omitempty"`
	ReportFile   string       `json:"report_file,omitempty"`
	ReportRef    *ArtifactRef `json:"report_ref,omitempty"`
}

// Followup is a report-proposed follow-up item. The follow-up artifact is owned
// by a later workflow slice; v1 report persistence only preserves suggestions.
type Followup struct {
	Title   string `json:"title"`
	Details string `json:"details,omitempty"`
}

// RetryLineage records retry counts consumed in the current step execution lineage.
type RetryLineage struct {
	StepID string         `json:"step_id"`
	Counts map[string]int `json:"counts,omitempty"`
}

// AttemptWarning records a process fact that does not change the authoritative
// terminal outcome of a worker attempt.
type AttemptWarning struct {
	AttemptID string    `json:"attempt_id"`
	Kind      string    `json:"kind"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	ExitState string    `json:"exit_state,omitempty"`
	Message   string    `json:"message,omitempty"`
	Time      time.Time `json:"time"`
}

// WorkflowLoop records workflow state entries accepted into durable run state.
type WorkflowLoop struct {
	Counts                 map[string]int               `json:"counts,omitempty"`
	Entries                []WorkflowStateEntry         `json:"entries,omitempty"`
	RepeatedStates         []string                     `json:"repeated_states,omitempty"`
	SoftCapWarnings        []WorkflowLoopSoftCap        `json:"soft_cap_warnings,omitempty"`
	HardCapBlock           *WorkflowLoopHardCap         `json:"hard_cap_block,omitempty"`
	PendingHardCapOverride *WorkflowLoopHardCapOverride `json:"pending_hard_cap_override,omitempty"`
}

// WorkflowStateEntry records one accepted workflow state entry.
type WorkflowStateEntry struct {
	Workflow      string `json:"workflow"`
	State         string `json:"state"`
	Count         int    `json:"count"`
	Repeated      bool   `json:"repeated,omitempty"`
	PreviousState string `json:"previous_state,omitempty"`
	TriggerStatus string `json:"trigger_status,omitempty"`
	TriggerResult string `json:"trigger_result,omitempty"`
}

// WorkflowStateEntryRequest describes workflow state entry metadata supplied by
// routing callers. The run store computes Count and Repeated atomically.
type WorkflowStateEntryRequest struct {
	State         string
	PreviousState string
	TriggerStatus string
	TriggerResult string
}

// WorkflowLoopSoftCap records the first advisory soft-cap hit for a workflow state.
type WorkflowLoopSoftCap struct {
	Workflow      string `json:"workflow"`
	State         string `json:"state"`
	Count         int    `json:"count"`
	Soft          int    `json:"soft"`
	Hard          int    `json:"hard"`
	PreviousState string `json:"previous_state,omitempty"`
	TriggerStatus string `json:"trigger_status,omitempty"`
	TriggerResult string `json:"trigger_result,omitempty"`
}

// WorkflowLoopHardCap records the deterministic human-decision stop before a
// worker-selecting state would exceed the configured hard cap.
type WorkflowLoopHardCap struct {
	Workflow         string `json:"workflow"`
	BlockedState     string `json:"blocked_target_state"`
	CurrentCount     int    `json:"current_count"`
	ProspectiveCount int    `json:"prospective_count"`
	Soft             int    `json:"soft"`
	Hard             int    `json:"hard"`
	PreviousState    string `json:"previous_state,omitempty"`
	TriggerStatus    string `json:"trigger_status,omitempty"`
	TriggerResult    string `json:"trigger_result,omitempty"`
	Reason           string `json:"reason"`
}

// WorkflowLoopHardCapOverride records a human-reviewed one-shot continuation
// from an active workflow loop hard-cap block.
type WorkflowLoopHardCapOverride struct {
	Workflow            string `json:"workflow"`
	TargetState         string `json:"target_state"`
	CountBeforeOverride int    `json:"count_before_override"`
	CountAfterOverride  int    `json:"count_after_override"`
	Soft                int    `json:"soft"`
	Hard                int    `json:"hard"`
	HumanAction         string `json:"human_action"`
	Reason              string `json:"reason"`
}

// FollowupSource identifies where a recorded follow-up was proposed.
type FollowupSource string

const (
	FollowupSourceReport       FollowupSource = "report"
	FollowupSourceOrchestrator FollowupSource = "orchestrator"
)

// RecordFollowupRequest describes one follow-up entry to append to followups.md.
type RecordFollowupRequest struct {
	Followup  Followup
	Source    FollowupSource
	StepID    string
	AgentID   string
	AttemptID string
	Time      time.Time
}

type createRunPayload struct {
	Workflow           string              `json:"workflow"`
	TaskSlug           string              `json:"task_slug,omitempty"`
	WorkflowStateEntry *WorkflowStateEntry `json:"workflow_state_entry,omitempty"`
}

type statusUpdatedPayload struct {
	State              string              `json:"state"`
	WorkflowStateEntry *WorkflowStateEntry `json:"workflow_state_entry,omitempty"`
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
	// ConsumeAttemptID records the latest terminal attempt ID this start consumes.
	ConsumeAttemptID string
	// RetryLineage records updated retry counts when the consumed outcome is retried.
	RetryLineage *RetryLineage
	// SupersedeReason records the consumed status/result pair that triggered the retry.
	SupersedeReason string
	// WorkflowStateEntry records an accepted workflow state entry for worker-selecting routing.
	WorkflowStateEntry WorkflowStateEntryRequest
	// ConsumeWorkflowLoopHardCapOverride consumes the pending one-shot human override for this state entry.
	ConsumeWorkflowLoopHardCapOverride *WorkflowLoopHardCapOverride
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

// RecordReportRequest terminalizes the active attempt with a structured report.
type RecordReportRequest struct {
	Report           Report
	State            string
	ReportContent    []byte
	ReportContentSet bool
	ReportName       string
	Time             time.Time
}

// IgnoreReportRequest records a report that did not target the active attempt.
type IgnoreReportRequest struct {
	RunID     string
	StepID    string
	AgentID   string
	AttemptID string
	Reason    string
	Errors    []string
	Time      time.Time
}

type attemptStartedPayload struct {
	Attempt                             Attempt                      `json:"attempt"`
	ConsumeAttemptID                    string                       `json:"consume_attempt_id,omitempty"`
	RetryLineage                        *RetryLineage                `json:"retry_lineage,omitempty"`
	SupersedeReason                     string                       `json:"supersede_reason,omitempty"`
	WorkflowStateEntry                  *WorkflowStateEntry          `json:"workflow_state_entry,omitempty"`
	ConsumedWorkflowLoopHardCapOverride *WorkflowLoopHardCapOverride `json:"consumed_workflow_loop_hard_cap_override,omitempty"`
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

type attemptReportedPayload struct {
	AttemptID    string        `json:"attempt_id"`
	State        string        `json:"state"`
	Report       Report        `json:"report"`
	FollowupRefs []ArtifactRef `json:"followup_refs,omitempty"`
}

type attemptWarningPayload struct {
	Warning AttemptWarning `json:"warning"`
}

type reportIgnoredPayload struct {
	RunID     string   `json:"run_id,omitempty"`
	StepID    string   `json:"step_id,omitempty"`
	AgentID   string   `json:"agent_id,omitempty"`
	AttemptID string   `json:"attempt_id,omitempty"`
	Reason    string   `json:"reason"`
	Errors    []string `json:"errors,omitempty"`
}

type workflowLoopSoftCapPayload struct {
	Cap WorkflowLoopSoftCap `json:"cap"`
}

type workflowLoopHardCapPayload struct {
	Cap   WorkflowLoopHardCap `json:"cap"`
	State string              `json:"state"`
}

type workflowLoopHardCapOverridePayload struct {
	Override WorkflowLoopHardCapOverride `json:"override"`
	State    string                      `json:"state"`
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
