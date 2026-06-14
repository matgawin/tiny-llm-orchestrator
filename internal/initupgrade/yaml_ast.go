package initupgrade

import (
	"fmt"
	"strconv"
	"strings"

	"tiny-llm-orchestrator/orc/internal/stableerr"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

const (
	yamlIndentSpaces    = 2
	yamlWildcardSegment = "*"
	yamlScalarTrue      = "true"
)

type yamlASTDocument struct {
	file            *ast.File
	root            *ast.MappingNode
	trailingNewline bool
	pendingComments map[string]*ast.CommentGroupNode
}

type yamlMapVisit struct {
	Path  YAMLPath
	Value ast.Node
}

func parseYAMLASTDocument(content []byte) (*yamlASTDocument, error) {
	file, err := parser.ParseBytes(content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse YAML AST: %w", err)
	}

	doc := &yamlASTDocument{
		file:            file,
		trailingNewline: len(content) == 0 || content[len(content)-1] == '\n',
		pendingComments: map[string]*ast.CommentGroupNode{},
	}

	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		doc.root = &ast.MappingNode{}
		return doc, nil
	}

	root, ok := file.Docs[0].Body.(*ast.MappingNode)
	if !ok {
		return nil, stableerr.New("YAML document is not a mapping")
	}

	doc.root = root

	return doc, nil
}

func parseMarkdownASTFrontmatter(content []byte) (*yamlASTDocument, []byte, bool, error) {
	frontmatter, body, ok, err := splitMarkdownFrontmatter(content)
	if err != nil || !ok {
		return nil, body, ok, err
	}

	doc, err := parseYAMLASTDocument(frontmatter)
	if err != nil {
		return nil, body, true, err
	}

	return doc, body, true, nil
}

func (d *yamlASTDocument) Exists(path YAMLPath) bool {
	_, ok := d.find(path)
	return ok
}

func (d *yamlASTDocument) Map(path YAMLPath) (*ast.MappingNode, bool) {
	if path.Empty() {
		return d.root, true
	}

	value, ok := d.find(path)
	if !ok {
		return nil, false
	}

	mapping, ok := value.Value.(*ast.MappingNode)

	return mapping, ok
}

func (d *yamlASTDocument) Value(path YAMLPath) (ast.Node, bool) {
	value, ok := d.find(path)
	if !ok {
		return nil, false
	}

	return value.Value, true
}

func (d *yamlASTDocument) Visit(pattern YAMLPath, visit func(yamlMapVisit) error) error {
	var walk func(*ast.MappingNode, []string, []string) error

	walk = func(mapping *ast.MappingNode, remaining []string, current []string) error {
		if len(remaining) == 0 {
			return nil
		}

		segment := remaining[0]

		for _, value := range mapping.Values {
			key, ok := astMapKey(value)
			if !ok {
				continue
			}

			if segment != yamlWildcardSegment && key != segment {
				continue
			}

			nextPath := append(append([]string(nil), current...), key)
			if len(remaining) == 1 {
				if err := visit(yamlMapVisit{Path: yamlPath(nextPath...), Value: value.Value}); err != nil {
					return err
				}

				continue
			}

			child, ok := value.Value.(*ast.MappingNode)
			if !ok {
				continue
			}

			if err := walk(child, remaining[1:], nextPath); err != nil {
				return err
			}
		}

		return nil
	}

	return walk(d.root, pattern.Segments(), nil)
}

func (d *yamlASTDocument) Add(path YAMLPath, value string) error {
	if path.Empty() {
		return stableerr.New("YAML path is required")
	}

	parentPath := yamlPath(path.segments[:len(path.segments)-1]...)
	key := path.segments[len(path.segments)-1]

	parent, err := d.ensureMap(parentPath)
	if err != nil {
		return err
	}

	if mappingValue(parent, key) != nil {
		return stableerr.Errorf("%s already exists", path.String())
	}

	node, err := newMappingValueNode(key, value, nodeColumn(parentPath))
	if err != nil {
		return err
	}

	if comment := d.takePendingComment(parentPath); comment != nil {
		_ = node.SetComment(comment)
	}

	insert := len(parent.Values)
	if path.String() == setupVersionField {
		if versionIdx := mappingValueIndex(parent, "version"); versionIdx >= 0 {
			insert = versionIdx + 1
		}
	}

	parent.Values = slicesInsert(parent.Values, insert, node)

	return nil
}

func (d *yamlASTDocument) Set(path YAMLPath, value string) error {
	target, ok := d.find(path)
	if !ok {
		return stableerr.Errorf("%s is missing", path.String())
	}

	node, err := newValueNode(value, target.Value.GetToken().Position.Column)
	if err != nil {
		return err
	}

	if comment := target.Value.GetComment(); comment != nil {
		_ = node.SetComment(comment)
	}

	if err := target.Replace(node); err != nil {
		return fmt.Errorf("replace YAML node: %w", err)
	}

	return nil
}

func (d *yamlASTDocument) Remove(path YAMLPath) error {
	if path.Empty() {
		return stableerr.New("YAML path is required")
	}

	parentPath := yamlPath(path.segments[:len(path.segments)-1]...)
	key := path.segments[len(path.segments)-1]

	parent, ok := d.Map(parentPath)
	if !ok {
		return stableerr.Errorf("%s is missing", path.String())
	}

	idx := mappingValueIndex(parent, key)
	if idx < 0 {
		return stableerr.Errorf("%s is missing", path.String())
	}

	if comment := parent.Values[idx].GetComment(); comment != nil {
		d.pendingComments[parentPath.String()] = comment
	}

	parent.Values = append(parent.Values[:idx], parent.Values[idx+1:]...)

	return nil
}

func (d *yamlASTDocument) Render() ([]byte, error) {
	if d.file == nil || len(d.file.Docs) == 0 {
		return nil, stableerr.New("YAML document is missing")
	}

	out := []byte(d.file.String())
	if d.trailingNewline && (len(out) == 0 || out[len(out)-1] != '\n') {
		out = append(out, '\n')
	}

	return out, nil
}

func (d *yamlASTDocument) find(path YAMLPath) (*ast.MappingValueNode, bool) {
	mapping := d.root
	for idx, segment := range path.segments {
		value := mappingValue(mapping, segment)
		if value == nil {
			return nil, false
		}

		if idx == len(path.segments)-1 {
			return value, true
		}

		next, ok := value.Value.(*ast.MappingNode)
		if !ok {
			return nil, false
		}

		mapping = next
	}

	return nil, false
}

func (d *yamlASTDocument) ensureMap(path YAMLPath) (*ast.MappingNode, error) {
	mapping := d.root
	for idx, segment := range path.segments {
		value := mappingValue(mapping, segment)
		if value == nil {
			node, err := newMappingValueNode(segment, "", nodeColumn(yamlPath(path.segments[:idx]...)))
			if err != nil {
				return nil, err
			}

			mapping.Values = append(mapping.Values, node)
			child, _ := node.Value.(*ast.MappingNode)
			mapping = child

			continue
		}

		child, ok := value.Value.(*ast.MappingNode)
		if !ok {
			return nil, stableerr.Errorf("%s is not a YAML mapping", yamlPath(path.segments[:idx+1]...).String())
		}

		mapping = child
	}

	return mapping, nil
}

func (d *yamlASTDocument) takePendingComment(path YAMLPath) *ast.CommentGroupNode {
	comment := d.pendingComments[path.String()]
	delete(d.pendingComments, path.String())

	return comment
}

func mappingValue(mapping *ast.MappingNode, key string) *ast.MappingValueNode {
	idx := mappingValueIndex(mapping, key)
	if idx < 0 {
		return nil
	}

	return mapping.Values[idx]
}

func mappingValueIndex(mapping *ast.MappingNode, key string) int {
	if mapping == nil {
		return -1
	}

	for idx, value := range mapping.Values {
		name, ok := astMapKey(value)
		if ok && name == key {
			return idx
		}
	}

	return -1
}

func astMapKey(value *ast.MappingValueNode) (string, bool) {
	if value == nil || value.Key == nil {
		return "", false
	}

	scalar, ok := value.Key.(ast.ScalarNode)
	if !ok {
		return "", false
	}

	return fmt.Sprint(scalar.GetValue()), true
}

func newMappingValueNode(key, value string, column int) (*ast.MappingValueNode, error) {
	source := key + ":"

	switch {
	case strings.TrimSpace(value) == "":
		source += "\n  {}\n"
	case strings.Contains(strings.TrimRight(value, "\n"), "\n"):
		source += "\n" + indentBlock(value, yamlIndentSpaces)
	default:
		source += " " + strings.TrimSpace(value) + "\n"
	}

	node, err := parseSingleMappingValue([]byte(source))
	if err != nil {
		return nil, err
	}

	node.AddColumn(column - 1)

	if strings.TrimSpace(value) == "" {
		node.Value = &ast.MappingNode{BaseNode: &ast.BaseNode{}, Values: nil}
	}

	return node, nil
}

func newValueNode(value string, column int) (ast.Node, error) {
	node, err := parseSingleMappingValue([]byte("value: " + strings.TrimSpace(value) + "\n"))
	if err != nil {
		return nil, err
	}

	node.Value.AddColumn(column - node.Value.GetToken().Position.Column)

	return node.Value, nil
}

func parseSingleMappingValue(content []byte) (*ast.MappingValueNode, error) {
	doc, err := parseYAMLASTDocument(content)
	if err != nil {
		return nil, err
	}

	if len(doc.root.Values) != 1 {
		return nil, stableerr.New("YAML edit value must parse as one mapping entry")
	}

	return doc.root.Values[0], nil
}

func nodeColumn(path YAMLPath) int {
	return len(path.segments)*2 + 1
}

func yamlScalarString(node ast.Node) string {
	scalar, ok := node.(ast.ScalarNode)
	if !ok {
		return ""
	}

	switch value := scalar.GetValue().(type) {
	case string:
		return value
	case bool:
		if value {
			return yamlScalarTrue
		}

		return "false"
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return fmt.Sprint(value)
	}
}

func yamlPathsOverlap(a, b YAMLPath) bool {
	if len(a.segments) == 0 || len(b.segments) == 0 {
		return false
	}

	short, long := a.segments, b.segments
	if len(short) > len(long) {
		short, long = long, short
	}

	for idx, segment := range short {
		if long[idx] != segment {
			return false
		}
	}

	return true
}

func slicesInsert[T any](items []T, idx int, value T) []T {
	items = append(items, value)
	copy(items[idx+1:], items[idx:])
	items[idx] = value

	return items
}
