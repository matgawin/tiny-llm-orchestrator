package cli

func assignFlagValue(args []string, index *int, target *string) bool {
	next := *index + 1
	if next >= len(args) || args[next] == "" {
		return false
	}
	*index = next
	*target = args[next]
	return true
}
