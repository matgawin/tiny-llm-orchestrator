package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-yaml"
)

const (
	configDirName           = ".orc"
	configFileName          = "config.yaml"
	schemaVersion           = 1
	executionModeSequential = "sequential"

	StepKindAgent   = "agent"
	StepKindCommand = "command"
	StepKindScript  = "script"

	taskContextBeadsDisabled = "disabled"
	taskContextBeadsOptional = "optional"
	taskContextBeadsRequired = "required"

	VCSDirtyStartBlock = "block"
	VCSDirtyStartAllow = "allow"
	VCSNoVCSAllow      = "allow"
	VCSNoVCSBlock      = "block"
)

var (
	allowedReportStatuses = map[string]struct{}{
		"done":    {},
		"blocked": {},
		"failed":  {},
	}
	allowedTerminalStates = map[string]struct{}{
		"ready_for_human":   {},
		"blocked_for_human": {},
		"cancelled":         {},
	}
	allowedTaskContextBeads = map[string]struct{}{
		taskContextBeadsDisabled: {},
		taskContextBeadsOptional: {},
		taskContextBeadsRequired: {},
	}
	allowedDirtyStartPolicies = map[string]struct{}{
		VCSDirtyStartBlock: {},
		VCSDirtyStartAllow: {},
	}
	allowedNoVCSPolicies = map[string]struct{}{
		VCSNoVCSAllow: {},
		VCSNoVCSBlock: {},
	}
)

type resultPairSet map[string]struct{}

// Project is the validated project-local orchestration configuration.
type Project struct {
	Root       string
	OrcDir     string
	RealOrcDir string
	Config     ProjectConfig
	Workflows  map[string]Workflow
	Agents     map[string]Agent
}

// ProjectConfig is the schema stored in .orc/config.yaml.
type ProjectConfig struct {
	Version   int                          `yaml:"version"`
	Defaults  ProjectDefaults              `yaml:"defaults"`
	Workflows map[string]WorkflowReference `yaml:"workflows"`
	Agents    map[string]string            `yaml:"agents"`
	Sandbox   *SandboxConfig               `yaml:"sandbox"`
}

// ProjectDefaults contains project-wide config defaults.
type ProjectDefaults struct {
	LoopCaps LoopCapsConfig `yaml:"loop_caps"`
}

// WorkflowReference points to a workflow file and optional per-workflow config.
type WorkflowReference struct {
	Path     string         `yaml:"path"`
	LoopCaps LoopCapsConfig `yaml:"loop_caps"`
}

// UnmarshalYAML accepts both the legacy scalar workflow path and the expanded
// object form used for workflow-level overrides.
func (r *WorkflowReference) UnmarshalYAML(data []byte) error {
	var path string
	if err := yaml.Unmarshal(data, &path); err == nil {
		r.Path = path
		return nil
	}
	type workflowReference WorkflowReference
	var expanded workflowReference
	if err := yaml.Unmarshal(data, &expanded); err != nil {
		return err
	}
	*r = WorkflowReference(expanded)
	return nil
}

// LoopCapsConfig stores optional loop-cap fields from project config.
type LoopCapsConfig struct {
	Enabled RequiredBool `yaml:"enabled"`
	Soft    OptionalInt  `yaml:"soft"`
	Hard    OptionalInt  `yaml:"hard"`
}

// SandboxConfig stores the durable Orc-managed sandbox configuration contract.
// Process execution and bubblewrap argv construction are owned by
// internal/sandbox.
type SandboxConfig struct {
	Command    SandboxCommand   `yaml:"command"`
	CWD        string           `yaml:"cwd"`
	Bubblewrap BubblewrapConfig `yaml:"bubblewrap"`
	Env        SandboxEnvConfig `yaml:"env"`
	Mounts     []SandboxMount   `yaml:"mounts"`
}

// SandboxCommand declares the argv-only command launched by orc sandbox run.
type SandboxCommand struct {
	Argv []string `yaml:"argv"`
}

// UnmarshalYAML rejects shell-string sandbox commands in favor of argv-only
// command declarations.
func (c *SandboxCommand) UnmarshalYAML(data []byte) error {
	var shellCommand string
	if err := yaml.Unmarshal(data, &shellCommand); err == nil {
		return errors.New("sandbox.command must use argv; shell-string commands are not supported")
	}
	type sandboxCommand SandboxCommand
	var decoded sandboxCommand
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = SandboxCommand(decoded)
	return nil
}

// BubblewrapConfig stores bubblewrap options used by sandbox execution.
type BubblewrapConfig struct {
	Enabled bool                  `yaml:"enabled"`
	Network RequiredBool          `yaml:"network"`
	Mounts  BubblewrapMountConfig `yaml:"mounts"`
}

// BubblewrapMountConfig stores named preset mount policies.
type BubblewrapMountConfig struct {
	Repo      string `yaml:"repo"`
	Beads     string `yaml:"beads"`
	CodexHome string `yaml:"codex_home"`
	Tmp       string `yaml:"tmp"`
}

// SandboxEnvConfig declares explicit environment passthrough and override
// policy. It does not imply whole-host environment passthrough.
type SandboxEnvConfig struct {
	Pass []string          `yaml:"pass"`
	Set  map[string]string `yaml:"set"`
}

// SandboxMount declares an extra host mount for sandbox execution.
type SandboxMount struct {
	Host     string       `yaml:"host"`
	Target   string       `yaml:"target"`
	Mode     string       `yaml:"mode"`
	Optional RequiredBool `yaml:"optional"`
}

// EffectiveLoopCaps is the resolved workflow loop-cap policy.
type EffectiveLoopCaps struct {
	Enabled bool
	Soft    int
	Hard    int
}

// Workflow is a validated workflow definition.
type Workflow struct {
	Name             string              `yaml:"name"`
	Start            string              `yaml:"start"`
	Execution        Execution           `yaml:"execution"`
	TaskContext      TaskContext         `yaml:"task_context"`
	VCS              VCSPolicy           `yaml:"vcs"`
	Defaults         Defaults            `yaml:"defaults"`
	LoopCaps         EffectiveLoopCaps   `yaml:"-"`
	Steps            map[string]Step     `yaml:"steps"`
	StepOrder        []string            `yaml:"-"`
	SourcePath       string              `yaml:"-"`
	ReferencedAgents map[string]AgentRef `yaml:"-"`
}

// Execution declares workflow execution semantics.
type Execution struct {
	Mode string `yaml:"mode"`
}

// TaskContext declares accepted task context sources.
type TaskContext struct {
	Beads            string       `yaml:"beads"`
	MarkdownFallback RequiredBool `yaml:"markdown_fallback"`
}

// VCSPolicy declares workflow-level repository cleanliness policy.
type VCSPolicy struct {
	DirtyStart string `yaml:"dirty_start"`
	NoVCS      string `yaml:"no_vcs"`
}

// EffectiveDirtyStart returns the configured dirty-start policy, defaulting to block.
func (p VCSPolicy) EffectiveDirtyStart() string {
	if p.DirtyStart == "" {
		return VCSDirtyStartBlock
	}
	return p.DirtyStart
}

// EffectiveNoVCS returns the configured no-VCS policy, defaulting to allow.
func (p VCSPolicy) EffectiveNoVCS() string {
	if p.NoVCS == "" {
		return VCSNoVCSAllow
	}
	return p.NoVCS
}

// Defaults contains workflow-wide policy defaults.
type Defaults struct {
	Timeout         Duration       `yaml:"timeout"`
	ReportExitGrace Duration       `yaml:"report_exit_grace"`
	Retries         map[string]int `yaml:"retries"`
}

// Duration wraps time.Duration for YAML values such as "30m".
type Duration struct {
	time.Duration
	Set bool
}

// UnmarshalYAML parses Go duration strings from YAML scalars.
func (d *Duration) UnmarshalYAML(data []byte) error {
	d.Set = true
	var raw string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML emits the public duration scalar instead of internal presence
// tracking fields.
func (d Duration) MarshalYAML() (any, error) {
	if !d.Set {
		return nil, nil
	}
	return d.String(), nil
}

// RequiredBool tracks whether a YAML boolean field was explicitly present.
type RequiredBool struct {
	Value bool
	Set   bool
}

// UnmarshalYAML parses a YAML boolean and records field presence.
func (b *RequiredBool) UnmarshalYAML(data []byte) error {
	b.Set = true
	return yaml.Unmarshal(data, &b.Value)
}

// MarshalYAML emits the public boolean scalar instead of internal presence
// tracking fields.
func (b RequiredBool) MarshalYAML() (any, error) {
	if !b.Set {
		return nil, nil
	}
	return b.Value, nil
}

// OptionalInt tracks whether a YAML integer field was explicitly present.
type OptionalInt struct {
	Value int
	Set   bool
}

// UnmarshalYAML parses an integer and records field presence.
func (i *OptionalInt) UnmarshalYAML(data []byte) error {
	i.Set = true
	return yaml.Unmarshal(data, &i.Value)
}

// MarshalYAML emits the public integer scalar instead of internal presence
// tracking fields.
func (i OptionalInt) MarshalYAML() (any, error) {
	if !i.Set {
		return nil, nil
	}
	return i.Value, nil
}

// Step is a named workflow step after validation.
type Step struct {
	Kind           string              `yaml:"kind"`
	Agent          string              `yaml:"agent"`
	Command        CommandStep         `yaml:"command"`
	Script         ScriptStep          `yaml:"script"`
	CWD            string              `yaml:"cwd"`
	Env            map[string]string   `yaml:"env"`
	AllowedResults map[string][]string `yaml:"allowed_results"`
	On             map[string]string   `yaml:"on"`
}

// EffectiveKind returns the backward-compatible v1 step kind.
func (s Step) EffectiveKind() string {
	if s.Kind == "" {
		return StepKindAgent
	}
	return s.Kind
}

// EffectiveAgentID returns the attempt actor id persisted for the step.
func (s Step) EffectiveAgentID() string {
	if s.Agent != "" {
		return s.Agent
	}
	return s.EffectiveKind()
}

// CommandStep declares an argv-only deterministic command step.
type CommandStep struct {
	Argv []string `yaml:"argv"`
}

// ScriptStep declares a deterministic repo-relative executable script step.
type ScriptStep struct {
	Path string   `yaml:"path"`
	Args []string `yaml:"args"`
	Body string   `yaml:"body"`
}

// AgentRef records a project-local agent reference used by a workflow.
type AgentRef struct {
	ID   string
	Path string
}

// Agent is a validated project-local role descriptor.
type Agent struct {
	ID          string
	Role        string
	Description string
	Body        string
	SourcePath  string
}

type agentFrontmatter struct {
	ID          string `yaml:"id"`
	Role        string `yaml:"role"`
	Description string `yaml:"description"`
}
