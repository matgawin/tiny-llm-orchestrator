package workflow

// Run status values owned by the workflow/report layers.
const (
	RunStatusRunning         = "running"
	RunStatusReadyForHuman   = "ready_for_human"
	RunStatusBlockedForHuman = "blocked_for_human"
	RunStatusCancelled       = "cancelled"
)

// Worker report status values accepted by v1 workflows.
const (
	ReportStatusDone    = "done"
	ReportStatusBlocked = "blocked"
	ReportStatusFailed  = "failed"
)

// DecisionKind identifies the next deterministic workflow action.
type DecisionKind string

const (
	DecisionSelectStep        DecisionKind = "select_step"
	DecisionRetryStep         DecisionKind = "retry_step"
	DecisionWaitActiveAttempt DecisionKind = "wait_active_attempt"
	DecisionTerminal          DecisionKind = "terminal"
)

// RunState is the in-memory workflow state needed for deterministic routing.
type RunState struct {
	Status        string
	SelectedStep  string
	ActiveAttempt bool
	Outcome       *Outcome
	Retry         RetryLineage
}

// Outcome is a terminal worker outcome for the selected step.
type Outcome struct {
	Step   string
	Status string
	Result string
}

// RetryLineage records retries consumed per status/result pair in the current
// step execution lineage.
type RetryLineage struct {
	Step   string
	Counts map[string]int
}

// Decision is the workflow engine's next action.
type Decision struct {
	Kind      DecisionKind
	Step      string
	RunStatus string
	Retry     RetryLineage
}
