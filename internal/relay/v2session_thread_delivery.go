package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

var (
	// ErrThreadWithheld means current recipient eligibility forbids the envelope.
	ErrThreadWithheld = errors.New("relay: thread delivery withheld")
	// ErrThreadDeliveryFailed means encoding, sealing or transport failed.
	ErrThreadDeliveryFailed = errors.New("relay: thread delivery failed")
)

// DeliverThread waits for local ordered transport acceptance of one envelope.
// Call only off Run, send sequences serially, and stop on any error before sending
// their final marker. Terminal error envelopes are also accepted. Enqueue and
// assembly completion alone do not certify delivery.
// Cancellation releases the caller; an already sealed canceled send retires the
// connection because its nonce may have reached the peer. Retirement, manager
// shutdown, or a missing ordered lane returns ErrConnNotFound. Legacy callers use Push.
func (m *V2SessionManager) DeliverThread(ctx context.Context, connID string, env protocol.Envelope) error {
	return m.deliverThread(ctx, connID, env, nil)
}

func (m *V2SessionManager) deliverThread(ctx context.Context, connID string, env protocol.Envelope, query *threadQuery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !threadContent(env.Type) && env.Type != protocol.TypeError {
		return ErrThreadDeliveryFailed
	}
	// Round-trip detaches all borrowed payload and metadata, and rejects encoding
	// failures before they can occupy a shared lane slot.
	raw, err := json.Marshal(env)
	if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes {
		return ErrThreadDeliveryFailed
	}
	var detached protocol.Envelope
	if err := json.Unmarshal(raw, &detached); err != nil {
		return ErrThreadDeliveryFailed
	}
	m.hostedMu.Lock()
	in := m.hostedInboxes[connID]
	m.hostedMu.Unlock()
	if in == nil || query != nil && query.inbox != in {
		return ErrConnNotFound
	}
	done := make(chan error, 1)
	o := &hostedOutput{env: &detached, callerCtx: ctx, outcome: done, query: query}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.hostedMu.Lock()
		if in.retired || in.overflow || in.ctx.Err() != nil || m.hostedInboxes[connID] != in {
			m.hostedMu.Unlock()
			return ErrConnNotFound
		}
		if in.used < hostedQueueLimit {
			in.used++
			in.pending = append(in.pending, o)
			m.hostedMu.Unlock()
			select {
			case m.hostedChanges <- struct{}{}:
			default:
			}
			break
		}
		capacity := in.capacity
		m.hostedMu.Unlock()
		select {
		case <-capacity:
		case <-ctx.Done():
			return ctx.Err()
		case <-in.ctx.Done():
			return ErrConnNotFound
		}
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-in.ctx.Done():
		// A failure published before retirement takes precedence over that retirement.
		select {
		case err := <-done:
			return err
		default:
			return ErrConnNotFound
		}
	}
}

func (o *hostedOutput) finish(err error) {
	if o.outcome != nil {
		o.outcome <- err
		o.outcome = nil
	}
}

func threadContent(typ string) bool {
	return threadItemUpdate(typ) || typ == protocol.TypeThreadItems || typ == protocol.TypeThreadReset || typ == protocol.TypeThreadToolBody || typ == protocol.TypeThreadChildren
}

func threadDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrThreadDeliveryFailed, err)
}
