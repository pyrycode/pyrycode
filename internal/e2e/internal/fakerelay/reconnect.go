package fakerelay

import (
	"context"
	"time"
)

// ReconnectBinary retires the current binary socket and waits for a different
// registration under the same server ID. The daemon must reconnect itself.
// An absent registration is transitional; a retiring registration is never ready.
// Returns false on cancellation, deadline expiry or relay shutdown.
func (s *Server) ReconnectBinary(ctx context.Context, serverID string) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	old, closed := s.binaries[serverID], s.closed
	s.mu.Unlock()
	if closed {
		return false
	}
	if old != nil {
		_ = old.conn.CloseNow() // Best effort; the registration transition is the barrier.
	}
	s.log.Debug("fakerelay: awaiting binary replacement", "server_id", serverID)
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		s.mu.Lock()
		current, closed := s.binaries[serverID], s.closed
		s.mu.Unlock()
		if closed {
			return false
		}
		if current != nil && current != old {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
