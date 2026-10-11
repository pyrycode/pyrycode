//go:build e2e_realclaude

package dispatch

import (
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func observeReadinessRequest(phase string, c *Conn, env protocol.Envelope, ok bool) {
	streamsup.ObserveReadinessRequest(phase, c.ConnID(), env, ok)
}
