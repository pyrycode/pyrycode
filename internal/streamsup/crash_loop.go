package streamsup

import "time"

// crashLoopFastExits is how many consecutive fast exits make a crash episode, and
// crashLoopFastUptime is the uptime under which an exit counts as fast (#2724).
//
// N is sized against the default ladder (500ms doubling, 30s cap): the 4th fast
// exit enters backoff after 0.5 + 1 + 2 = 3.5s of total backoff wait, inside the
// 10s the ticket allows. N = 5 would be 7.5s and N = 6 15.5s.
//
// The uptime bound is well under BackoffReset (60s), so a child that stayed up
// long enough to reset the ladder always ends the episode too.
const (
	crashLoopFastExits  = 4
	crashLoopFastUptime = 10 * time.Second
)

// crashEpisode counts consecutive fast exits for one runner. It is fed only from
// Run's backoff branch, so a deliberate restart and a shutdown are never seen: a
// restart neither counts nor ends an episode, and a spawn that failed before
// claude launched counts as fast. Run-goroutine-private, so it needs no lock.
type crashEpisode struct{ fast int }

// observe records one exit that is entering the backoff ladder and reports whether
// it is the one that completes an episode. It fires exactly once per episode
// because the count keeps rising past crashLoopFastExits and only a slow exit
// zeroes it.
func (e *crashEpisode) observe(uptime time.Duration) bool {
	if uptime >= crashLoopFastUptime {
		e.fast = 0
		return false
	}
	e.fast++
	return e.fast == crashLoopFastExits
}
