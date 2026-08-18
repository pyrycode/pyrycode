package conversations

import "time"

// archiveIdleThreshold is the inactivity window after which an unpromoted
// conversation becomes eligible for auto-archive. Promoted channels and
// manually archived conversations are exempt regardless of LastUsedAt.
const archiveIdleThreshold = 30 * 24 * time.Hour

// ShouldArchive reports whether c should be auto-archived as of now.
//
// A conversation archives iff it is unpromoted (a discussion, not a channel)
// AND not manually archived AND its LastUsedAt is at least
// archiveIdleThreshold in the past. The boundary is inclusive: exactly 30 days
// idle archives. Auto-archive deletes the row, so it must not claim a
// conversation the user archived to keep — manual archive (#880/#881) is
// recoverable by design and stays durable until the user unarchives.
//
// Pure function. No I/O, no clock — the caller passes now. The sweep loop
// (#220) is responsible for picking now (typically time.Now()) and for
// iterating the registry.
func ShouldArchive(c Conversation, now time.Time) bool {
	// Promoted channels are long-lived by definition.
	if c.IsPromoted {
		return false
	}
	// Manual archive is recoverable; the destructive sweep must not undo it.
	if c.IsArchived {
		return false
	}
	return now.Sub(c.LastUsedAt) >= archiveIdleThreshold
}
