package initupgrade

import "bytes"

var knownReplacementBaselines = map[string][][]byte{}

type removedManagedScaffoldFile struct {
	Path     string
	Reason   string
	Guidance string
}

var removedManagedScaffoldFiles []removedManagedScaffoldFile

func replacementBaselineMatches(path string, content []byte) bool {
	for _, baseline := range knownReplacementBaselines[path] {
		if bytes.Equal(content, baseline) {
			return true
		}
	}

	return false
}
