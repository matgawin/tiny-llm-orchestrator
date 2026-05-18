package initupgrade

import (
	"fmt"

	"tiny-llm-orchestrator/orc/internal/config"

	"github.com/goccy/go-yaml"
)

type configFile struct {
	content       []byte
	identity      FileIdentity
	schemaVersion int
	setupVersion  int
	data          config.ProjectConfig
	doc           yaml.MapSlice
}

func (c configFile) has(key string) bool {
	_, ok := mapLookup(c.doc, key)
	return ok
}

func (c configFile) hasNested(parent, key string) bool {
	node, ok := mapLookup(c.doc, parent)
	if !ok {
		return false
	}

	nested, ok := asMapSlice(node)
	if !ok {
		return false
	}

	_, ok = mapLookup(nested, key)

	return ok
}

func (c configFile) scalarNested(parent, key string) string {
	node, ok := mapLookup(c.doc, parent)
	if !ok {
		return ""
	}

	nested, ok := asMapSlice(node)
	if !ok {
		return ""
	}

	value, ok := mapLookup(nested, key)
	if !ok {
		return ""
	}

	return fmt.Sprint(value)
}

func (c configFile) runtimePath(name string) string {
	if c.data.Runtimes == nil {
		return ""
	}

	return c.data.Runtimes[name]
}

func (c configFile) workflowPath(name string) string {
	if c.data.Workflows == nil {
		return ""
	}

	return c.data.Workflows[name].Path
}

func (c configFile) agentPath(name string) string {
	if c.data.Agents == nil {
		return ""
	}

	return c.data.Agents[name]
}

func mapLookup(items yaml.MapSlice, key string) (any, bool) {
	for _, item := range items {
		name, ok := item.Key.(string)
		if ok && name == key {
			return item.Value, true
		}
	}

	return nil, false
}

func asMapSlice(value any) (yaml.MapSlice, bool) {
	switch typed := value.(type) {
	case yaml.MapSlice:
		return typed, true
	case map[string]any:
		items := make(yaml.MapSlice, 0, len(typed))
		for key, value := range typed {
			items = append(items, yaml.MapItem{Key: key, Value: value})
		}

		return items, true
	default:
		return nil, false
	}
}
