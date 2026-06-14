package initupgrade

import (
	"fmt"

	"github.com/goccy/go-yaml/ast"
)

var workflowStepsYAMLPath = mustYAMLPath("steps")

type yamlMapHandle struct {
	doc  *yamlASTDocument
	path YAMLPath
	node *ast.MappingNode
}

func newYAMLMapHandle(doc *yamlASTDocument, path YAMLPath, node *ast.MappingNode) yamlMapHandle {
	return yamlMapHandle{doc: doc, path: path, node: node}
}

func rootYAMLMapHandle(doc *yamlASTDocument) yamlMapHandle {
	return newYAMLMapHandle(doc, YAMLPath{}, doc.root)
}

func (h yamlMapHandle) Path() YAMLPath {
	return h.path
}

func (h yamlMapHandle) Node() *ast.MappingNode {
	return h.node
}

func (h yamlMapHandle) Exists(field string) bool {
	return h.doc.Exists(h.path.Child(field))
}

func (h yamlMapHandle) Value(field string) (ast.Node, bool) {
	return h.doc.Value(h.path.Child(field))
}

func (h yamlMapHandle) Map(field string) (yamlMapHandle, bool) {
	path := h.path.Child(field)

	node, ok := h.doc.Map(path)
	if !ok {
		return yamlMapHandle{}, false
	}

	return newYAMLMapHandle(h.doc, path, node), true
}

func (h yamlMapHandle) ChildMap(field string) yamlMapHandle {
	path := h.path.Child(field)
	node, _ := h.doc.Map(path)

	return newYAMLMapHandle(h.doc, path, node)
}

func (h yamlMapHandle) AddField(field, value string) SurgicalEdit {
	return SurgicalEdit{Kind: EditAddYAMLField, Path: h.path.Child(field), Value: value}
}

func (h yamlMapHandle) SetField(field, value string) SurgicalEdit {
	return SurgicalEdit{Kind: EditSetYAMLField, Path: h.path.Child(field), Value: value}
}

func (h yamlMapHandle) RemoveField(field string) SurgicalEdit {
	return SurgicalEdit{Kind: EditRemoveYAMLField, Path: h.path.Child(field)}
}

func (h yamlMapHandle) AddMapEntry(field, value string) SurgicalEdit {
	return SurgicalEdit{Kind: EditAddYAMLMapEntry, Path: h.path, Key: field, Value: value}
}

func (h yamlMapHandle) AddASTField(field, value string) SurgicalEdit {
	return SurgicalEdit{Kind: EditASTAddYAMLField, Path: h.path.Child(field), Value: value}
}

func (h yamlMapHandle) SetASTField(field, value string) SurgicalEdit {
	return SurgicalEdit{Kind: EditASTSetYAMLField, Path: h.path.Child(field), Value: value}
}

func (h yamlMapHandle) RemoveASTField(field string) SurgicalEdit {
	return SurgicalEdit{Kind: EditASTRemoveYAMLField, Path: h.path.Child(field)}
}

func (h yamlMapHandle) AddASTMapEntry(field, value string) SurgicalEdit {
	return SurgicalEdit{Kind: EditASTAddYAMLMapEntry, Path: h.path, Key: field, Value: value}
}

type workflowStepVisit struct {
	ID   string
	Path YAMLPath
	Map  yamlMapHandle
}

func visitWorkflowSteps(file schemaMigrationFile, visit func(workflowStepVisit) ([]SurgicalEdit, error)) schemaMigrationDecision {
	doc, decision, ok := yamlSurfaceDocument(file)
	if !ok {
		return decision
	}

	stepsNode, exists := doc.Value(workflowStepsYAMLPath)
	if !exists {
		return unsupportedWorkflowStepsDecision("workflow steps mapping is missing")
	}

	steps, ok := stepsNode.(*ast.MappingNode)
	if !ok {
		return unsupportedWorkflowStepsDecision("workflow steps is not a YAML mapping")
	}

	var edits []SurgicalEdit

	for _, value := range steps.Values {
		stepID, ok := astMapKey(value)
		if !ok {
			return unsupportedWorkflowStepsDecision("workflow steps contains a non-scalar step id")
		}

		stepMap, ok := value.Value.(*ast.MappingNode)
		if !ok {
			return unsupportedWorkflowStepsDecision(fmt.Sprintf("workflow step %q is not a YAML mapping", stepID))
		}

		handle := newYAMLMapHandle(doc, workflowStepsYAMLPath.Child(stepID), stepMap)

		nextEdits, err := visit(workflowStepVisit{ID: stepID, Path: handle.Path(), Map: handle})
		if err != nil {
			return schemaMigrationDecision{Conflict: err.Error()}
		}

		edits = append(edits, nextEdits...)
	}

	return schemaMigrationDecision{Edits: edits}
}

func unsupportedWorkflowStepsDecision(message string) schemaMigrationDecision {
	return schemaMigrationDecision{
		Skipped:  message,
		Guidance: "rewrite the workflow to use a top-level steps mapping whose keys are step ids and whose values are step mappings before applying this workflow schema migration",
	}
}

func runtimeYAML(file schemaMigrationFile) (yamlMapHandle, schemaMigrationDecision, bool) {
	doc, decision, ok := yamlSurfaceDocument(file)
	if !ok {
		return yamlMapHandle{}, decision, false
	}

	return rootYAMLMapHandle(doc), schemaMigrationDecision{}, true
}

func agentFrontmatter(file schemaMigrationFile) (yamlMapHandle, schemaMigrationDecision, bool) {
	if file.InvalidMarkdown != nil {
		return yamlMapHandle{}, schemaMigrationDecision{Skipped: schemaMigrationInvalidFrontmatterMessage}, false
	}

	if !file.HasFrontmatter {
		return yamlMapHandle{}, schemaMigrationDecision{}, false
	}

	if file.InvalidYAML != nil {
		return yamlMapHandle{}, schemaMigrationDecision{Skipped: schemaMigrationInvalidFrontmatterMessage}, false
	}

	return rootYAMLMapHandle(file.Frontmatter), schemaMigrationDecision{}, true
}

func yamlSurfaceDocument(file schemaMigrationFile) (*yamlASTDocument, schemaMigrationDecision, bool) {
	if file.InvalidYAML != nil {
		return nil, schemaMigrationDecision{Skipped: schemaMigrationInvalidYAMLMessage}, false
	}

	if file.Doc == nil {
		return nil, schemaMigrationDecision{Conflict: "targeted YAML document is missing"}, false
	}

	return file.Doc, schemaMigrationDecision{}, true
}
