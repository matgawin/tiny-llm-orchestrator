package initupgrade

import (
	"strconv"

	"tiny-llm-orchestrator/orc/internal/config"

	"github.com/goccy/go-yaml/ast"
)

type configFile struct {
	content       []byte
	identity      FileIdentity
	schemaVersion int
	setupVersion  int
	data          config.ProjectConfig
	doc           *yamlASTDocument
	loadErr       error
}

func (c configFile) rootMap() yamlMapHandle {
	return rootYAMLMapHandle(c.doc)
}

func (c configFile) runtimePath(name string) string {
	return c.mapScalarPath("runtimes", name)
}

func (c configFile) workflowPath(name string) string {
	workflows, ok := c.rootMap().Map("workflows")
	if !ok {
		return ""
	}

	value, ok := workflows.Value(name)
	if !ok {
		return ""
	}

	if path := yamlScalarString(value); path != "" {
		return path
	}

	workflow, ok := workflows.Map(name)
	if !ok {
		return ""
	}

	return scalarNodeString(workflow.Value("path"))
}

func (c configFile) agentPath(name string) string {
	return c.mapScalarPath("agents", name)
}

func (c configFile) mapScalarPath(parent, name string) string {
	handle, ok := c.rootMap().Map(parent)
	if !ok {
		return ""
	}

	return scalarNodeString(handle.Value(name))
}

func scalarNodeString(node ast.Node, ok bool) string {
	if !ok {
		return ""
	}

	return yamlScalarString(node)
}

func intScalarField(doc *yamlASTDocument, key string) int {
	node, ok := doc.Value(mustYAMLPath(key))
	if !ok {
		return 0
	}

	parsed, err := strconv.Atoi(yamlScalarString(node))
	if err != nil {
		return 0
	}

	return parsed
}
