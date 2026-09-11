package streamsup

import (
	"slices"
	"strings"
)

const (
	// maxContextUsageEntries caps every category or inventory list decoded from
	// a get_context_usage response. Ranking before the cut preserves the entries
	// that account for the most context while keeping retained storage fixed.
	maxContextUsageEntries = 32

	// maxContextUsageStringBytes is a byte bound, not a rune bound. An overlong
	// designated field rejects its complete entry rather than inventing a cut
	// name or path for a client to display.
	maxContextUsageStringBytes = 256
)

type weightedContextUsageEntry[T any] struct {
	value  T
	weight int
}

// boundContextUsageEntries returns independently owned copies of the heaviest
// valid entries. stringFields must return pointers to string fields on entry,
// which is a local copy of a flat decoded value rather than the source element.
func boundContextUsageEntries[T any](
	entries []T,
	tokenWeight func(T) int,
	stringFields func(*T) []*string,
) ([]T, int) {
	if len(entries) == 0 {
		return nil, 0
	}

	valid := make([]weightedContextUsageEntry[T], 0, len(entries))
	for _, source := range entries {
		entry := source
		fields := stringFields(&entry)
		if anyContextUsageStringOverlong(fields) {
			continue
		}
		for _, field := range fields {
			*field = strings.Clone(*field)
		}
		valid = append(valid, weightedContextUsageEntry[T]{
			value:  entry,
			weight: tokenWeight(entry),
		})
	}

	slices.SortStableFunc(valid, func(a, b weightedContextUsageEntry[T]) int {
		switch {
		case a.weight > b.weight:
			return -1
		case a.weight < b.weight:
			return 1
		default:
			return 0
		}
	})

	retained := min(len(valid), maxContextUsageEntries)
	result := make([]T, retained)
	for i := range retained {
		result[i] = valid[i].value
	}
	return result, len(entries) - retained
}

func anyContextUsageStringOverlong(fields []*string) bool {
	for _, field := range fields {
		if len(*field) > maxContextUsageStringBytes {
			return true
		}
	}
	return false
}
