package config

import (
	"sort"
	"strings"
)

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func formatStringSet(values map[string]struct{}) string {
	return strings.Join(sortedKeys(values), ", ")
}
