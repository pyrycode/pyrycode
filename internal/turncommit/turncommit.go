// Package turncommit carries a "commit gate" alongside a delivery context.
//
// The inbound message queue (internal/msgqueue) delivers a queued turn to claude
// through an injected seam that blocks in two phases: first it WAITS for claude to
// go idle so the previous turn can finish, then it WRITES the turn. A user may
// drop a still-queued message at any time. Dropping during the WAIT must succeed,
// because the message has not been written. Dropping once the WRITE has begun must
// not, or a half-written turn could reach claude.
//
// The queue owns that decision but cannot see inside the seam to tell the two
// phases apart. So it passes a Gate down the delivery context. The seam calls the
// gate once, after the wait and before the write. Under the queue's lock the gate
// either claims the head, marking it un-droppable and returning true so the write
// proceeds, or reports the head was already dropped, returning false so the seam
// aborts without writing. The claim and the drop both take the queue lock, so the
// committed-versus-dropped decision is atomic and there is no race.
package turncommit

import (
	"context"
	"errors"
)

// Gate claims the current head for writing. It returns true when the caller may
// proceed to write, meaning the head is still queued and is now marked
// un-droppable. It returns false when the head was dropped while the caller was
// waiting for claude to go idle, meaning the caller must abort without writing.
// Call it once per delivery attempt, after the idle-gate wait and before the
// write.
type Gate func() bool

// ErrDropped is the error a delivery seam returns when the gate reports the
// queued message was dropped before the write began. The queue treats it as a
// clean drop with no retry and no give-up, the same as any path that leaves the
// head no longer at the front of the queue.
var ErrDropped = errors.New("turncommit: queued turn dropped before commit")

type ctxKey struct{}

// With returns a copy of ctx carrying gate. A nil gate is a no-op and returns ctx
// unchanged, so a caller that does not gate delivery passes ctx straight through
// and From reports no gate.
func With(ctx context.Context, gate Gate) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, gate)
}

// From returns the gate carried by ctx, or nil when there is none. A nil gate
// means the delivery is not queue-driven, for example the ACP path, so the seam
// writes unconditionally.
func From(ctx context.Context) Gate {
	gate, _ := ctx.Value(ctxKey{}).(Gate)
	return gate
}
