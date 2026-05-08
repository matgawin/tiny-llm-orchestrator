package config

import "testing"

func TestLoadRejectsInvalidProjectConfig(t *testing.T) {
	tests := []struct {
		name     string
		agents   map[string]string
		config   string
		contains []string
	}{
		{
			name:     "step references missing configured agent",
			agents:   map[string]string{"coder": validAgentDescriptor("coder")},
			contains: []string{`step "plan" references missing agent "planner"`},
		},
		{
			name:     "invalid agent frontmatter",
			agents:   map[string]string{"planner": "---\nid: planner\nrole: planner\n---\n\nPlan the work.\n"},
			contains: []string{"frontmatter description is required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				agents: tt.agents,
				config: tt.config,
			})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}
