package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/initupgrade"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

type initUpgradeOptions struct {
	Apply bool
	JSON  bool
}

const (
	initUpgradeStatusCurrent = "current"
	initUpgradeStatusPlanned = "planned"
	initUpgradeStatusApplied = "applied"
	initUpgradeStatusPartial = "partial"
	initUpgradeStatusBlocked = "blocked"
	initUpgradeStatusFailed  = "failed"
)

type initUpgradeJSON struct {
	ProjectRoot         string                       `json:"project_root"`
	Status              string                       `json:"status"`
	ConfigSchemaVersion int                          `json:"config_schema_version"`
	CurrentSetupVersion int                          `json:"current_setup_version"`
	TargetSetupVersion  int                          `json:"target_setup_version"`
	Actions             []initUpgradeActionJSON      `json:"actions"`
	Warnings            []initupgrade.Warning        `json:"warnings"`
	Conflicts           []initupgrade.Conflict       `json:"conflicts"`
	SkippedActions      []initupgrade.SkippedAction  `json:"skipped_actions"`
	StaleFiles          []initupgrade.StaleFile      `json:"stale_files"`
	AffectedPaths       []initupgrade.AffectedPath   `json:"affected_paths"`
	FollowUps           []initupgrade.FollowUp       `json:"follow_ups"`
	Applied             bool                         `json:"applied"`
	Refused             bool                         `json:"refused"`
	CreatedPaths        []string                     `json:"created_paths,omitempty"`
	ModifiedPaths       []string                     `json:"modified_paths,omitempty"`
	ApplyRefusal        *initUpgradeApplyRefusalJSON `json:"apply_refusal,omitempty"`
}

type initUpgradeActionJSON struct {
	Kind      initupgrade.ActionKind     `json:"kind"`
	Path      string                     `json:"path"`
	Reason    string                     `json:"reason"`
	DependsOn []string                   `json:"depends_on,omitempty"`
	Edits     []initupgrade.SurgicalEdit `json:"edits,omitempty"`
}

type initUpgradeApplyRefusalJSON struct {
	Reason string `json:"reason"`
}

func newInitUpgradeCommand(stdout, stderr io.Writer) *cobra.Command {
	opts := initUpgradeOptions{}
	cmd := &cobra.Command{
		Use:           commandUpgrade,
		Short:         "Plan or apply project setup upgrades",
		Long:          initUpgradeHelpLong(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return initUpgradeFlagError(cmd, stderr, stableerr.Errorf("unexpected argument %q", args[0]))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeInitUpgrade(opts, stdout, stderr)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.Apply, "apply", false, "Apply safe planned changes")
	flags.BoolVar(&opts.JSON, "json", false, "Emit machine-readable upgrade output")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return initUpgradeFlagError(cmd, stderr, err)
	})

	return cmd
}

func initUpgradeHelpLong() string {
	return appName + ` init upgrade plans and applies persistent project-local Tiny Orc setup upgrades.

Bare orc init upgrade is plan-only and writes nothing. Use --apply to write safe independent planned changes; conflicts block only their own path and dependent actions when the rest of the plan is safe. No separate dry-run flag exists for this command because the bare command is the dry-run behavior.

The upgrade scope includes schema migrations for .orc/config.yaml, eligible .orc/runtimes/**/*.yaml, .orc/workflows/**/*.yaml, and .orc/agents/**/*.md frontmatter, scaffold ownership metadata in .orc/scaffold.lock.yaml, scaffold refresh only when ownership is proven, .gitignore only for .orc/runs/, and managed Tiny Orc guidance in AGENTS.md. It never modifies .orc/runs/**. Existing runs keep pinned config snapshots; after applying live setup changes, run orc run refresh-config <run-id> for runs that should adopt the new setup.`
}

func executeInitUpgrade(opts initUpgradeOptions, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute init upgrade: %w", err)
	}

	plan, err := initupgrade.Plan(root)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s init upgrade: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute init upgrade: %w", writeErr)
		}

		return fmt.Errorf("execute init upgrade: %w", err)
	}

	if !opts.Apply {
		if opts.JSON {
			return encodeInitUpgradeJSON(stdout, initUpgradePlanJSON(plan))
		}

		return printInitUpgradePlan(stdout, plan)
	}

	applied, err := initupgrade.Apply(context.Background(), plan, initupgrade.ApplyOptions{Env: os.Environ()})
	if err != nil {
		var conflictErr *initupgrade.ConflictError
		if errors.As(err, &conflictErr) {
			return handleInitUpgradeApplyConflicts(opts, stdout, stderr, plan, applied, conflictErr.Conflicts())
		}

		if _, writeErr := fmt.Fprintf(stderr, "%s init upgrade: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute init upgrade: %w", writeErr)
		}

		return fmt.Errorf("execute init upgrade: %w", err)
	}

	if opts.JSON {
		return encodeInitUpgradeJSON(stdout, initUpgradeApplyJSON(plan, applied))
	}

	return printInitUpgradeApply(stdout, applied)
}

func refuseInitUpgradeApplyConflicts(opts initUpgradeOptions, stdout, stderr io.Writer, plan *initupgrade.Result, conflicts []initupgrade.Conflict) error {
	if opts.JSON {
		conflictsCopy := append([]initupgrade.Conflict(nil), conflicts...)
		payload := initUpgradePlanJSON(plan)

		payload.Refused = true
		payload.Status = initUpgradeStatusBlocked
		payload.Conflicts = conflictsCopy

		payload.ApplyRefusal = &initUpgradeApplyRefusalJSON{
			Reason: "apply detected conflicts; --apply did not write",
		}
		if err := encodeInitUpgradeJSON(stdout, payload); err != nil {
			return err
		}
	} else if err := printInitUpgradeApplyRefusal(stdout, plan, conflicts); err != nil {
		return err
	}

	if _, writeErr := fmt.Fprintf(stderr, "%s init upgrade: apply detected conflicts and did not write\n", appName); writeErr != nil {
		return fmt.Errorf("execute init upgrade: %w", writeErr)
	}

	return stableerr.New("init upgrade apply detected conflicts")
}

func handleInitUpgradeApplyConflicts(opts initUpgradeOptions, stdout, stderr io.Writer, plan *initupgrade.Result, applied *initupgrade.ApplyResult, conflicts []initupgrade.Conflict) error {
	if applied == nil {
		return refuseInitUpgradeApplyConflicts(opts, stdout, stderr, plan, conflicts)
	}

	if opts.JSON {
		payload := initUpgradeApplyJSON(plan, applied)
		payload.Refused = true
		payload.ApplyRefusal = &initUpgradeApplyRefusalJSON{
			Reason: "apply completed with unresolved conflicts",
		}

		if err := encodeInitUpgradeJSON(stdout, payload); err != nil {
			return err
		}
	} else if err := printInitUpgradeApply(stdout, applied); err != nil {
		return err
	}

	if _, writeErr := fmt.Fprintf(stderr, "%s init upgrade: applied safe changes but unresolved conflicts remain\n", appName); writeErr != nil {
		return fmt.Errorf("execute init upgrade: %w", writeErr)
	}

	return stableerr.New("init upgrade apply completed with unresolved conflicts")
}

func encodeInitUpgradeJSON(stdout io.Writer, payload initUpgradeJSON) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(payload); err != nil {
		return fmt.Errorf("encode init upgrade json: %w", err)
	}

	return nil
}

func initUpgradePlanJSON(plan *initupgrade.Result) initUpgradeJSON {
	skippedActions := initUpgradePlanSkippedActions(plan)

	actions := make([]initUpgradeActionJSON, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		actions = append(actions, initUpgradeActionJSON{
			Kind:      action.Kind,
			Path:      action.Path,
			Reason:    action.Reason,
			DependsOn: append([]string(nil), action.DependsOn...),
			Edits:     append([]initupgrade.SurgicalEdit(nil), action.Edits...),
		})
	}

	return initUpgradeJSON{
		ProjectRoot:         plan.ProjectRoot,
		Status:              initUpgradePlanStatus(plan),
		ConfigSchemaVersion: plan.ConfigSchemaVersion,
		CurrentSetupVersion: plan.CurrentSetupVersion,
		TargetSetupVersion:  plan.TargetSetupVersion,
		Actions:             actions,
		Warnings:            append([]initupgrade.Warning(nil), plan.Warnings...),
		Conflicts:           append([]initupgrade.Conflict(nil), plan.Conflicts...),
		SkippedActions:      skippedActions,
		StaleFiles:          append([]initupgrade.StaleFile(nil), plan.StaleFiles...),
		AffectedPaths:       append([]initupgrade.AffectedPath(nil), plan.AffectedPaths...),
		FollowUps:           append([]initupgrade.FollowUp(nil), plan.FollowUps...),
	}
}

func initUpgradeApplyJSON(plan *initupgrade.Result, applied *initupgrade.ApplyResult) initUpgradeJSON {
	payload := initUpgradePlanJSON(plan)
	payload.Applied = true
	payload.Status = initUpgradeApplyStatus(applied)

	payload.Warnings = append([]initupgrade.Warning(nil), applied.Warnings...)
	payload.Conflicts = append([]initupgrade.Conflict(nil), applied.Conflicts...)
	payload.SkippedActions = append([]initupgrade.SkippedAction(nil), applied.SkippedActions...)
	payload.StaleFiles = append([]initupgrade.StaleFile(nil), applied.StaleFiles...)
	payload.FollowUps = append([]initupgrade.FollowUp(nil), applied.FollowUps...)
	payload.CreatedPaths = append([]string(nil), applied.CreatedPaths...)
	payload.ModifiedPaths = append([]string(nil), applied.ModifiedPaths...)

	return payload
}

func printInitUpgradePlan(stdout io.Writer, plan *initupgrade.Result) error {
	if _, err := fmt.Fprintf(stdout, "orc init upgrade plan\n\nstatus: %s\nsetup version: %d -> %d\nconfig schema version: %d\n\n", initUpgradePlanStatus(plan), plan.CurrentSetupVersion, plan.TargetSetupVersion, plan.ConfigSchemaVersion); err != nil {
		return fmt.Errorf("print init upgrade plan: %w", err)
	}

	if _, err := fmt.Fprintln(stdout, "scope: persistent project-local Tiny Orc setup only"); err != nil {
		return fmt.Errorf("print init upgrade plan: %w", err)
	}

	if _, err := fmt.Fprintln(stdout, "includes: schema migrations for eligible .orc config/workflow/runtime/agent descriptor files, .orc/scaffold.lock.yaml ownership metadata, scaffold refresh when ownership is proven, .gitignore only for .orc/runs/, managed Tiny Orc guidance in AGENTS.md"); err != nil {
		return fmt.Errorf("print init upgrade plan: %w", err)
	}

	if _, err := fmt.Fprintln(stdout, "excludes: .orc/runs/** is never modified"); err != nil {
		return fmt.Errorf("print init upgrade plan: %w", err)
	}

	if _, err := fmt.Fprintln(stdout, "setup_version: missing setup_version is treated as legacy setup version 0; version remains the .orc/config.yaml schema version"); err != nil {
		return fmt.Errorf("print init upgrade plan: %w", err)
	}

	if err := printInitUpgradeActions(stdout, "planned changes", plan.Actions); err != nil {
		return err
	}

	if err := printInitUpgradeSkippedActions(stdout, initUpgradePlanSkippedActions(plan)); err != nil {
		return err
	}

	if err := printInitUpgradeWarnings(stdout, "warnings", plan.Warnings); err != nil {
		return err
	}

	if err := printInitUpgradeConflicts(stdout, plan.Conflicts); err != nil {
		return err
	}

	if err := printInitUpgradeStaleFiles(stdout, plan.StaleFiles); err != nil {
		return err
	}

	if err := printInitUpgradeFollowUps(stdout, plan.FollowUps); err != nil {
		return err
	}

	if len(plan.Conflicts) > 0 {
		if _, err := fmt.Fprintln(stdout, "\napply: run orc init upgrade --apply to write safe independent changes; conflicting paths and dependent actions will be skipped"); err != nil {
			return fmt.Errorf("print init upgrade plan: %w", err)
		}

		return nil
	}

	if len(plan.SkippedActions) > 0 {
		if _, err := fmt.Fprintln(stdout, "\napply: run orc init upgrade --apply to write safe planned changes; skipped manual-refresh items will be preserved"); err != nil {
			return fmt.Errorf("print init upgrade plan: %w", err)
		}

		return nil
	}

	if _, err := fmt.Fprintln(stdout, "\napply: run orc init upgrade --apply to write safe planned changes"); err != nil {
		return fmt.Errorf("print init upgrade plan: %w", err)
	}

	return nil
}

func printInitUpgradeApplyRefusal(stdout io.Writer, plan *initupgrade.Result, conflicts []initupgrade.Conflict) error {
	if _, err := fmt.Fprintf(stdout, "orc init upgrade apply refused\n\nstatus: blocked\nsetup version: %d -> %d\nconfig schema version: %d\n\n", plan.CurrentSetupVersion, plan.TargetSetupVersion, plan.ConfigSchemaVersion); err != nil {
		return fmt.Errorf("print init upgrade apply refusal: %w", err)
	}

	if err := printInitUpgradeActions(stdout, "planned changes", plan.Actions); err != nil {
		return err
	}

	if err := printInitUpgradeWarnings(stdout, "warnings", plan.Warnings); err != nil {
		return err
	}

	if err := printInitUpgradeSkippedActions(stdout, initUpgradePlanSkippedActions(plan)); err != nil {
		return err
	}

	if err := printInitUpgradeConflicts(stdout, conflicts); err != nil {
		return err
	}

	if err := printInitUpgradeStaleFiles(stdout, plan.StaleFiles); err != nil {
		return err
	}

	if err := printInitUpgradeFollowUps(stdout, plan.FollowUps); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(stdout, "\napply: --apply did not write because conflicts were detected during apply"); err != nil {
		return fmt.Errorf("print init upgrade apply refusal: %w", err)
	}

	return nil
}

func printInitUpgradeApply(stdout io.Writer, applied *initupgrade.ApplyResult) error {
	title := "orc init upgrade applied"
	if len(applied.Conflicts) > 0 || len(applied.SkippedActions) > 0 {
		title = initUpgradePartialTitle
	}

	if _, err := fmt.Fprintf(stdout, "%s\n\nstatus: %s\nsetup version: %d -> %d\nconfig schema version: %d\n", title, initUpgradeApplyStatus(applied), applied.PreviousSetupVersion, applied.TargetSetupVersion, applied.ConfigSchemaVersion); err != nil {
		return fmt.Errorf("print init upgrade apply: %w", err)
	}

	if err := printStringSection(stdout, "created files", applied.CreatedPaths); err != nil {
		return err
	}

	if err := printStringSection(stdout, "modified files", applied.ModifiedPaths); err != nil {
		return err
	}

	if err := printInitUpgradeSkippedActions(stdout, applied.SkippedActions); err != nil {
		return err
	}

	if err := printInitUpgradeConflicts(stdout, applied.Conflicts); err != nil {
		return err
	}

	if err := printInitUpgradeWarnings(stdout, "warnings", applied.Warnings); err != nil {
		return err
	}

	if err := printInitUpgradeStaleFiles(stdout, applied.StaleFiles); err != nil {
		return err
	}

	if err := printInitUpgradeFollowUps(stdout, applied.FollowUps); err != nil {
		return err
	}

	if len(applied.CreatedPaths) == 0 && len(applied.ModifiedPaths) == 0 {
		result := "result: no files changed"
		if len(applied.Conflicts) > 0 {
			result = "result: no files changed; unresolved conflicts remain"
		} else if len(applied.SkippedActions) > 0 {
			result = "result: no files changed; manual refresh items remain"
		}

		if _, err := fmt.Fprintln(stdout, "\n"+result); err != nil {
			return fmt.Errorf("print init upgrade apply: %w", err)
		}

		return nil
	}

	result := "result: safe planned changes were written"
	if len(applied.Conflicts) > 0 {
		result = initUpgradePartialResult
	} else if len(applied.SkippedActions) > 0 {
		result = "result: safe planned changes were written; manual refresh items remain"
	}

	if _, err := fmt.Fprintln(stdout, "\n"+result); err != nil {
		return fmt.Errorf("print init upgrade apply: %w", err)
	}

	return nil
}

func initUpgradePlanStatus(plan *initupgrade.Result) string {
	skippedActions := initUpgradePlanSkippedActions(plan)
	if len(plan.Conflicts) > 0 {
		if len(plan.Actions) > 0 || len(skippedActions) > 0 {
			return initUpgradeStatusPartial
		}

		return initUpgradeStatusBlocked
	}

	if len(skippedActions) > 0 {
		return initUpgradeStatusPartial
	}

	if len(plan.Actions) == 0 && len(plan.StaleFiles) == 0 && len(plan.FollowUps) == 0 {
		return initUpgradeStatusCurrent
	}

	return initUpgradeStatusPlanned
}

func initUpgradePlanSkippedActions(plan *initupgrade.Result) []initupgrade.SkippedAction {
	return initupgrade.PlanSkippedActions(plan)
}

func initUpgradeApplyStatus(applied *initupgrade.ApplyResult) string {
	if applied == nil {
		return initUpgradeStatusFailed
	}

	hasWrites := len(applied.CreatedPaths) > 0 || len(applied.ModifiedPaths) > 0
	if len(applied.Conflicts) > 0 {
		if hasWrites || len(applied.SkippedActions) > 0 {
			return initUpgradeStatusPartial
		}

		return initUpgradeStatusBlocked
	}

	if len(applied.SkippedActions) > 0 {
		return initUpgradeStatusPartial
	}

	if !hasWrites {
		return initUpgradeStatusCurrent
	}

	return initUpgradeStatusApplied
}

func printInitUpgradeActions(stdout io.Writer, title string, actions []initupgrade.Action) error {
	if _, err := fmt.Fprintf(stdout, "\n%s:\n", title); err != nil {
		return fmt.Errorf("print init upgrade actions: %w", err)
	}

	if len(actions) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print init upgrade actions: %w", err)
		}

		return nil
	}

	for _, action := range actions {
		if _, err := fmt.Fprintf(stdout, "  - %s %s: %s\n", action.Kind, action.Path, action.Reason); err != nil {
			return fmt.Errorf("print init upgrade actions: %w", err)
		}

		for _, edit := range action.Edits {
			if _, err := fmt.Fprintf(stdout, "    edit: %s %s%s\n", edit.Kind, edit.Path, initUpgradeEditSuffix(edit)); err != nil {
				return fmt.Errorf("print init upgrade actions: %w", err)
			}
		}
	}

	return nil
}

func initUpgradeEditSuffix(edit initupgrade.SurgicalEdit) string {
	switch {
	case edit.Key != "":
		return " " + edit.Key
	case strings.TrimSpace(edit.Value) != "" && edit.Path.Empty():
		return " " + strings.TrimSpace(edit.Value)
	default:
		return ""
	}
}

func printInitUpgradeWarnings(stdout io.Writer, title string, warnings []initupgrade.Warning) error {
	if _, err := fmt.Fprintf(stdout, "\n%s:\n", title); err != nil {
		return fmt.Errorf("print init upgrade warnings: %w", err)
	}

	if len(warnings) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print init upgrade warnings: %w", err)
		}

		return nil
	}

	for _, warning := range warnings {
		path := warning.Path
		if path == "" {
			path = "project"
		}

		if _, err := fmt.Fprintf(stdout, "  - %s [%s]: %s\n", path, warning.Code, warning.Message); err != nil {
			return fmt.Errorf("print init upgrade warnings: %w", err)
		}

		if warning.Guidance != "" {
			if _, err := fmt.Fprintf(stdout, "    guidance: %s\n", warning.Guidance); err != nil {
				return fmt.Errorf("print init upgrade warnings: %w", err)
			}
		}
	}

	return nil
}

func printInitUpgradeSkippedActions(stdout io.Writer, skipped []initupgrade.SkippedAction) error {
	if _, err := fmt.Fprintln(stdout, "\nskipped actions:"); err != nil {
		return fmt.Errorf("print init upgrade skipped actions: %w", err)
	}

	if len(skipped) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print init upgrade skipped actions: %w", err)
		}

		return nil
	}

	for _, item := range skipped {
		if _, err := fmt.Fprintf(stdout, "  - %s [%s]: %s\n    guidance: %s\n", item.Path, item.Code, item.Message, item.Guidance); err != nil {
			return fmt.Errorf("print init upgrade skipped actions: %w", err)
		}

		if item.ActionKind != "" {
			if _, err := fmt.Fprintf(stdout, "    action: %s\n", item.ActionKind); err != nil {
				return fmt.Errorf("print init upgrade skipped actions: %w", err)
			}
		}

		if len(item.DependsOn) > 0 {
			if _, err := fmt.Fprintf(stdout, "    depends_on: %s\n", strings.Join(item.DependsOn, ", ")); err != nil {
				return fmt.Errorf("print init upgrade skipped actions: %w", err)
			}
		}
	}

	return nil
}

func printInitUpgradeConflicts(stdout io.Writer, conflicts []initupgrade.Conflict) error {
	if _, err := fmt.Fprintln(stdout, "\nconflicts:"); err != nil {
		return fmt.Errorf("print init upgrade conflicts: %w", err)
	}

	if len(conflicts) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print init upgrade conflicts: %w", err)
		}

		return nil
	}

	for _, conflict := range conflicts {
		if _, err := fmt.Fprintf(stdout, "  - %s [%s]: %s\n    guidance: %s\n", conflict.Path, conflict.Code, conflict.Message, conflict.Guidance); err != nil {
			return fmt.Errorf("print init upgrade conflicts: %w", err)
		}
	}

	return nil
}

func printInitUpgradeStaleFiles(stdout io.Writer, stale []initupgrade.StaleFile) error {
	if _, err := fmt.Fprintln(stdout, "\nstale managed files:"); err != nil {
		return fmt.Errorf("print init upgrade stale files: %w", err)
	}

	if len(stale) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print init upgrade stale files: %w", err)
		}

		return nil
	}

	for _, file := range stale {
		if _, err := fmt.Fprintf(stdout, "  - %s: %s\n    guidance: %s\n", file.Path, file.Reason, file.Guidance); err != nil {
			return fmt.Errorf("print init upgrade stale files: %w", err)
		}
	}

	return nil
}

func printInitUpgradeFollowUps(stdout io.Writer, followUps []initupgrade.FollowUp) error {
	if _, err := fmt.Fprintln(stdout, "\nfollow-up guidance:"); err != nil {
		return fmt.Errorf("print init upgrade follow ups: %w", err)
	}

	if len(followUps) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print init upgrade follow ups: %w", err)
		}

		return nil
	}

	for _, followUp := range followUps {
		if _, err := fmt.Fprintf(stdout, "  - [%s] %s\n", followUp.Code, followUp.Message); err != nil {
			return fmt.Errorf("print init upgrade follow ups: %w", err)
		}
	}

	return nil
}

func printStringSection(stdout io.Writer, title string, values []string) error {
	if _, err := fmt.Fprintf(stdout, "\n%s:\n", title); err != nil {
		return fmt.Errorf("print string section: %w", err)
	}

	if len(values) == 0 {
		if _, err := fmt.Fprintln(stdout, "  none"); err != nil {
			return fmt.Errorf("print string section: %w", err)
		}

		return nil
	}

	for _, value := range values {
		if _, err := fmt.Fprintf(stdout, "  - %s\n", value); err != nil {
			return fmt.Errorf("print string section: %w", err)
		}
	}

	return nil
}

func initUpgradeFlagError(cmd *cobra.Command, stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s init upgrade: %v\n\n", appName, err); writeErr != nil {
		return fmt.Errorf("init upgrade flag error: %w", writeErr)
	}

	cmd.SetOut(stderr)

	if usageErr := cmd.Usage(); usageErr != nil {
		return fmt.Errorf("init upgrade flag error: %w", usageErr)
	}

	return fmt.Errorf("%s init upgrade: %w", appName, err)
}
