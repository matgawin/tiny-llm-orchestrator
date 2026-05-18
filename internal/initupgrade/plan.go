// Package initupgrade plans no-write upgrades for project-local Tiny Orc setup.
package initupgrade

// ActionKind classifies a planned write candidate.
type ActionKind string

const (
	ActionCreate ActionKind = "create"
	ActionModify ActionKind = "modify"
)

// EditKind classifies a surgical edit for an existing file.
type EditKind string

const (
	EditAddYAMLField      EditKind = "add_yaml_field"
	EditSetYAMLField      EditKind = "set_yaml_field"
	EditRemoveYAMLField   EditKind = "remove_yaml_field"
	EditAddYAMLMapEntry   EditKind = "add_yaml_map_entry"
	EditAppendLine        EditKind = "append_line"
	EditAppendSection     EditKind = "append_section"
	EditReplaceIfBaseline EditKind = "replace_if_baseline"
)

// Result is the structured no-write upgrade plan.
type Result struct {
	ProjectRoot         string         `json:"project_root"`
	ConfigSchemaVersion int            `json:"config_schema_version"`
	CurrentSetupVersion int            `json:"current_setup_version"`
	TargetSetupVersion  int            `json:"target_setup_version"`
	Actions             []Action       `json:"actions"`
	Warnings            []Warning      `json:"warnings"`
	Conflicts           []Conflict     `json:"conflicts"`
	StaleFiles          []StaleFile    `json:"stale_files"`
	AffectedPaths       []AffectedPath `json:"affected_paths"`
	FollowUps           []FollowUp     `json:"follow_ups"`
}

// Action describes a safe create or modify that a later apply path may consume.
type Action struct {
	Kind         ActionKind     `json:"kind"`
	Path         string         `json:"path"`
	Reason       string         `json:"reason"`
	Content      []byte         `json:"content,omitempty"`
	Edits        []SurgicalEdit `json:"edits,omitempty"`
	FileIdentity *FileIdentity  `json:"file_identity,omitempty"`
}

// SurgicalEdit describes a localized edit for an existing file.
type SurgicalEdit struct {
	Kind  EditKind `json:"kind"`
	Path  string   `json:"path,omitempty"`
	Key   string   `json:"key,omitempty"`
	Value string   `json:"value,omitempty"`
}

// Warning describes a non-blocking operator-facing condition.
type Warning struct {
	Path     string `json:"path,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Guidance string `json:"guidance,omitempty"`
}

// Conflict describes an ambiguous or unsafe upgrade decision.
type Conflict struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Guidance string `json:"guidance"`
}

// StaleFile reports a removed managed file. V1 never plans deletion.
type StaleFile struct {
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	Guidance string `json:"guidance"`
}

// FollowUp describes post-plan or post-apply operator guidance.
type FollowUp struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AffectedPath identifies a path a later apply operation would write.
type AffectedPath struct {
	Path         string        `json:"path"`
	Exists       bool          `json:"exists"`
	FileIdentity *FileIdentity `json:"file_identity,omitempty"`
}

// FileIdentity records content metadata for changed-during-apply checks.
type FileIdentity struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
