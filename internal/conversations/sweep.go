package conversations

import "time"

// Sweep removes every conversation in reg for which ShouldArchive(c, now)
// returns true. Returns the number of entries archived.
//
// Sweep operates on a snapshot of reg.List(): the underlying slice may be
// modified (by Delete) during iteration without affecting the snapshot the
// loop is walking.
//
// Sweep does NOT call reg.Save — disk persistence is the daemon-wiring
// ticket's concern. Callers responsible for durability must Save themselves
// after Sweep returns.
func Sweep(reg *Registry, now time.Time) int {
	n := 0
	for _, c := range reg.List() {
		if ShouldArchive(c, now) {
			if reg.Delete(c.ID) {
				n++
			}
		}
	}
	return n
}
