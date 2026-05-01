package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// ErrAttachUnavailable is returned by Session.Attach when the session has no
// bridge (foreground mode). The Phase 1.0 control plane consumes the
// supervisor's bridge directly and never calls this; Child B (#29) wires the
// control plane to use Session.Attach, at which point this error gets mapped
// to the existing "daemon may be in foreground mode" wire string.
var ErrAttachUnavailable = errors.New("sessions: attach unavailable (no bridge)")

// Session is one supervised claude instance plus the bridge that mediates its
// I/O in service mode. Exactly one Session per Pool entry. Phase 1.1+ spawns
// many; today there is exactly one (the bootstrap entry).
type Session struct {
	id     SessionID
	sup    *supervisor.Supervisor
	bridge *supervisor.Bridge // nil in foreground mode
	log    *slog.Logger
}

// ID returns the session's stable identifier.
func (s *Session) ID() SessionID { return s.id }

// State returns a snapshot of the supervisor's runtime state. Pure delegation
// to (*supervisor.Supervisor).State — preserves the existing
// safe-from-any-goroutine contract.
func (s *Session) State() supervisor.State { return s.sup.State() }

// Attach binds a client to this session's bridge. Returns ErrAttachUnavailable
// when the session has no bridge (foreground mode). Otherwise delegates to
// (*supervisor.Bridge).Attach, propagating supervisor.ErrBridgeBusy verbatim
// when a second client races for the same session.
//
// The bridge does not close in/out — caller owns their lifecycle and closes
// them after `done` fires. Same contract as supervisor.Bridge.Attach.
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error) {
	if s.bridge == nil {
		return nil, ErrAttachUnavailable
	}
	return s.bridge.Attach(in, out)
}

// Run blocks until ctx is cancelled, supervising this session's claude child.
// Pure delegation to (*supervisor.Supervisor).Run today; Phase 1.1+ keeps
// this shape so Pool.Run can fan out one goroutine per session.
func (s *Session) Run(ctx context.Context) error { return s.sup.Run(ctx) }
