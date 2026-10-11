//go:build !e2e_realclaude

package dispatch

import "github.com/pyrycode/pyrycode/internal/protocol"

func observeReadinessRequest(string, *Conn, protocol.Envelope, bool) {}
