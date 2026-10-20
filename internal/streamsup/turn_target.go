package streamsup

import (
	"context"
	"io"
)

type turnTargetKey struct{}

// WithTurnTarget observes the exact selected stdin pipe before a user write.
// The observer runs synchronously outside the runner lock. Pipe identity is
// process-local bookkeeping and must never be published as client metadata.
func WithTurnTarget(ctx context.Context, observe func(io.Writer)) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, turnTargetKey{}, observe)
}

// ChildTarget retains the current or most recently retired child's pipe identity.
// It does not grant permission to write; WriteUserTurn selects a live target.
func (r *Runner) ChildTarget() io.Writer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.childTarget
}
