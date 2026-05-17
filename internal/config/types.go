package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"

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

	SystemSkipStatus = "done"
	SystemSkipResult = "skipped"
	SystemSkipPair   = SystemSkipStatus + "/" + SystemSkipResult
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
	Root       string              `json:"Root"`
	OrcDir     string              `json:"OrcDir"`
	RealOrcDir string              `json:"RealOrcDir"`
	Config     ProjectConfig       `json:"Config"`
	Workflows  map[string]Workflow `json:"Workflows"`
	Agents     map[string]Agent    `json:"Agents"`
	Runtimes   map[string]Runtime  `json:"Runtimes"`
}

// ProjectConfig is the schema stored in .orc/config.yaml.
type ProjectConfig struct {
	Version   int                          `yaml:"version" json:"Version"`
	Defaults  ProjectDefaults              `yaml:"defaults" json:"Defaults"`
	Workflows map[string]WorkflowReference `yaml:"workflows" json:"Workflows"`
	Agents    map[string]string            `yaml:"agents" json:"Agents"`
	Runtimes  map[string]string            `yaml:"runtimes" json:"Runtimes"`
	Sandbox   *SandboxConfig               `yaml:"sandbox" json:"Sandbox"`
}

// ProjectDefaults contains project-wide config defaults.
type ProjectDefaults struct {
	LoopCaps LoopCapsConfig `yaml:"loop_caps" json:"LoopCaps"`
}

// WorkflowReference points to a workflow file and optional per-workflow config.
type WorkflowReference struct {
	Path     string         `yaml:"path" json:"Path"`
	LoopCaps LoopCapsConfig `yaml:"loop_caps" json:"LoopCaps"`
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
		return fmt.Errorf("unmarshal workflow reference YAML: %w", err)
	}

	*r = WorkflowReference(expanded)

	return nil
}

// LoopCapsConfig stores optional loop-cap fields from project config.
type LoopCapsConfig struct {
	Enabled RequiredBool `yaml:"enabled" json:"Enabled"`
	Soft    OptionalInt  `yaml:"soft" json:"Soft"`
	Hard    OptionalInt  `yaml:"hard" json:"Hard"`
}

// SandboxConfig stores the durable Orc-managed sandbox configuration contract.
// Process execution and bubblewrap argv construction are owned by
// internal/sandbox.
type SandboxConfig struct {
	Command           SandboxCommand         `yaml:"command" json:"Command"`
	CWD               string                 `yaml:"cwd" json:"CWD"`
	RequireForWorkers bool                   `yaml:"require_for_workers" json:"RequireForWorkers"`
	Home              SandboxHomeConfig      `yaml:"home" json:"Home"`
	Path              SandboxPathConfig      `yaml:"path" json:"Path"`
	ProtectedPaths    []SandboxProtectedPath `yaml:"protected_paths" json:"ProtectedPaths"`
	Bubblewrap        BubblewrapConfig       `yaml:"bubblewrap" json:"Bubblewrap"`
	Env               SandboxEnvConfig       `yaml:"env" json:"Env"`
	Mounts            []SandboxMount         `yaml:"mounts" json:"Mounts"`
}

// SandboxHomeConfig stores the sandbox HOME path policy.
type SandboxHomeConfig struct {
	Mode string `yaml:"mode" json:"Mode"`
}

// SandboxPathConfig stores the sandbox PATH mount policy.
type SandboxPathConfig struct {
	Mode string `yaml:"mode" json:"Mode"`
}

// SandboxProtectedPath stores one static protected host-path declaration. The
// host_home and absolute values are syntactic config only; host-dependent
// resolution belongs to internal/sandbox.
type SandboxProtectedPath struct { //nolint:recvcheck // YAML requires pointer unmarshal state tracking and value marshal support for slice elements.
	HostHome    string `yaml:"host_home" json:"HostHome"`
	Absolute    string `yaml:"absolute" json:"Absolute"`
	HostHomeSet bool   `yaml:"-" json:"HostHomeSet"`
	AbsoluteSet bool   `yaml:"-" json:"AbsoluteSet"`

	decodeError string
	unknownKeys []string
}

// UnmarshalYAML records key presence so validation can reject empty values and
// entries that set neither or both supported forms.
func (p *SandboxProtectedPath) UnmarshalYAML(data []byte) error {
	*p = SandboxProtectedPath{}

	var scalar string
	if err := yaml.Unmarshal(data, &scalar); err == nil {
		p.decodeError = "must be an object with exactly one of host_home or absolute"
		return nil
	}

	var raw yaml.MapSlice
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal sandbox protected path YAML: %w", err)
	}

	for _, item := range raw {
		key, ok := item.Key.(string)
		if !ok {
			p.decodeError = "must use string keys"
			continue
		}

		switch key {
		case "host_home":
			p.HostHomeSet = true

			value, ok := item.Value.(string)
			if !ok {
				p.decodeError = "host_home must be a string"
				continue
			}

			p.HostHome = value
		case "absolute":
			p.AbsoluteSet = true

			value, ok := item.Value.(string)
			if !ok {
				p.decodeError = "absolute must be a string"
				continue
			}

			p.Absolute = value
		default:
			p.unknownKeys = append(p.unknownKeys, key)
		}
	}

	slices.Sort(p.unknownKeys)

	return nil
}

// MarshalYAML emits only the public schema fields.
func (p SandboxProtectedPath) MarshalYAML() (any, error) {
	out := map[string]string{}
	if p.HostHomeSet {
		out["host_home"] = p.HostHome
	}

	if p.AbsoluteSet {
		out["absolute"] = p.Absolute
	}

	if len(out) == 0 {
		return nil, nil //nolint:nilnil // nil tells the YAML marshaler to omit absent protected path forms.
	}

	return out, nil
}

func (p SandboxProtectedPath) hasUnknownKeys() bool {
	return len(p.unknownKeys) > 0
}

func (p SandboxProtectedPath) unknownKeyList() string {
	return strings.Join(p.unknownKeys, ", ")
}

// SandboxCommand declares the argv-only command launched by orc sandbox run.
type SandboxCommand struct {
	Argv []string `yaml:"argv" json:"Argv"`
}

// UnmarshalYAML rejects shell-string sandbox commands in favor of argv-only
// command declarations.
func (c *SandboxCommand) UnmarshalYAML(data []byte) error {
	var shellCommand string
	if err := yaml.Unmarshal(data, &shellCommand); err == nil {
		return stableerr.New("sandbox.command must use argv; shell-string commands are not supported")
	}

	type sandboxCommand SandboxCommand

	var decoded sandboxCommand
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("unmarshal sandbox command YAML: %w", err)
	}

	*c = SandboxCommand(decoded)

	return nil
}

// BubblewrapConfig stores bubblewrap options used by sandbox execution.
type BubblewrapConfig struct {
	Enabled bool                  `yaml:"enabled" json:"Enabled"`
	Network RequiredBool          `yaml:"network" json:"Network"`
	Mounts  BubblewrapMountConfig `yaml:"mounts" json:"Mounts"`
}

// BubblewrapMountConfig stores named preset mount policies.
type BubblewrapMountConfig struct {
	Repo  string `yaml:"repo" json:"Repo"`
	Beads string `yaml:"beads" json:"Beads"`
	Tmp   string `yaml:"tmp" json:"Tmp"`
}

// SandboxEnvConfig declares explicit environment passthrough and override
// policy. It does not imply whole-host environment passthrough.
type SandboxEnvConfig struct {
	Pass []string          `yaml:"pass" json:"Pass"`
	Set  map[string]string `yaml:"set" json:"Set"`
}

// SandboxMount declares an extra host mount for sandbox execution.
type SandboxMount struct {
	Host     string       `yaml:"host" json:"Host"`
	Target   string       `yaml:"target" json:"Target"`
	Mode     string       `yaml:"mode" json:"Mode"`
	Optional RequiredBool `yaml:"optional" json:"Optional"`
}

// Runtime is a validated project-local executable runtime descriptor.
type Runtime struct {
	ID          string             `json:"ID"`
	Command     RuntimeCommand     `json:"Command"`
	Prompt      RuntimePrompt      `json:"Prompt"`
	Model       RuntimeModel       `json:"Model"`
	Reasoning   RuntimeReasoning   `json:"Reasoning"`
	Directories RuntimeDirectories `json:"Directories"`
	Sandbox     RuntimeSandbox     `json:"Sandbox"`
	SourcePath  string             `json:"SourcePath"`
}

// RuntimeCommand declares the base argv fragments for a runtime.
type RuntimeCommand struct {
	Executable  string   `yaml:"executable" json:"Executable"`
	Args        []string `yaml:"args" json:"Args"`
	NormalArgs  []string `yaml:"normal_args" json:"NormalArgs"`
	SandboxArgs []string `yaml:"sandbox_args" json:"SandboxArgs"`
}

// RuntimePrompt declares how a runtime receives rendered worker prompts.
type RuntimePrompt struct {
	Delivery string `yaml:"delivery" json:"Delivery"`
}

// RuntimeModel declares model-selection capability and argv behavior.
type RuntimeModel struct {
	Supported bool     `yaml:"supported" json:"Supported"`
	Required  bool     `yaml:"required" json:"Required"`
	Default   string   `yaml:"default" json:"Default"`
	Allowed   []string `yaml:"allowed" json:"Allowed"`
	Args      []string `yaml:"args" json:"Args"`
}

// RuntimeReasoning declares reasoning-selection capability and argv behavior.
type RuntimeReasoning struct {
	Supported bool     `yaml:"supported" json:"Supported"`
	Required  bool     `yaml:"required" json:"Required"`
	Default   string   `yaml:"default" json:"Default"`
	Allowed   []string `yaml:"allowed" json:"Allowed"`
	Args      []string `yaml:"args" json:"Args"`
}

// RuntimeDirectories declares runtime directory capability and argv behavior.
type RuntimeDirectories struct {
	Supported bool     `yaml:"supported" json:"Supported"`
	Args      []string `yaml:"args" json:"Args"`
}

// RuntimeSandbox declares runtime sandbox compatibility and static requirements.
type RuntimeSandbox struct {
	Supported    bool                       `yaml:"supported" json:"Supported"`
	Required     bool                       `yaml:"required" json:"Required"`
	Requirements RuntimeSandboxRequirements `yaml:"requirements" json:"Requirements"`
}

// RuntimeSandboxRequirements declares runtime-owned sandbox inputs.
type RuntimeSandboxRequirements struct {
	Env    RuntimeSandboxEnvConfig `yaml:"env" json:"Env"`
	Mounts []RuntimeSandboxMount   `yaml:"mounts" json:"Mounts"`
}

// RuntimeSandboxEnvConfig declares runtime-owned sandbox environment inputs.
type RuntimeSandboxEnvConfig struct {
	Pass         []string                          `yaml:"pass" json:"Pass"`
	Set          map[string]string                 `yaml:"set" json:"Set"`
	SetFromMount map[string]RuntimeEnvFromMountRef `yaml:"set_from_mount" json:"SetFromMount"`
}

// RuntimeEnvFromMountRef declares a sandbox env value derived from a resolved
// runtime sandbox mount.
type RuntimeEnvFromMountRef struct {
	Mount string `yaml:"mount" json:"Mount"`
	Value string `yaml:"value" json:"Value"`
}

// RuntimeSandboxMount declares a runtime-owned sandbox mount. It supports the
// legacy simple host/target shape and the extended env-sourced shape.
type RuntimeSandboxMount struct {
	ID       string                    `yaml:"id" json:"ID"`
	Host     string                    `yaml:"host" json:"Host"`
	Source   RuntimeSandboxMountSource `yaml:"source" json:"Source"`
	Target   RuntimeSandboxMountTarget `yaml:"target" json:"Target"`
	Mode     string                    `yaml:"mode" json:"Mode"`
	Optional RequiredBool              `yaml:"optional" json:"Optional"`
}

// RuntimeSandboxMountSource declares how an extended runtime mount source is
// resolved from host state.
type RuntimeSandboxMountSource struct {
	Env      string                            `yaml:"env" json:"Env"`
	Fallback RuntimeSandboxMountSourceFallback `yaml:"fallback" json:"Fallback"`
	Create   bool                              `yaml:"create" json:"Create"`
}

// RuntimeSandboxMountSourceFallback declares source fallback strategies.
type RuntimeSandboxMountSourceFallback struct {
	HostHome string `yaml:"host_home" json:"HostHome"`
}

// RuntimeSandboxMountTarget declares where an extended runtime mount appears in
// the sandbox. Path is populated for the legacy scalar target form.
type RuntimeSandboxMountTarget struct {
	Path            string                            `yaml:"-" json:"Path"`
	EnvSameAsSource bool                              `yaml:"env_same_as_source" json:"EnvSameAsSource"`
	Fallback        RuntimeSandboxMountTargetFallback `yaml:"fallback" json:"Fallback"`
}

// UnmarshalYAML accepts both the legacy scalar target and the extended mapping
// target forms for runtime sandbox mounts.
func (t *RuntimeSandboxMountTarget) UnmarshalYAML(data []byte) error {
	var path string
	if err := yaml.Unmarshal(data, &path); err == nil {
		t.Path = path
		return nil
	}

	type target RuntimeSandboxMountTarget

	var decoded target
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("unmarshal runtime sandbox mount target YAML: %w", err)
	}

	*t = RuntimeSandboxMountTarget(decoded)

	return nil
}

// RuntimeSandboxMountTargetFallback declares target fallback strategies.
type RuntimeSandboxMountTargetFallback struct {
	SandboxHome string `yaml:"sandbox_home" json:"SandboxHome"`
}

// EffectiveLoopCaps is the resolved workflow loop-cap policy.
type EffectiveLoopCaps struct {
	Enabled bool `json:"Enabled"`
	Soft    int  `json:"Soft"`
	Hard    int  `json:"Hard"`
}

// Workflow is a validated workflow definition.
type Workflow struct {
	Name             string              `yaml:"name" json:"Name"`
	Start            string              `yaml:"start" json:"Start"`
	Execution        Execution           `yaml:"execution" json:"Execution"`
	TaskContext      TaskContext         `yaml:"task_context" json:"TaskContext"`
	VCS              VCSPolicy           `yaml:"vcs" json:"VCS"`
	Defaults         Defaults            `yaml:"defaults" json:"Defaults"`
	LoopCaps         EffectiveLoopCaps   `yaml:"-" json:"LoopCaps"`
	Steps            map[string]Step     `yaml:"steps" json:"Steps"`
	StepOrder        []string            `yaml:"-" json:"StepOrder"`
	SourcePath       string              `yaml:"-" json:"SourcePath"`
	ReferencedAgents map[string]AgentRef `yaml:"-" json:"ReferencedAgents"`
}

// Execution declares workflow execution semantics.
type Execution struct {
	Mode string `yaml:"mode" json:"Mode"`
}

// TaskContext declares accepted task context sources.
type TaskContext struct {
	Beads            string       `yaml:"beads" json:"Beads"`
	MarkdownFallback RequiredBool `yaml:"markdown_fallback" json:"MarkdownFallback"`
}

// VCSPolicy declares workflow-level repository cleanliness policy.
type VCSPolicy struct {
	DirtyStart string `yaml:"dirty_start" json:"DirtyStart"`
	NoVCS      string `yaml:"no_vcs" json:"NoVCS"`
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
	Timeout         Duration       `yaml:"timeout" json:"Timeout"`
	ReportExitGrace Duration       `yaml:"report_exit_grace" json:"ReportExitGrace"`
	Retries         map[string]int `yaml:"retries" json:"Retries"`
	Runtime         string         `yaml:"runtime" json:"Runtime"`
	Model           string         `yaml:"model" json:"Model"`
	Reasoning       string         `yaml:"reasoning" json:"Reasoning"`
	RuntimeDirs     []string       `yaml:"runtime_dirs" json:"RuntimeDirs"`
}

// EffectiveRuntime returns the runtime selected for an agent step.
func (w Workflow) EffectiveRuntime(step Step) string {
	if step.Runtime != "" {
		return step.Runtime
	}

	return w.Defaults.Runtime
}

// EffectiveModel returns the model selected for an agent step and runtime.
func (w Workflow) EffectiveModel(step Step, runtime Runtime) string {
	if step.Model != "" {
		return step.Model
	}

	if w.Defaults.Model != "" {
		return w.Defaults.Model
	}

	return runtime.Model.Default
}

// EffectiveReasoning returns the reasoning value selected for an agent step and runtime.
func (w Workflow) EffectiveReasoning(step Step, runtime Runtime) string {
	if step.Reasoning != "" {
		return step.Reasoning
	}

	if w.Defaults.Reasoning != "" {
		return w.Defaults.Reasoning
	}

	return runtime.Reasoning.Default
}

// EffectiveRuntimeDirs returns workflow default runtime directories followed
// by step runtime directories, preserving configured order.
func (w Workflow) EffectiveRuntimeDirs(step Step) []string {
	dirs := make([]string, 0, len(w.Defaults.RuntimeDirs)+len(step.RuntimeDirs))
	dirs = append(dirs, w.Defaults.RuntimeDirs...)
	dirs = append(dirs, step.RuntimeDirs...)

	return dirs
}

// Duration wraps time.Duration for YAML values such as "30m".
type Duration struct {
	time.Duration `json:"Duration"`
	Set           bool `json:"Set"`
}

// UnmarshalYAML parses Go duration strings from YAML scalars.
func (d *Duration) UnmarshalYAML(data []byte) error {
	d.Set = true

	var raw string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal duration YAML: %w", err)
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
		return nil, nil //nolint:nilnil // nil tells the YAML marshaler to omit unset optional durations.
	}

	return d.String(), nil
}

// RequiredBool tracks whether a YAML boolean field was explicitly present.
type RequiredBool struct {
	Value bool `json:"Value"`
	Set   bool `json:"Set"`
}

// UnmarshalYAML parses a YAML boolean and records field presence.
func (b *RequiredBool) UnmarshalYAML(data []byte) error {
	b.Set = true
	if err := yaml.Unmarshal(data, &b.Value); err != nil {
		return fmt.Errorf("unmarshal YAML bool: %w", err)
	}

	return nil
}

// MarshalYAML emits the public boolean scalar instead of internal presence
// tracking fields.
func (b RequiredBool) MarshalYAML() (any, error) {
	if !b.Set {
		return nil, nil //nolint:nilnil // nil tells the YAML marshaler to omit unset required booleans.
	}

	return b.Value, nil
}

// OptionalInt tracks whether a YAML integer field was explicitly present.
type OptionalInt struct {
	Value int  `json:"Value"`
	Set   bool `json:"Set"`
}

// UnmarshalYAML parses an integer and records field presence.
func (i *OptionalInt) UnmarshalYAML(data []byte) error {
	i.Set = true
	if err := yaml.Unmarshal(data, &i.Value); err != nil {
		return fmt.Errorf("unmarshal YAML integer: %w", err)
	}

	return nil
}

// MarshalYAML emits the public integer scalar instead of internal presence
// tracking fields.
func (i OptionalInt) MarshalYAML() (any, error) {
	if !i.Set {
		return nil, nil //nolint:nilnil // nil tells the YAML marshaler to omit unset optional integers.
	}

	return i.Value, nil
}

// Step is a named workflow step after validation.
type Step struct {
	Kind           string              `yaml:"kind" json:"Kind"`
	Agent          string              `yaml:"agent" json:"Agent"`
	Runtime        string              `yaml:"runtime" json:"Runtime"`
	Model          string              `yaml:"model" json:"Model"`
	Reasoning      string              `yaml:"reasoning" json:"Reasoning"`
	RuntimeDirs    []string            `yaml:"runtime_dirs" json:"RuntimeDirs"`
	Command        CommandStep         `yaml:"command" json:"Command"`
	Script         ScriptStep          `yaml:"script" json:"Script"`
	CWD            string              `yaml:"cwd" json:"CWD"`
	Env            map[string]string   `yaml:"env" json:"Env"`
	Skippable      bool                `yaml:"skippable" json:"Skippable"`
	AllowedResults map[string][]string `yaml:"allowed_results" json:"AllowedResults"`
	On             map[string]string   `yaml:"on" json:"On"`
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
	Argv []string `yaml:"argv" json:"Argv"`
}

// ScriptStep declares a deterministic repo-relative executable script step.
type ScriptStep struct {
	Path string   `yaml:"path" json:"Path"`
	Args []string `yaml:"args" json:"Args"`
	Body string   `yaml:"body" json:"Body"`
}

// AgentRef records a project-local agent reference used by a workflow.
type AgentRef struct {
	ID   string `json:"ID"`
	Path string `json:"Path"`
}

// Agent is a validated project-local role descriptor.
type Agent struct {
	ID          string `json:"ID"`
	Role        string `json:"Role"`
	Description string `json:"Description"`
	Body        string `json:"Body"`
	SourcePath  string `json:"SourcePath"`
}

type agentFrontmatter struct {
	ID          string `yaml:"id"`
	Role        string `yaml:"role"`
	Description string `yaml:"description"`
}
