// Package report validates and persists worker reports.
package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// Options describes an orc report request.
type Options struct {
	Root       string
	JSONFile   string
	Report     runstore.Report
	ReportFile string
	Time       time.Time
}

// Result describes the persisted report action.
type Result struct {
	RunID     string
	Attempt   runstore.Attempt
	Event     runstore.Event
	ReportRef *runstore.ArtifactRef
	Ignored   bool
}

// Submit validates and persists a worker report.
func Submit(ctx context.Context, opts Options) (Result, error) {
	return submit(ctx, opts, nil)
}

func submit(ctx context.Context, opts Options, beforeRecord func()) (Result, error) {
	if ctx == nil {
		return Result{}, stableerr.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("submit: %w", err)
	}
	if opts.Root == "" {
		return Result{}, stableerr.New("project root is required")
	}
	payload, reportFile, schemaErrs, err := loadPayload(opts)
	if err != nil {
		return Result{}, err
	}
	if payload.RunID == "" {
		return Result{}, stableerr.New("run id is required")
	}
	store, err := runstore.Open(opts.Root)
	if err != nil {
		return Result{}, fmt.Errorf("submit: %w", err)
	}
	run, err := store.LoadContext(ctx, payload.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("submit: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("submit: %w", err)
	}
	targetErr := validateCurrentTarget(run.Status.ActiveAttempt, payload)
	if targetErr != nil {
		event, recordErr := store.RecordIgnoredReportContext(ctx, payload.RunID, runstore.IgnoreReportRequest{
			RunID:     payload.RunID,
			StepID:    payload.StepID,
			AgentID:   payload.AgentID,
			AttemptID: payload.AttemptID,
			Reason:    "report does not target current active attempt",
			Errors:    []string{targetErr.Error()},
			Time:      opts.Time,
		})
		if recordErr != nil {
			return Result{}, errors.Join(targetErr, recordErr)
		}
		return Result{RunID: payload.RunID, Event: event, Ignored: true}, targetErr
	}
	validationErrs := append([]string(nil), schemaErrs...)
	validationErrs = append(validationErrs, validatePayloadShape(payload)...)
	var reportContent []byte
	var reportContentSet bool
	if reportFile != "" {
		content, err := readRegularFile(reportFile)
		if err != nil {
			validationErrs = append(validationErrs, fmt.Sprintf("report_file %q: %v", reportFile, err))
		} else {
			reportContent = content
			reportContentSet = true
		}
	}
	if len(validationErrs) > 0 {
		return recordInvalidReport(ctx, store, payload, validationErrs, opts.Time, beforeRecord)
	}

	workflowConfig, err := loadWorkflowConfig(run)
	if err != nil {
		return Result{}, err
	}
	validationErrs = validatePayloadWorkflow(workflowConfig, payload)
	if len(validationErrs) > 0 {
		return recordInvalidReport(ctx, store, payload, validationErrs, opts.Time, beforeRecord)
	}

	return recordValidReport(ctx, store, payload, reportContent, reportContentSet, opts.Time, beforeRecord)
}

func recordInvalidReport(ctx context.Context, store *runstore.Store, report runstore.Report, validationErrs []string, at time.Time, beforeRecord func()) (Result, error) {
	invalid := invalidReport(report, validationErrs)
	callBeforeRecord(beforeRecord)
	attempt, event, recordErr := store.RecordAttemptReportContext(ctx, report.RunID, runstore.RecordReportRequest{
		Report: invalid,
		State:  runstore.AttemptStateInvalidReport,
		Time:   at,
	})
	err := stableerr.New(strings.Join(validationErrs, "; "))
	if recordErr != nil {
		if result, ignoreErr, ignored := recordTargetRaceResult(ctx, store, report, at, recordErr); ignored {
			return result, errors.Join(err, ignoreErr)
		}
		return Result{}, errors.Join(err, recordErr)
	}
	return Result{RunID: report.RunID, Attempt: attempt, Event: event}, err
}

func recordValidReport(ctx context.Context, store *runstore.Store, report runstore.Report, reportContent []byte, reportContentSet bool, at time.Time, beforeRecord func()) (Result, error) {
	req := runstore.RecordReportRequest{
		Report: report,
		State:  runstore.AttemptStateReported,
		Time:   at,
	}
	if reportContentSet {
		report.ReportFile = ""
		req.Report = report
		req.ReportContent = reportContent
		req.ReportContentSet = true
		req.ReportName = report.StepID
	}
	callBeforeRecord(beforeRecord)
	attempt, event, err := store.RecordAttemptReportContext(ctx, report.RunID, req)
	if err != nil {
		if result, ignoreErr, ignored := recordTargetRaceResult(ctx, store, report, at, err); ignored {
			return result, ignoreErr
		}
		return Result{}, fmt.Errorf("record valid report: %w", err)
	}
	return Result{RunID: report.RunID, Attempt: attempt, Event: event, ReportRef: attempt.ReportRef}, nil
}

func callBeforeRecord(beforeRecord func()) {
	if beforeRecord != nil {
		beforeRecord()
	}
}

func recordTargetRaceResult(ctx context.Context, store *runstore.Store, report runstore.Report, at time.Time, err error) (Result, error, bool) {
	ignored, ignoreErr := recordTargetRaceAsIgnored(ctx, store, report, at, err)
	if !ignored {
		return Result{}, nil, false
	}
	return Result{RunID: report.RunID, Event: ignoreErr.Event, Ignored: true}, ignoreErr.Err, true
}

func loadWorkflowConfig(run *runstore.Run) (config.Workflow, error) {
	snapshot, err := configsnapshot.LoadCurrent(run)
	if err != nil {
		return config.Workflow{}, fmt.Errorf("load workflow config: %w", err)
	}
	workflowConfig, ok := snapshot.Project.Workflows[run.Status.Workflow]
	if !ok {
		return config.Workflow{}, stableerr.Errorf("workflow %q from run %q is not configured", run.Status.Workflow, run.ID)
	}
	return workflowConfig, nil
}

type ignoredRaceResult struct {
	Event runstore.Event
	Err   error
}

func recordTargetRaceAsIgnored(ctx context.Context, store *runstore.Store, report runstore.Report, at time.Time, err error) (bool, ignoredRaceResult) {
	var targetErr *runstore.ReportTargetError
	if !errors.As(err, &targetErr) {
		return false, ignoredRaceResult{}
	}
	reason := targetErr.Reason
	if reason == "" {
		reason = "report does not target current active attempt"
	}
	event, recordErr := store.RecordIgnoredReportContext(ctx, report.RunID, runstore.IgnoreReportRequest{
		RunID:     report.RunID,
		StepID:    report.StepID,
		AgentID:   report.AgentID,
		AttemptID: report.AttemptID,
		Reason:    reason,
		Errors:    []string{targetErr.Error()},
		Time:      at,
	})
	if recordErr != nil {
		return true, ignoredRaceResult{Err: errors.Join(err, recordErr)}
	}
	return true, ignoredRaceResult{Event: event, Err: err}
}

func loadPayload(opts Options) (runstore.Report, string, []string, error) {
	if opts.JSONFile == "" {
		report := opts.Report
		if report.ReportRef != nil {
			return runstore.Report{}, "", nil, stableerr.New("report_ref cannot be supplied by callers")
		}
		if report.ReportFile != "" {
			return runstore.Report{}, "", nil, stableerr.New("report_file cannot be supplied by flags; use --report-file")
		}
		return report, opts.ReportFile, nil, nil
	}
	content, err := readRegularFile(opts.JSONFile)
	if err != nil {
		return runstore.Report{}, "", nil, err
	}
	report, schemaErrs, err := parseJSONReport(content)
	if err != nil {
		return runstore.Report{}, "", nil, fmt.Errorf("parse json report %s: %w", opts.JSONFile, err)
	}
	if hasFlagPayload(opts) {
		schemaErrs = append(schemaErrs, "--json-file cannot be combined with report field flags")
	}
	reportFile := report.ReportFile
	report.ReportFile = ""
	report.ReportRef = nil
	return report, reportFile, schemaErrs, nil
}

type jsonReport struct {
	RunID        string         `json:"run_id"`
	StepID       string         `json:"step_id"`
	AgentID      string         `json:"agent_id"`
	AttemptID    string         `json:"attempt_id"`
	Status       string         `json:"status"`
	Result       string         `json:"result"`
	Summary      string         `json:"summary"`
	ChangedPaths []string       `json:"changed_paths"`
	Commands     []string       `json:"commands"`
	Tests        []string       `json:"tests"`
	Risks        []string       `json:"risks"`
	Followups    []jsonFollowup `json:"followups"`
	ReportFile   string         `json:"report_file"`
}

type jsonFollowup struct {
	Title   string `json:"title"`
	Details string `json:"details"`
}

func parseJSONReport(content []byte) (runstore.Report, []string, error) {
	fallback, err := decodeFirstReportLenient(content)
	if err != nil {
		fallback, err = decodeFirstReportIdentity(content)
		if err != nil {
			return runstore.Report{}, nil, err
		}
	}
	decoded, strictErrs := decodeJSONReport(content)
	if len(strictErrs) > 0 {
		return fallback, strictErrs, nil
	}
	return decoded.toReport(), nil, nil
}

func decodeFirstReportLenient(content []byte) (runstore.Report, error) {
	var report runstore.Report
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&report); err != nil {
		return runstore.Report{}, fmt.Errorf("decode first report lenient: %w", err)
	}
	return report, nil
}

func decodeFirstReportIdentity(content []byte) (runstore.Report, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&raw); err != nil {
		return runstore.Report{}, fmt.Errorf("decode first report identity: %w", err)
	}
	return reportIdentityFromRaw(raw), nil
}

func reportIdentityFromRaw(raw map[string]json.RawMessage) runstore.Report {
	return runstore.Report{
		RunID:     rawString(raw, "run_id"),
		StepID:    rawString(raw, "step_id"),
		AgentID:   rawString(raw, "agent_id"),
		AttemptID: rawString(raw, "attempt_id"),
	}
}

func rawString(raw map[string]json.RawMessage, field string) string {
	var value string
	_ = json.Unmarshal(raw[field], &value)
	return value
}

func decodeJSONReport(content []byte) (jsonReport, []string) {
	var report jsonReport
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return jsonReport{}, []string{err.Error()}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return jsonReport{}, []string{"multiple JSON values are not allowed"}
	}
	return report, nil
}

func (report jsonReport) toReport() runstore.Report {
	followups := make([]runstore.Followup, 0, len(report.Followups))
	for _, followup := range report.Followups {
		followups = append(followups, runstore.Followup{
			Title:   followup.Title,
			Details: followup.Details,
		})
	}
	return runstore.Report{
		RunID:        report.RunID,
		StepID:       report.StepID,
		AgentID:      report.AgentID,
		AttemptID:    report.AttemptID,
		Status:       report.Status,
		Result:       report.Result,
		Summary:      report.Summary,
		ChangedPaths: report.ChangedPaths,
		Commands:     report.Commands,
		Tests:        report.Tests,
		Risks:        report.Risks,
		Followups:    followups,
		ReportFile:   report.ReportFile,
	}
}

func hasFlagPayload(opts Options) bool {
	report := opts.Report
	return report.RunID != "" ||
		report.StepID != "" ||
		report.AgentID != "" ||
		report.AttemptID != "" ||
		report.Status != "" ||
		report.Result != "" ||
		report.Summary != "" ||
		len(report.ChangedPaths) > 0 ||
		len(report.Commands) > 0 ||
		len(report.Tests) > 0 ||
		len(report.Risks) > 0 ||
		len(report.Followups) > 0 ||
		report.ReportFile != "" ||
		opts.ReportFile != ""
}

func validateCurrentTarget(active *runstore.Attempt, report runstore.Report) error {
	if active == nil {
		return stableerr.New("run has no active attempt")
	}
	if active.State != runstore.AttemptStateActive {
		return stableerr.Errorf("active attempt %q is %q, want active", active.AttemptID, active.State)
	}
	missing := identityMissing(report)
	if len(missing) > 0 {
		return stableerr.Errorf("report identity is incomplete: missing %s", strings.Join(missing, ", "))
	}
	switch {
	case report.RunID != active.RunID:
		return stableerr.Errorf("report run_id %q does not match active attempt run_id %q", report.RunID, active.RunID)
	case report.StepID != active.StepID:
		return stableerr.Errorf("report step_id %q does not match active attempt step_id %q", report.StepID, active.StepID)
	case report.AgentID != active.AgentID:
		return stableerr.Errorf("report agent_id %q does not match active attempt agent_id %q", report.AgentID, active.AgentID)
	case report.AttemptID != active.AttemptID:
		return stableerr.Errorf("report attempt_id %q does not match active attempt attempt_id %q", report.AttemptID, active.AttemptID)
	default:
		return nil
	}
}

func identityMissing(report runstore.Report) []string {
	var missing []string
	if report.RunID == "" {
		missing = append(missing, "run_id")
	}
	if report.StepID == "" {
		missing = append(missing, "step_id")
	}
	if report.AgentID == "" {
		missing = append(missing, "agent_id")
	}
	if report.AttemptID == "" {
		missing = append(missing, "attempt_id")
	}
	return missing
}

func validatePayloadShape(report runstore.Report) []string {
	var errs []string
	if report.Status == "" {
		errs = append(errs, "status is required")
	}
	if report.Result == "" {
		errs = append(errs, "result is required")
	}
	if strings.TrimSpace(report.Summary) == "" {
		errs = append(errs, "summary is required")
	}
	if !WorkerReportableOutcome(report.Status, report.Result) {
		errs = append(errs, fmt.Sprintf("workers cannot report reserved system outcome %s/%s", report.Status, report.Result))
	}
	for i, followup := range report.Followups {
		if strings.TrimSpace(followup.Title) == "" {
			errs = append(errs, fmt.Sprintf("followups[%d].title is required", i))
		}
	}
	return errs
}

func validatePayloadWorkflow(workflowConfig config.Workflow, report runstore.Report) []string {
	var errs []string
	step, ok := workflowConfig.Steps[report.StepID]
	if !ok {
		errs = append(errs, fmt.Sprintf("step %q is not declared", report.StepID))
		return errs
	}
	results, ok := step.AllowedResults[report.Status]
	if !ok || !slices.Contains(results, report.Result) {
		errs = append(errs, fmt.Sprintf("step %q does not allow %s/%s", report.StepID, report.Status, report.Result))
	}
	return errs
}

// WorkerReportableOutcome reports whether a status/result pair may be authored
// by a worker through orc report.
func WorkerReportableOutcome(status, result string) bool {
	if status == config.SystemSkipStatus && result == config.SystemSkipResult {
		return false
	}
	if status != "failed" {
		return true
	}
	switch result {
	case "error", runstore.AttemptResultInvalidReport, runstore.AttemptResultMissingReport, runstore.AttemptResultProcessError, runstore.AttemptResultTimeout:
		return false
	default:
		return true
	}
}

func invalidReport(report runstore.Report, validationErrs []string) runstore.Report {
	report.Status = "failed"
	report.Result = runstore.AttemptResultInvalidReport
	report.Summary = "Invalid report: " + strings.Join(validationErrs, "; ")
	report.ReportRef = nil
	return report
}

func readRegularFile(path string) ([]byte, error) {
	if path == "" {
		return nil, stableerr.New("path is required")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- caller-provided report path is intentionally read after symlink refusal.
	if err != nil {
		return nil, fmt.Errorf("read regular file: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("read regular file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, stableerr.Errorf("%s is not a regular file", path)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read report file %s: %w", path, err)
	}
	return content, nil
}
