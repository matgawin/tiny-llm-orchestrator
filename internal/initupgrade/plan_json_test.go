package initupgrade

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSurgicalEditJSONUsesPathOnlyForStructuredYAMLEdits(t *testing.T) {
	tests := []struct {
		name     string
		edit     SurgicalEdit
		wantPath bool
	}{
		{
			name:     "YAML edit",
			edit:     SurgicalEdit{Kind: EditAddYAMLField, Path: mustYAMLPath("defaults.loop_caps"), Value: yamlEnabledTrue},
			wantPath: true,
		},
		{
			name:     "append line",
			edit:     SurgicalEdit{Kind: EditAppendLine, Value: ".orc/runs/"},
			wantPath: false,
		},
		{
			name:     "append section",
			edit:     SurgicalEdit{Kind: EditAppendSection, Value: "## Tiny Orc\n"},
			wantPath: false,
		},
		{
			name:     "replace baseline",
			edit:     SurgicalEdit{Kind: EditReplaceIfBaseline, Value: "replacement\n"},
			wantPath: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := json.Marshal(tt.edit)
			if err != nil {
				t.Fatalf("json.Marshal returned error: %v", err)
			}

			hasPath := strings.Contains(string(content), `"path"`)
			if hasPath != tt.wantPath {
				t.Fatalf("JSON = %s, path presence = %v, want %v", content, hasPath, tt.wantPath)
			}
		})
	}
}
