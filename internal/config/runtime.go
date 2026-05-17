package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"tiny-llm-orchestrator/orc/internal/stableerr"

	"github.com/goccy/go-yaml"
)

const (
	runtimePromptDeliveryStdin = "stdin"
	runtimePromptDeliveryFile  = "file"
)

var (
	configIDPattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
	argvPlaceholderRegex = regexp.MustCompile(`\{[^{}]+\}`)
)

type runtimeDescriptorYAML struct {
	ID          string             `yaml:"id"`
	Command     RuntimeCommand     `yaml:"command"`
	Prompt      RuntimePrompt      `yaml:"prompt"`
	Model       RuntimeModel       `yaml:"model"`
	Reasoning   RuntimeReasoning   `yaml:"reasoning"`
	Directories RuntimeDirectories `yaml:"directories"`
	Sandbox     RuntimeSandbox     `yaml:"sandbox"`
}

func loadRuntime(realOrcDir, path string) (Runtime, error) {
	content, err := readConfigFile(realOrcDir, path)
	if err != nil {
		return Runtime{}, err
	}

	var raw runtimeDescriptorYAML
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return Runtime{}, fmt.Errorf("parse %s: %w", path, err)
	}

	runtime := Runtime{
		ID:          strings.TrimSpace(raw.ID),
		Command:     raw.Command,
		Prompt:      raw.Prompt,
		Model:       raw.Model,
		Reasoning:   raw.Reasoning,
		Directories: raw.Directories,
		Sandbox:     raw.Sandbox,
		SourcePath:  path,
	}
	if err := validateRuntime(runtime); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func validateRuntime(runtime Runtime) error {
	field := func(name string, err error) error {
		if err == nil {
			return nil
		}

		return fmt.Errorf("%s: %w", name, err)
	}

	if err := validateConfigID("id", runtime.ID); err != nil {
		return err
	}

	if err := field("command.executable", validateRuntimeExecutable(runtime)); err != nil {
		return err
	}

	if err := validateRuntimeArgv("command.args", runtime.Command.Args, runtime, placeholderContextCommand); err != nil {
		return err
	}

	if err := validateRuntimeArgv("command.normal_args", runtime.Command.NormalArgs, runtime, placeholderContextCommand); err != nil {
		return err
	}

	if err := validateRuntimeArgv("command.sandbox_args", runtime.Command.SandboxArgs, runtime, placeholderContextCommand); err != nil {
		return err
	}

	if runtime.Prompt.Delivery != runtimePromptDeliveryStdin && runtime.Prompt.Delivery != runtimePromptDeliveryFile {
		return stableerr.Errorf("prompt.delivery %q is invalid; allowed: stdin, file", runtime.Prompt.Delivery)
	}

	if err := validateRuntimeModel(runtime); err != nil {
		return err
	}

	if err := validateRuntimeReasoning(runtime); err != nil {
		return err
	}

	if err := validateRuntimeDirectories(runtime); err != nil {
		return err
	}

	if err := validateRuntimeSandbox(runtime); err != nil {
		return err
	}

	return nil
}

func validateConfigID(name, id string) error {
	if id == "" {
		return stableerr.Errorf("%s is required", name)
	}

	if !configIDPattern.MatchString(id) {
		return stableerr.Errorf("%s %q is invalid; must match %s", name, id, configIDPattern.String())
	}

	return nil
}

func validateRuntimeExecutable(runtime Runtime) error {
	if runtime.Command.Executable == "" {
		return stableerr.New("is required")
	}

	if strings.Contains(runtime.Command.Executable, "{") || strings.Contains(runtime.Command.Executable, "}") {
		return stableerr.New("must not contain placeholders")
	}

	return nil
}

func validateRuntimeModel(runtime Runtime) error {
	return validateRuntimeSelection(runtime, runtimeSelectionValidation{
		name:        "model",
		supported:   runtime.Model.Supported,
		required:    runtime.Model.Required,
		defaultName: runtime.Model.Default,
		allowed:     runtime.Model.Allowed,
		args:        runtime.Model.Args,
		placeholder: placeholderContextModel,
	})
}

func validateRuntimeReasoning(runtime Runtime) error {
	return validateRuntimeSelection(runtime, runtimeSelectionValidation{
		name:        "reasoning",
		supported:   runtime.Reasoning.Supported,
		required:    runtime.Reasoning.Required,
		defaultName: runtime.Reasoning.Default,
		allowed:     runtime.Reasoning.Allowed,
		args:        runtime.Reasoning.Args,
		placeholder: placeholderContextReasoning,
	})
}

type runtimeSelectionValidation struct {
	name        string
	supported   bool
	required    bool
	defaultName string
	allowed     []string
	args        []string
	placeholder placeholderContext
}

func validateRuntimeSelection(runtime Runtime, selection runtimeSelectionValidation) error {
	if !selection.supported {
		switch {
		case selection.required:
			return stableerr.Errorf("%s.required requires %s.supported=true", selection.name, selection.name)
		case selection.defaultName != "":
			return stableerr.Errorf("%s.default requires %s.supported=true", selection.name, selection.name)
		case len(selection.allowed) > 0:
			return stableerr.Errorf("%s.allowed requires %s.supported=true", selection.name, selection.name)
		case len(selection.args) > 0:
			return stableerr.Errorf("%s.args requires %s.supported=true", selection.name, selection.name)
		}

		return nil
	}

	if err := validateStringListNoEmpty(selection.name+".allowed", selection.allowed); err != nil {
		return err
	}

	if len(selection.allowed) > 0 && selection.defaultName != "" && !slices.Contains(selection.allowed, selection.defaultName) {
		return stableerr.Errorf("%s.default %q is not allowed by %s.allowed", selection.name, selection.defaultName, selection.name)
	}

	return validateRuntimeArgv(selection.name+".args", selection.args, runtime, selection.placeholder)
}

func validateRuntimeDirectories(runtime Runtime) error {
	if !runtime.Directories.Supported {
		if len(runtime.Directories.Args) > 0 {
			return stableerr.New("directories.args requires directories.supported=true")
		}

		return nil
	}

	if err := validateRuntimeArgv("directories.args", runtime.Directories.Args, runtime, placeholderContextDirectories); err != nil {
		return err
	}

	if len(runtime.Directories.Args) == 0 {
		return stableerr.New("directories.args must include {dir} when directories.supported=true")
	}

	if !argvContainsPlaceholder(runtime.Directories.Args, "{dir}") {
		return stableerr.New("directories.args must include {dir} when directories.supported=true")
	}

	return nil
}

func validateRuntimeSandbox(runtime Runtime) error {
	if runtime.Sandbox.Required && !runtime.Sandbox.Supported {
		return stableerr.New("sandbox.required requires sandbox.supported=true")
	}

	if err := validateRuntimeSandboxEnvConfig(runtime.Sandbox.Requirements.Env); err != nil {
		return fmt.Errorf("sandbox.requirements.env: %w", err)
	}

	mountIDs := map[string]struct{}{}
	for i, mount := range runtime.Sandbox.Requirements.Mounts {
		if err := validateRuntimeSandboxMount(i, mount, mountIDs); err != nil {
			return err
		}
	}

	for name, ref := range runtime.Sandbox.Requirements.Env.SetFromMount {
		if err := validateSandboxEnvName(name); err != nil {
			return fmt.Errorf("sandbox.requirements.env.set_from_mount[%q]: %w", name, err)
		}

		if ref.Mount == "" {
			return stableerr.Errorf("sandbox.requirements.env.set_from_mount[%q].mount is required", name)
		}

		if _, ok := mountIDs[ref.Mount]; !ok {
			return stableerr.Errorf("sandbox.requirements.env.set_from_mount[%q].mount %q does not reference a sandbox.requirements.mounts id", name, ref.Mount)
		}

		if ref.Value != "target" {
			return stableerr.Errorf("sandbox.requirements.env.set_from_mount[%q].value %q is invalid; allowed: target", name, ref.Value)
		}
	}

	return nil
}

func validateRuntimeSandboxEnvConfig(env RuntimeSandboxEnvConfig) error {
	if err := validateSandboxEnvConfig(SandboxEnvConfig{Pass: env.Pass, Set: env.Set}); err != nil {
		return err
	}

	for name := range env.SetFromMount {
		if err := validateSandboxEnvName(name); err != nil {
			return fmt.Errorf("set_from_mount[%q]: %w", name, err)
		}
	}

	return nil
}

func validateRuntimeSandboxMount(index int, mount RuntimeSandboxMount, mountIDs map[string]struct{}) error {
	name := fmt.Sprintf("sandbox.requirements.mounts[%d]", index)
	if mount.ID != "" {
		if err := validateConfigID(name+".id", mount.ID); err != nil {
			return err
		}

		if _, ok := mountIDs[mount.ID]; ok {
			return stableerr.Errorf("%s.id %q duplicates another sandbox.requirements.mounts id", name, mount.ID)
		}

		mountIDs[mount.ID] = struct{}{}
	}

	if mount.Mode != sandboxMountModeRO && mount.Mode != sandboxMountModeRW {
		return stableerr.Errorf("%s.mode %q is invalid; allowed: ro, rw", name, mount.Mode)
	}

	hasSource := runtimeMountHasSource(mount.Source)
	switch {
	case mount.Host != "" && hasSource:
		return stableerr.Errorf("%s must not combine simple host with extended source", name)
	case mount.Host == "" && !hasSource:
		return stableerr.Errorf("%s.host is required", name)
	case mount.Host != "":
		return validateRuntimeSimpleSandboxMount(name, mount)
	default:
		return validateRuntimeExtendedSandboxMount(name, mount)
	}
}

func validateRuntimeSimpleSandboxMount(name string, mount RuntimeSandboxMount) error {
	if err := validateRuntimeRequirementHost(name+".host", mount.Host); err != nil {
		return err
	}

	if mount.Target.Path == "" {
		return stableerr.Errorf("%s.target is required", name)
	}

	if runtimeMountHasStructuredTarget(mount.Target) {
		return stableerr.Errorf("%s.target must use either simple path or extended target fields, not both", name)
	}

	if err := validateSandboxMountTarget("", SandboxHomeModeSynthetic, mount.Target.Path); err != nil {
		return fmt.Errorf("%s.target %q: %w", name, mount.Target.Path, err)
	}

	return nil
}

func validateRuntimeExtendedSandboxMount(name string, mount RuntimeSandboxMount) error {
	if mount.Target.Path != "" {
		return stableerr.Errorf("%s.target must use extended target fields when source is extended", name)
	}

	if mount.Source.Env == "" {
		return stableerr.Errorf("%s.source.env is required", name)
	}

	if err := validateSandboxEnvName(mount.Source.Env); err != nil {
		return fmt.Errorf("%s.source.env: %w", name, err)
	}

	if mount.Source.Fallback.HostHome != "" {
		if err := validateCleanRelativeNoExpansion(name+".source.fallback.host_home", mount.Source.Fallback.HostHome); err != nil {
			return err
		}
	}

	if !mount.Target.EnvSameAsSource {
		return stableerr.Errorf("%s.target.env_same_as_source must be true for env-sourced mounts", name)
	}

	if mount.Source.Fallback.HostHome != "" {
		if mount.Target.Fallback.SandboxHome == "" {
			return stableerr.Errorf("%s.target.fallback.sandbox_home is required when source.fallback.host_home is set", name)
		}

		if err := validateCleanRelativeNoExpansion(name+".target.fallback.sandbox_home", mount.Target.Fallback.SandboxHome); err != nil {
			return err
		}
	} else if mount.Target.Fallback.SandboxHome != "" {
		return stableerr.Errorf("%s.target.fallback.sandbox_home requires source.fallback.host_home", name)
	}

	return nil
}

func runtimeMountHasSource(source RuntimeSandboxMountSource) bool {
	return source.Env != "" || source.Fallback.HostHome != "" || source.Create
}

func runtimeMountHasStructuredTarget(target RuntimeSandboxMountTarget) bool {
	return target.EnvSameAsSource || target.Fallback.SandboxHome != ""
}

func validateRuntimeRequirementHost(name, host string) error {
	if strings.HasPrefix(host, "~") || strings.ContainsAny(host, "$`") {
		return stableerr.Errorf("%s %q must not use shell, environment, or tilde expansion", name, host)
	}

	if filepath.IsAbs(host) {
		if filepath.Clean(host) != host {
			return stableerr.Errorf("%s %q must be clean", name, host)
		}

		return nil
	}

	clean := filepath.Clean(host)
	if host != clean || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return stableerr.Errorf("%s %q must be clean and stay under repository root", name, host)
	}

	return nil
}

func validateCleanRelativeNoExpansion(name, value string) error {
	if strings.HasPrefix(value, "~") || strings.ContainsAny(value, "$`") {
		return stableerr.Errorf("%s %q must not use shell, environment, or tilde expansion", name, value)
	}

	if filepath.IsAbs(value) {
		return stableerr.Errorf("%s %q must be relative", name, value)
	}

	clean := filepath.Clean(value)
	if value != clean || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return stableerr.Errorf("%s %q must be clean and stay under its base directory", name, value)
	}

	return nil
}

func validateSelectedRuntimeSandboxRequirementConflicts(sandbox *SandboxConfig, workflows map[string]Workflow, runtimes map[string]Runtime) error {
	if sandbox == nil {
		return nil
	}

	merged := sandboxRequirementSet{
		envSet: map[string]string{},
		mounts: map[string]sandboxRequirementMount{},
	}
	if err := merged.addEnvSet("sandbox.env.set", sandbox.Env.Set); err != nil {
		return err
	}

	if err := merged.addMounts("sandbox.mounts", sandbox.Mounts); err != nil {
		return err
	}

	for _, runtimeID := range SelectedRuntimeIDs(workflows) {
		runtime, ok := runtimes[runtimeID]
		if !ok {
			continue
		}

		prefix := fmt.Sprintf("runtime %q sandbox.requirements", runtimeID)
		if err := merged.addEnvSet(prefix+".env.set", runtime.Sandbox.Requirements.Env.Set); err != nil {
			return err
		}

		if err := merged.addRuntimeMounts(prefix+".mounts", runtime.Sandbox.Requirements.Mounts); err != nil {
			return err
		}
	}

	return nil
}

type sandboxRequirementSet struct {
	envSet map[string]string
	mounts map[string]sandboxRequirementMount
}

type sandboxRequirementMount struct {
	host     string
	mode     string
	optional bool
	source   string
}

func (s sandboxRequirementSet) addEnvSet(source string, env map[string]string) error {
	for name, value := range env {
		if existing, ok := s.envSet[name]; ok && existing != value {
			return stableerr.Errorf("%s.%s conflicts with another fixed sandbox environment value for %s", source, name, name)
		}

		s.envSet[name] = value
	}

	return nil
}

func (s sandboxRequirementSet) addMounts(source string, mounts []SandboxMount) error {
	for i, mount := range mounts {
		if err := s.addStaticMountDescriptor(filepath.Clean(mount.Target), sandboxRequirementMount{
			host:     cleanSandboxRequirementHost(mount.Host),
			mode:     mount.Mode,
			optional: mount.Optional.Value,
			source:   fmt.Sprintf("%s[%d]", source, i),
		}); err != nil {
			return err
		}
	}

	return nil
}

func (s sandboxRequirementSet) addRuntimeMounts(source string, mounts []RuntimeSandboxMount) error {
	for i, mount := range mounts {
		if runtimeMountHasSource(mount.Source) {
			continue
		}

		if err := s.addStaticMountDescriptor(filepath.Clean(mount.Target.Path), sandboxRequirementMount{
			host:     cleanSandboxRequirementHost(mount.Host),
			mode:     mount.Mode,
			optional: mount.Optional.Value,
			source:   fmt.Sprintf("%s[%d]", source, i),
		}); err != nil {
			return err
		}
	}

	return nil
}

func (s sandboxRequirementSet) addStaticMountDescriptor(target string, next sandboxRequirementMount) error {
	existing, ok := s.mounts[target]
	if !ok {
		s.mounts[target] = next
		return nil
	}

	if existing.host != next.host || existing.mode != next.mode || existing.optional != next.optional {
		return stableerr.Errorf("%s target %q conflicts with %s target %q", next.source, target, existing.source, target)
	}

	return nil
}

func cleanSandboxRequirementHost(host string) string {
	if filepath.IsAbs(host) {
		return filepath.Clean(host)
	}

	return filepath.Clean(host)
}

// SelectedRuntimeIDs returns the runtime IDs selected by agent steps in loaded workflows.
func SelectedRuntimeIDs(workflows map[string]Workflow) []string {
	seen := map[string]bool{}
	ids := make([]string, 0)

	for _, workflow := range workflows {
		for _, step := range workflow.Steps {
			if step.EffectiveKind() != StepKindAgent {
				continue
			}

			runtimeID := workflow.EffectiveRuntime(step)
			if runtimeID == "" || seen[runtimeID] {
				continue
			}

			seen[runtimeID] = true
			ids = append(ids, runtimeID)
		}
	}

	slices.Sort(ids)

	return ids
}

type placeholderContext int

const (
	placeholderContextCommand placeholderContext = iota
	placeholderContextModel
	placeholderContextReasoning
	placeholderContextDirectories
)

func validateRuntimeArgv(name string, args []string, runtime Runtime, ctx placeholderContext) error {
	for i, arg := range args {
		if arg == "" {
			return stableerr.Errorf("%s[%d] is empty", name, i)
		}

		if err := validateRuntimePlaceholders(name, i, arg, runtime, ctx); err != nil {
			return err
		}
	}

	return nil
}

func validateRuntimePlaceholders(name string, index int, arg string, runtime Runtime, ctx placeholderContext) error {
	for _, placeholder := range argvPlaceholderRegex.FindAllString(arg, -1) {
		switch placeholder {
		case "{agent_id}", "{step_id}", "{attempt_id}", "{run_id}":
		case "{prompt_file}":
			if runtime.Prompt.Delivery != runtimePromptDeliveryFile {
				return stableerr.Errorf("%s[%d] placeholder %s requires prompt.delivery=file", name, index, placeholder)
			}
		case "{model}":
			if !runtime.Model.Supported {
				return stableerr.Errorf("%s[%d] placeholder %s requires model.supported=true", name, index, placeholder)
			}

			if ctx == placeholderContextDirectories || ctx == placeholderContextReasoning {
				return stableerr.Errorf("%s[%d] placeholder %s is not valid in %s", name, index, placeholder, placeholderContextName(ctx))
			}
		case "{reasoning}":
			if !runtime.Reasoning.Supported {
				return stableerr.Errorf("%s[%d] placeholder %s requires reasoning.supported=true", name, index, placeholder)
			}

			if ctx != placeholderContextReasoning {
				return stableerr.Errorf("%s[%d] placeholder %s is valid only in reasoning.args", name, index, placeholder)
			}
		case "{dir}":
			if ctx != placeholderContextDirectories {
				return stableerr.Errorf("%s[%d] placeholder %s is valid only in directories.args", name, index, placeholder)
			}
		default:
			return stableerr.Errorf("%s[%d] contains unknown placeholder %s", name, index, placeholder)
		}
	}

	withoutPlaceholders := argvPlaceholderRegex.ReplaceAllString(arg, "")
	if strings.ContainsAny(withoutPlaceholders, "{}") {
		return stableerr.Errorf("%s[%d] contains malformed placeholder syntax", name, index)
	}

	return nil
}

func placeholderContextName(ctx placeholderContext) string {
	switch ctx {
	case placeholderContextCommand, placeholderContextModel:
		return "command or model args"
	case placeholderContextReasoning:
		return "reasoning.args"
	case placeholderContextDirectories:
		return "directories.args"
	default:
		return "command or model args"
	}
}

func validateStringListNoEmpty(name string, values []string) error {
	for i, value := range values {
		if value == "" {
			return stableerr.Errorf("%s[%d] is empty", name, i)
		}
	}

	return nil
}

func argvContainsPlaceholder(args []string, placeholder string) bool {
	for _, arg := range args {
		if strings.Contains(arg, placeholder) {
			return true
		}
	}

	return false
}
