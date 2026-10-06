package relay

import "context"

// FlushPushes waits for each currently open connection's preceding pushes to
// pass Run's capability/agent gates and seal-and-forward path, or for that
// connection to close. It is called off Run with the daemon lifetime context.
// Later pushes cannot extend the wait. Transport-down and replay holds retain
// the markers in FIFO order until recovery; daemon cancellation always releases
// the caller. Markers contain no payload and never reach the wire or logs.
func (m *V2SessionManager) FlushPushes(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.pushMu.Lock()
	done := make([]chan struct{}, 0, len(m.queues))
	for _, q := range m.queues {
		barrier := make(chan struct{})
		q.items = append(q.items, queuedEnv{barrier: barrier})
		done = append(done, barrier)
	}
	m.pushMu.Unlock()
	select {
	case m.drainCh <- struct{}{}:
	default:
	}
	for _, barrier := range done {
		select {
		case <-barrier:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
