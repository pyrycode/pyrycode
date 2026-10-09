package control

// Seal refuses new handlers and joins admitted handlers without closing the
// listener. The bound listener retains instance ownership until Close.
func (s *Server) Seal() {
	s.mu.Lock()
	s.sealed = true
	s.mu.Unlock()
	s.handlers.Wait()
}
