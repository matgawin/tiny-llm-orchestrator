package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

func loadAgent(realOrcDir, path string) (Agent, error) {
	content, err := readConfigFile(realOrcDir, path)
	if err != nil {
		return Agent{}, err
	}
	frontmatter, body, err := splitFrontmatter(string(content))
	if err != nil {
		return Agent{}, err
	}
	var meta agentFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return Agent{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	meta.ID = strings.TrimSpace(meta.ID)
	meta.Role = strings.TrimSpace(meta.Role)
	meta.Description = strings.TrimSpace(meta.Description)
	body = strings.TrimSpace(body)
	switch {
	case meta.ID == "":
		return Agent{}, errors.New("frontmatter id is required")
	case meta.Role == "":
		return Agent{}, errors.New("frontmatter role is required")
	case meta.Description == "":
		return Agent{}, errors.New("frontmatter description is required")
	case body == "":
		return Agent{}, errors.New("descriptor body is required")
	}
	return Agent{
		ID:          meta.ID,
		Role:        meta.Role,
		Description: meta.Description,
		Body:        body,
		SourcePath:  path,
	}, nil
}

func splitFrontmatter(content string) (string, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", "", errors.New("frontmatter must start with ---")
	}
	rest := strings.TrimPrefix(normalized, "---\n")
	before, after, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return "", "", errors.New("frontmatter must end with ---")
	}
	return before, after, nil
}
