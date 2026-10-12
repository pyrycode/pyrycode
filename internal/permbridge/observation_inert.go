//go:build !permission_observe

package permbridge

// Ordinary builds have no recorder, retained identities or observer locks.
type observationState struct{}
type observationLifetime struct{}

func (*observationState) register(string, Request, string) observationLifetime {
	return observationLifetime{}
}
func (observationLifetime) terminal(Verdict, bool) {}
