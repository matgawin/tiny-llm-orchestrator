package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

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
		return fmt.Errorf("prompt.delivery %q is invalid; allowed: stdin, file", runtime.Prompt.Delivery)
	}
	if err := validateRuntimeModel(runtime); err != nil {
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
		return fmt.Errorf("%s is required", name)
	}
	if !configIDPattern.MatchString(id) {
		return fmt.Errorf("%s %q is invalid; must match %s", name, id, configIDPattern.String())
	}
	return nil
}

func validateRuntimeExecutable(runtime Runtime) error {
	if runtime.Command.Executable == "" {
		return errors.New("is required")
	}
	if strings.Contains(runtime.Command.Executable, "{") || strings.Contains(runtime.Command.Executable, "}") {
		return errors.New("must not contain placeholders")
	}
	return nil
}

func validateRuntimeModel(runtime Runtime) error {
	if !runtime.Model.Supported {
		switch {
		case runtime.Model.Required:
			return errors.New("model.required requires model.supported=true")
		case runtime.Model.Default != "":
			return errors.New("model.default requires model.supported=true")
		case len(runtime.Model.Allowed) > 0:
			return errors.New("model.allowed requires model.supported=true")
		case len(runtime.Model.Args) > 0:
			return errors.New("model.args requires model.supported=true")
		}
		return nil
	}
	if err := validateStringListNoEmpty("model.allowed", runtime.Model.Allowed); err != nil {
		return err
	}
	if len(runtime.Model.Allowed) > 0 && runtime.Model.Default != "" && !slices.Contains(runtime.Model.Allowed, runtime.Model.Default) {
		return fmt.Errorf("model.default %q is not allowed by model.allowed", runtime.Model.Default)
	}
	return validateRuntimeArgv("model.args", runtime.Model.Args, runtime, placeholderContextModel)
}

func validateRuntimeDirectories(runtime Runtime) error {
	if !runtime.Directories.Supported {
		if len(runtime.Directories.Args) > 0 {
			return errors.New("directories.args requires directories.supported=true")
		}
		return nil
	}
	if err := validateRuntimeArgv("directories.args", runtime.Directories.Args, runtime, placeholderContextDirectories); err != nil {
		return err
	}
	if len(runtime.Directories.Args) == 0 {
		return errors.New("directories.args must include {dir} when directories.supported=true")
	}
	if !argvContainsPlaceholder(runtime.Directories.Args, "{dir}") {
		return errors.New("directories.args must include {dir} when directories.supported=true")
	}
	return nil
}

func validateRuntimeSandbox(runtime Runtime) error {
	if runtime.Sandbox.Required && !runtime.Sandbox.Supported {
		return errors.New("sandbox.required requires sandbox.supported=true")
	}
	if err := validateSandboxEnvConfig(runtime.Sandbox.Requirements.Env); err != nil {
		return fmt.Errorf("sandbox.requirements.env: %w", err)
	}
	for i, mount := range runtime.Sandbox.Requirements.Mounts {
		name := fmt.Sprintf("sandbox.requirements.mounts[%d]", i)
		if mount.Host == "" {
			return fmt.Errorf("%s.host is required", name)
		}
		if err := validateRuntimeRequirementHost(name+".host", mount.Host); err != nil {
			return err
		}
		if mount.Target == "" {
			return fmt.Errorf("%s.target is required", name)
		}
		if err := validateSandboxMountTarget("", SandboxHomeModeSynthetic, mount.Target); err != nil {
			return fmt.Errorf("%s.target %q: %w", name, mount.Target, err)
		}
		if mount.Mode != sandboxMountModeRO && mount.Mode != sandboxMountModeRW {
			return fmt.Errorf("%s.mode %q is invalid; allowed: ro, rw", name, mount.Mode)
		}
	}
	return nil
}

func validateRuntimeRequirementHost(name, host string) error {
	if strings.HasPrefix(host, "~") || strings.ContainsAny(host, "$`") {
		return fmt.Errorf("%s %q must not use shell, environment, or tilde expansion", name, host)
	}
	if filepath.IsAbs(host) {
		if filepath.Clean(host) != host {
			return fmt.Errorf("%s %q must be clean", name, host)
		}
		return nil
	}
	clean := filepath.Clean(host)
	if host != clean || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q must be clean and stay under repository root", name, host)
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
		if err := merged.addMounts(prefix+".mounts", runtime.Sandbox.Requirements.Mounts); err != nil {
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
			return fmt.Errorf("%s.%s conflicts with another fixed sandbox environment value for %s", source, name, name)
		}
		s.envSet[name] = value
	}
	return nil
}

func (s sandboxRequirementSet) addMounts(source string, mounts []SandboxMount) error {
	for i, mount := range mounts {
		target := filepath.Clean(mount.Target)
		next := sandboxRequirementMount{
			host:     cleanSandboxRequirementHost(mount.Host),
			mode:     mount.Mode,
			optional: mount.Optional.Value,
			source:   fmt.Sprintf("%s[%d]", source, i),
		}
		existing, ok := s.mounts[target]
		if !ok {
			s.mounts[target] = next
			continue
		}
		if existing.host != next.host || existing.mode != next.mode || existing.optional != next.optional {
			return fmt.Errorf("%s target %q conflicts with %s target %q", next.source, target, existing.source, target)
		}
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
	placeholderContextDirectories
)

func validateRuntimeArgv(name string, args []string, runtime Runtime, ctx placeholderContext) error {
	for i, arg := range args {
		if arg == "" {
			return fmt.Errorf("%s[%d] is empty", name, i)
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
				return fmt.Errorf("%s[%d] placeholder %s requires prompt.delivery=file", name, index, placeholder)
			}
		case "{model}":
			if !runtime.Model.Supported {
				return fmt.Errorf("%s[%d] placeholder %s requires model.supported=true", name, index, placeholder)
			}
			if ctx == placeholderContextDirectories {
				return fmt.Errorf("%s[%d] placeholder %s is not valid in directories.args", name, index, placeholder)
			}
		case "{dir}":
			if ctx != placeholderContextDirectories {
				return fmt.Errorf("%s[%d] placeholder %s is valid only in directories.args", name, index, placeholder)
			}
		default:
			return fmt.Errorf("%s[%d] contains unknown placeholder %s", name, index, placeholder)
		}
	}
	withoutPlaceholders := argvPlaceholderRegex.ReplaceAllString(arg, "")
	if strings.ContainsAny(withoutPlaceholders, "{}") {
		return fmt.Errorf("%s[%d] contains malformed placeholder syntax", name, index)
	}
	return nil
}

func validateStringListNoEmpty(name string, values []string) error {
	for i, value := range values {
		if value == "" {
			return fmt.Errorf("%s[%d] is empty", name, i)
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
