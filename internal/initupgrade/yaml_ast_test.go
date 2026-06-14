package initupgrade

import (
	"reflect"
	"slices"
	"testing"
)

const (
	testDefaultsPath         = "defaults"
	testDefaultsLoopCapsPath = "defaults.loop_caps"
	yamlEnabledTrue          = "enabled: true"
)

func TestYAMLPathOverlapUsesStructuredSegments(t *testing.T) {
	tests := map[string]struct {
		left  string
		right string
		want  bool
	}{
		"equal path":               {left: testDefaultsLoopCapsPath, right: testDefaultsLoopCapsPath, want: true},
		"parent child":             {left: testDefaultsPath, right: testDefaultsLoopCapsPath, want: true},
		"child parent":             {left: testDefaultsLoopCapsPath, right: testDefaultsPath, want: true},
		"raw prefix sibling":       {left: "defaults.loop", right: testDefaultsLoopCapsPath, want: false},
		"different branch":         {left: testDefaultsLoopCapsPath, right: "runtimes.codex", want: false},
		"different prefix sibling": {left: "default", right: testDefaultsPath, want: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := yamlPathsOverlap(mustYAMLPath(tt.left), mustYAMLPath(tt.right)); got != tt.want {
				t.Fatalf("yamlPathsOverlap(%q, %q) = %v, want %v", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestYAMLASTDocumentMapMutationAndRenderPreservesUnrelatedFormatting(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("# top\nversion: 1\ndefaults:\n  # old note\n  max_loops: 3\nruntimes:\n  codex: runtimes/codex.yaml\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if !doc.Exists(mustYAMLPath("defaults.max_loops")) {
		t.Fatalf("defaults.max_loops missing before mutation")
	}

	if err := doc.Remove(mustYAMLPath("defaults.max_loops")); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	if err := doc.Add(mustYAMLPath(testDefaultsLoopCapsPath), "enabled: true\nsoft: 3\nhard: 4"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath("version"), "2"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	if err := doc.Add(mustYAMLPath("runtimes.local"), "runtimes/local.yaml"); err != nil {
		t.Fatalf("Add map entry returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "# top\nversion: 2\ndefaults:\n  # old note\n  loop_caps:\n    enabled: true\n    soft: 3\n    hard: 4\nruntimes:\n  codex: runtimes/codex.yaml\n  local: runtimes/local.yaml\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetPreservesScalarAndInlineComment(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("setup_version: 0 # keep setup note\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath("setup_version"), "1"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "setup_version: 1 # keep setup note\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetPreservesBlankScalarBehavior(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("field: old # keep field note\nnext: keep\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath("field"), "   "); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "field: # keep field note\nnext: keep\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetAcceptsBlockMappingValue(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("version: 1\ndefaults:\n  loop_caps: legacy\nruntimes:\n  codex: runtimes/codex.yaml\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath(testDefaultsLoopCapsPath), "enabled: true\nsoft: 2\nhard: 4"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "version: 1\ndefaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\nruntimes:\n  codex: runtimes/codex.yaml\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetReplacesBlockMappingValue(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("# top\nversion: 1\ndefaults:\n  loop_caps:\n    enabled: true\n    soft: 1\n    hard: 2 # old hard note\n  retry_limit: 2\nruntimes:\n  codex: runtimes/codex.yaml\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath(testDefaultsLoopCapsPath), "enabled: false\nsoft: 3\nhard: 4"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "# top\nversion: 1\ndefaults:\n  loop_caps:\n    enabled: false\n    soft: 3\n    hard: 4\n  retry_limit: 2\nruntimes:\n  codex: runtimes/codex.yaml\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetAcceptsWholeSequenceValue(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("steps: old\nnext: keep\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath("steps"), "- plan\n- code\n- review"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "steps:\n  - plan\n  - code\n  - review\nnext: keep\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetAcceptsBlockScalarValue(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("description: old\nnext: keep\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath("description"), "|\n  line one\n  line two"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	out, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "description: |\n    line one\n    line two\nnext: keep\n"
	if string(out) != want {
		t.Fatalf("rendered YAML mismatch\nwant:\n%s\ngot:\n%s", want, out)
	}
}

func TestYAMLASTDocumentSetRejectsInvalidValue(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("field: old\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	if err := doc.Set(mustYAMLPath("field"), "["); err == nil {
		t.Fatalf("Set returned nil error for invalid value")
	}
}

func TestYAMLASTDocumentWildcardVisit(t *testing.T) {
	doc, err := parseYAMLASTDocument([]byte("steps:\n  plan:\n    model: gpt-5\n  code:\n    model: gpt-5-codex\n  review:\n    timeout: 30\n"))
	if err != nil {
		t.Fatalf("parseYAMLASTDocument returned error: %v", err)
	}

	var got []string

	err = doc.Visit(mustYAMLPath("steps.*.model"), func(visit yamlMapVisit) error {
		got = append(got, visit.Path.String()+"="+yamlScalarString(visit.Value))
		return nil
	})
	if err != nil {
		t.Fatalf("Visit returned error: %v", err)
	}

	want := []string{"steps.plan.model=gpt-5", "steps.code.model=gpt-5-codex"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visits = %#v, want %#v", got, want)
	}
}

func TestMarkdownASTFrontmatterPreservesBodyBytesExactly(t *testing.T) {
	body := []byte("\n# Planner\r\nKeep trailing spaces.  \n\n")
	content := append([]byte("---\nid: planner\nlegacy: true\n---\n"), body...)

	doc, parsedBody, ok, err := parseMarkdownASTFrontmatter(content)
	if err != nil {
		t.Fatalf("parseMarkdownASTFrontmatter returned error: %v", err)
	}

	if !ok {
		t.Fatalf("frontmatter not detected")
	}

	if !slices.Equal(parsedBody, body) {
		t.Fatalf("body = %q, want exact %q", parsedBody, body)
	}

	if err := doc.Remove(mustYAMLPath("legacy")); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	if err := doc.Add(mustYAMLPath("modern"), "true"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	frontmatter, err := doc.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	joined := joinMarkdownFrontmatter(frontmatter, parsedBody)
	if !slices.Equal(joined[len(joined)-len(body):], body) {
		t.Fatalf("joined body tail = %q, want exact %q", joined[len(joined)-len(body):], body)
	}
}

func TestYAMLASTDocumentInvalidInputsReturnErrors(t *testing.T) {
	if _, err := parseYAMLASTDocument([]byte("defaults: [\n")); err == nil {
		t.Fatalf("parseYAMLASTDocument returned nil error for invalid YAML")
	}

	_, _, ok, err := parseMarkdownASTFrontmatter([]byte("---\nid: planner\n"))
	if err == nil {
		t.Fatalf("parseMarkdownASTFrontmatter returned nil error for invalid frontmatter")
	}

	if !ok {
		t.Fatalf("invalid frontmatter should still be recognized as frontmatter")
	}
}
