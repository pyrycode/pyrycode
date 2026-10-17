package history

// ResumeReadStats exposes actual-read counters to the external Store proof
// without adding a production instrumentation API.
func ResumeReadStats(s *Store, reset bool) (int64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, n := s.readBytes, s.segmentsOpened
	if reset {
		s.readBytes, s.segmentsOpened = 0, 0
	}
	return b, n
}
