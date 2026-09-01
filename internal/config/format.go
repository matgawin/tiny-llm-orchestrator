package config

import (
	"maps"
	"slices"
	"strings"
)

func formatStringSet(values map[string]struct{}) string {
	return strings.Join(slices.Sorted(maps.Keys(values)), ", ")
}
