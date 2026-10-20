package e2emcp

import "sync"

// ServerLogs is a bounded snapshot of bytes successfully forwarded from actual
// owned server stderr. It neither supplies log bytes nor declares stderr EOF.
// Prefix owners must pin a snapshot and require identical terminal bytes.
type ServerLogs struct {
	mu     sync.Mutex
	bodies map[string]string
	bytes  int
	failed bool
}

func newServerLogs(kinds []string) *ServerLogs {
	logs := &ServerLogs{bodies: map[string]string{}}
	for _, kind := range kinds {
		logs.bodies[kind] = ""
	}
	return logs
}
func (s *ServerLogs) append(kind string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.bodies[kind]
	if s.failed || !exists || len(raw) > MaxFrameBytes-s.bytes {
		s.failed = true
		return protocolError()
	}
	s.bodies[kind] += string(raw)
	s.bytes += len(raw)
	return nil
}
func (s *ServerLogs) Snapshot() (map[string]string, error) {
	if s == nil {
		return nil, protocolError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return nil, protocolError()
	}
	out := map[string]string{}
	for kind, body := range s.bodies {
		out[kind] = body
	}
	return out, nil
}
