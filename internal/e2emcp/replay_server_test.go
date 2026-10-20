package e2emcp

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func unitReplayEvents() []RPCEvent {
	raw := []struct {
		d Direction
		s string
	}{
		{ClientToServer, ` {"jsonrpc":"2.0","id":0,"method":"initialize","params":{}} `},
		{ServerToClient, `{"jsonrpc":"2.0","id":0,"result":{}}`},
		{ClientToServer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{ClientToServer, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{ServerToClient, `{"jsonrpc":"2.0","id":1,"result":{}}`},
	}
	var events []RPCEvent
	for i, r := range raw {
		events = append(events, RPCEvent{Sequence: i + 1, Connection: "mcp-files", Direction: r.d, Raw: r.s})
	}
	return events
}
func TestReplayServersRequireActualResponsesPrefixEOFAndWait(t *testing.T) {
	for _, mutation := range []string{"complete", "wrong-response", "cancel-blocked-RPC", "early-exit", "premature-complete", "cancel-after-prefix"} {
		t.Run(mutation, func(t *testing.T) {
			b := actualUnitBinding(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			events := unitReplayEvents()
			if mutation == "wrong-response" {
				events[4].Raw = `{"jsonrpc":"2.0","id":1,"result":{"changed":true}}`
			}
			if mutation == "cancel-blocked-RPC" {
				for i := 3; i < len(events); i++ {
					events[i].NativeInputCount = 1
					events[i].UserOrdinal = 1
				}
			}
			matched := make(chan int, len(events))
			c, e := NewReplayCoordinator(ctx, events, func(_ Direction, raw []byte) error { _, e := ParseRPC(raw); return e }, func(want RPCEvent, actual []byte) error {
				if !bytes.Equal([]byte(want.Raw), actual) {
					return protocolError()
				}
				matched <- want.Sequence
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			env := []string{"HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID, "PYRY_E2EMCP_UNIT_HELPER=1"}
			if mutation == "early-exit" {
				env = append(env, "PYRY_E2EMCP_UNIT_EARLY_EXIT=1")
			}
			s, e := StartReplayServers(ctx, b, c, []string{"mcp-files"}, env, func(kind string, raw []byte) error {
				if kind != "mcp-files" || string(raw) != "controlled helper stderr\n" {
					return protocolError()
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Abort()
			if mutation == "wrong-response" || mutation == "early-exit" {
				select {
				case <-s.done:
				case <-time.After(3 * time.Second):
					t.Fatal("actual bad server did not fail promptly")
				}
				if proofs, e := s.CompleteAndJoin(time.Second); e == nil || proofs != nil {
					t.Fatal("bad actual server accepted")
				}
				if _, e := c.Finish(); e == nil {
					t.Fatal("bad actual server sealed")
				}
				return
			}
			if mutation == "premature-complete" {
				if proofs, e := s.CompleteAndJoin(time.Second); e == nil || proofs != nil {
					t.Fatal("prefix omitted")
				}
				return
			}
			count := len(events)
			if mutation == "cancel-blocked-RPC" {
				count = 3
			}
			for i := 1; i <= count; i++ {
				select {
				case n := <-matched:
					if n != i {
						t.Fatal("order")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("actual response missing")
				}
			}
			if mutation == "cancel-blocked-RPC" {
				started := time.Now()
				cancel()
				s.Abort()
				if time.Since(started) > 2*time.Second {
					t.Fatal("blocked RPC did not cancel")
				}
				if _, e := c.Finish(); e == nil {
					t.Fatal("cancelled RPC sealed")
				}
				return
			}
			logDeadline := time.Now().Add(time.Second)
			var prefixStderr map[string]string
			for {
				prefixStderr, e = s.StderrSnapshot()
				if e != nil {
					t.Fatal(e)
				}
				if prefixStderr["mcp-files"] == "controlled helper stderr\n" {
					break
				}
				if time.Now().After(logDeadline) {
					t.Fatal("actual stderr prefix missing")
				}
				time.Sleep(time.Millisecond)
			}
			prefix, e := c.Checkpoint()
			if e != nil || len(prefix) != len(events) {
				t.Fatal("live actual prefix", e)
			}
			select {
			case <-s.done:
				t.Fatal("actual server exited before client EOF")
			default:
			}
			if mutation == "cancel-after-prefix" {
				cancel()
				if proofs, e := s.CompleteAndJoin(time.Second); e == nil || proofs != nil {
					t.Fatal("cancelled prefix accepted")
				}
				return
			}
			proofs, e := s.CompleteAndJoin(3 * time.Second)
			if e != nil || len(proofs) != 1 {
				t.Fatal("actual process not joined", e)
			}
			p := proofs[0]
			terminalStderr, e := s.StderrSnapshot()
			if e != nil || terminalStderr["mcp-files"] != prefixStderr["mcp-files"] || terminalStderr["mcp-files"] != p.Stderr {
				t.Fatal("actual terminal stderr changed pinned prefix")
			}

			if p.NativeProxyPID != 0 || p.NativeOwnerPID != b.NativeOwnerPID || p.ServerPID <= 1 || p.ServerPID == b.NativeOwnerPID || p.ExitCode != 0 || !p.InputEOF || !p.OutputEOF || !p.StderrEOF || p.InputFrames != 3 || p.OutputFrames != 2 {
				t.Fatal("actual process facts", p)
			}
			terminal, e := c.Finish()
			if e != nil || len(terminal) != len(prefix) {
				t.Fatal("terminal source trace", e)
			}
			for i := range prefix {
				if terminal[i] != prefix[i] {
					t.Fatal("source prefix changed")
				}
			}
		})
	}
}
func TestReplayServersRejectForeignOrDuplicateDeclarationBeforeLaunch(t *testing.T) {
	for _, kinds := range [][]string{{"mcp-foreign"}, {"mcp-approve"}, {"mcp-files", "mcp-files"}, nil} {
		b := actualUnitBinding(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		c, e := NewReplayCoordinator(ctx, unitReplayEvents(), func(_ Direction, raw []byte) error { _, e := ParseRPC(raw); return e }, func(RPCEvent, []byte) error { return nil })
		if e != nil {
			t.Fatal(e)
		}
		s, e := StartReplayServers(ctx, b, c, kinds, []string{"HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID}, func(string, []byte) error { return nil })
		cancel()
		if e == nil || s != nil || len(c.sessions) != 0 {
			t.Fatal("foreign declaration reached process stage")
		}
	}
}
func TestProxyServerRejectsAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	c, _ := NewCoordinator(func(_ Direction, raw []byte) error { _, e := ParseRPC(raw); return e })
	if s, e := NewProxyServer(ctx, actualUnitBinding(t), c, func([]byte) error { return nil }); e == nil || s != nil {
		t.Fatal("already cancelled source accepted")
	}
}
