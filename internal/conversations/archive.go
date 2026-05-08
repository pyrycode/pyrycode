package conversations

import "time"

// archiveIdleThreshold is the inactivity window after which an unpromoted
// conversation becomes eligible for auto-archive. Promoted channels are
// exempt regardless of LastUsedAt.
const archiveIdleThreshold = 30 * 24 * time.Hour

// ShouldArchive reports whether c should be auto-archived as of now.
//
// A conversation archives iff it is unpromoted (a discussion, not a channel)
// AND its LastUsedAt is at least archiveIdleThreshold in the past. The
// boundary is inclusive: exactly 30 days idle archives.
//
// Pure function. No I/O, no clock — the caller passes now. The sweep loop
// (#220) is responsible for picking now (typically time.Now()) and for
// iterating the registry.
func ShouldArchive(c Conversation, now time.Time) bool {
	if c.IsPromoted {
		return false
	}
	return now.Sub(c.LastUsedAt) >= archiveIdleThreshold
}
