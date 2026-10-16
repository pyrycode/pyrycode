package relay

import "context"

// HasThreadDemand reports retained query ownership in authenticated connection
// generations. A completed query's join keeps its publication available for live
// deltas until that generation retires. No socket, provider or history I/O occurs.
func (m *V2SessionManager) HasThreadDemand(ctx context.Context, conversation string) bool {
	if ctx.Err() != nil {
		return false
	}
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	for _, in := range m.hostedInboxes {
		if in.retired || in.ctx == nil || in.ctx.Err() != nil {
			continue
		}
		if _, ok := in.threadJoins[conversation]; ok {
			return true
		}
		if in.query != nil && in.query.conversation == conversation {
			return true
		}
	}
	return false
}
