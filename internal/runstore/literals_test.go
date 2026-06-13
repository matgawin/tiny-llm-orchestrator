package runstore

const (
	readyForHumanState         = "ready_for_human"
	testProcessStartTime       = "123456789"
	testWorkflowName           = "implementation"
	testWorkflowStatePlan      = "plan"
	testWorkflowStateCode      = "code"
	testWorkflowStateReview    = "review"
	testWorkflowStateTest      = "test"
	testWorkflowSkip           = "skipped"
	testAgentPlanner           = "planner"
	testAgentCoder             = "coder"
	testAttemptID              = "attempt-001"
	testPlanAttemptID          = "plan-attempt-001"
	testTaskSlug               = "task"
	testManualRunID            = "manual-run"
	testOtherRunID             = "other-run"
	testPromptPlanPath         = "prompts/000002-plan.md"
	testReportContent          = "report\n"
	testReportSummaryPlanReady = "Plan is ready."
	reportStatusBlocked        = "blocked"
	reportResultReady          = "ready"
	testExitStateExited        = "exited"
	testOldAttemptID           = "old-attempt"
	testReviewedReason         = "reviewed"

	runstoreLockExitMissingEnv   = 2
	runstoreLockExitOpenStore    = 3
	runstoreLockExitLoad         = 4
	runstoreLockExitParseRef     = 5
	runstoreLockExitReadArtifact = 6
	runstoreLockExitAppendEvent  = 7
	runstoreLockExitUnknownMode  = 8
	runstoreLockExitLockEnv      = 9
	runstoreLockExitLockLoad     = 10
	runstoreLockExitLockOpen     = 11
	runstoreLockExitLockFlock    = 12
	runstoreLockExitLockReady    = 13
	runstoreLockExitLockUnlock   = 14
)
