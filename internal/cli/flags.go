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

func appendFlagValue(args []string, index *int, target *[]string) bool {
	var value string
	if !assignFlagValue(args, index, &value) {
		return false
	}
	*target = append(*target, value)
	return true
}
