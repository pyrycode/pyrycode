package e2emcp

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

const replayConnection = "mcp-files"

func replayValidation(_ Direction, raw []byte) error { _, err := ParseRPC(raw); return err }
func replayLiteral(expected RPCEvent, actual []byte) error {
	if expected.Raw != string(actual) {
		return protocolError()
	}
	return nil
}
func recordRPC(t *testing.T, c *Coordinator, d Direction, raw string) {
	t.Helper()
	if c.ForwardRPC(replayConnection, d, []byte(raw), func([]byte) error { return nil }) != nil {
		t.Fatal("record source RPC")
	}
}
func recordReplayHandshake(t *testing.T, c *Coordinator) {
	t.Helper()
	recordRPC(t, c, ClientToServer, ` {"jsonrpc":"2.0","id":0,"method":"initialize","params":{}} `)
	recordRPC(t, c, ServerToClient, `{"jsonrpc":"2.0","id":0,"result":{}}`)
	recordRPC(t, c, ClientToServer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}
func replaySourceTrace(t *testing.T) []RPCEvent {
	t.Helper()
	c, err := NewCoordinator(replayValidation)
	if err != nil || c.Register(replayConnection) != nil {
		t.Fatal("source coordinator")
	}
	if c.NativeInput(false, func() error { return nil }) != nil {
		t.Fatal("source input")
	}
	recordReplayHandshake(t, c)
	if c.NativeOutput(func() error { return nil }) != nil || c.NativeInput(true, func() error { return nil }) != nil {
		t.Fatal("source native")
	}
	recordRPC(t, c, ClientToServer, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"controlled"}}`)
	if c.NativeOutput(func() error { return nil }) != nil {
		t.Fatal("source output")
	}
	recordRPC(t, c, ServerToClient, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"actual result"}]}}`)
	events, err := c.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	return events
}
func TestMCPReplayPreservesActualInputAndOutputCausalBarriers(t *testing.T) {
	expected := replaySourceTrace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := NewReplayCoordinator(ctx, expected, replayValidation, replayLiteral)
	if err != nil || c.Register(replayConnection) != nil {
		t.Fatal("replay", err)
	}
	var order []string
	done := make(chan error, 1)
	go func() {
		for _, event := range expected {
			if err := c.ForwardRPC(event.Connection, event.Direction, []byte(event.Raw), func(raw []byte) error {
				if !bytes.Equal(raw, []byte(event.Raw)) {
					return protocolError()
				}
				order = append(order, "rpc")
				return nil
			}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	native := func(label string) func() error { return func() error { order = append(order, label); return nil } }
	if c.NativeInput(false, native("control-1")) != nil || c.NativeOutput(native("stdout-1")) != nil || c.NativeInput(true, native("user")) != nil || c.NativeOutput(native("stdout-2")) != nil || c.NativeInput(false, native("control-2")) != nil || c.NativeOutput(native("stdout-3")) != nil {
		t.Fatal("native barrier rejected")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{"control-1", "rpc", "rpc", "rpc", "stdout-1", "user", "rpc", "stdout-2", "rpc", "control-2", "stdout-3"}
	if len(order) != len(want) {
		t.Fatal("forward cardinality")
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatal("native forward crossed an actual RPC barrier")
		}
	}
	if _, err := c.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if c.CloseRPC(replayConnection, true, true, nil) != nil {
		t.Fatal("actual terminal")
	}
	actual, err := c.Finish()
	if err != nil || len(actual) != len(expected) {
		t.Fatal("terminal", err)
	}
	for i := range expected {
		if actual[i] != expected[i] {
			t.Fatal("literal or causal replay source changed")
		}
	}
}
func TestMCPReplayRefreshesPendingLedgerAfterWaitingForOtherForward(t *testing.T) {
	source, err := NewCoordinator(replayValidation)
	if err != nil || source.Register(replayConnection) != nil {
		t.Fatal("source")
	}
	recordReplayHandshake(t, source)
	req1 := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req2 := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	res1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	res2 := `{"jsonrpc":"2.0","id":2,"result":{}}`
	recordRPC(t, source, ClientToServer, req1)
	recordRPC(t, source, ClientToServer, req2)
	recordRPC(t, source, ServerToClient, res1)
	recordRPC(t, source, ServerToClient, res2)
	expected, err := source.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	early := make(chan struct{})
	var once sync.Once
	active := false
	validate := func(d Direction, raw []byte) error {
		frame, err := ParseRPC(raw)
		if active && err == nil && d == ServerToClient && frame.IDKey == "number:1" {
			once.Do(func() { close(early) })
		}
		return err
	}
	c, err := NewReplayCoordinator(ctx, expected, validate, replayLiteral)
	if err != nil || c.Register(replayConnection) != nil {
		t.Fatal(err)
	}
	active = true
	for _, event := range expected[:4] {
		if err := c.ForwardRPC(event.Connection, event.Direction, []byte(event.Raw), func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- c.ForwardRPC(replayConnection, ServerToClient, []byte(res1), func([]byte) error { return nil })
	}()
	select {
	case <-early:
	case <-ctx.Done():
		t.Fatal("early actual response not observed")
	}
	// The response holds the mutex through validation, then releases it while
	// waiting for request2. Its refreshed ledger must preserve request2 pending.
	if c.ForwardRPC(replayConnection, ClientToServer, []byte(req2), func([]byte) error { return nil }) != nil {
		t.Fatal("second request")
	}
	if err := <-done; err != nil {
		t.Fatal("first response", err)
	}
	if c.ForwardRPC(replayConnection, ServerToClient, []byte(res2), func([]byte) error { return nil }) != nil {
		t.Fatal("second pending request was lost")
	}
	if _, err := c.Checkpoint(); err != nil || c.CloseRPC(replayConnection, true, true, nil) != nil {
		t.Fatal("prefix/terminal", err)
	}
	if _, err := c.Finish(); err != nil {
		t.Fatal(err)
	}
}
func TestMCPReplayRejectsUnclosedOrChangedSourceBeforeForward(t *testing.T) {
	original := replaySourceTrace(t)
	for _, mutation := range []string{"sequence", "input-regression", "stdout-regression", "user-past-input", "missing-response", "changed-wire", "foreign-connection", "cancel", "abort"} {
		t.Run(mutation, func(t *testing.T) {
			events := append([]RPCEvent(nil), original...)
			switch mutation {
			case "sequence":
				events[1].Sequence = 1
			case "input-regression":
				events[3].NativeInputCount = 0
			case "stdout-regression":
				events[4].NativeStdoutCount = 0
			case "user-past-input":
				events[0].UserOrdinal = 2
			case "missing-response":
				events = events[:len(events)-1]
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c, err := NewReplayCoordinator(ctx, events, replayValidation, replayLiteral)
			invalidSource := mutation == "sequence" || mutation == "input-regression" || mutation == "stdout-regression" || mutation == "user-past-input" || mutation == "missing-response"
			if invalidSource {
				if err == nil {
					t.Fatal("invalid immutable source accepted")
				}
				return
			}
			if err != nil || c.Register(replayConnection) != nil {
				t.Fatal(err)
			}
			if mutation == "foreign-connection" {
				if c.Register("foreign") == nil {
					t.Fatal("fixture supplied unregistered authority")
				}
				return
			}
			if mutation == "changed-wire" {
				if c.NativeInput(false, func() error { return nil }) != nil {
					t.Fatal("source control")
				}
				err = c.ForwardRPC(replayConnection, ClientToServer, []byte(events[0].Raw+" "), func([]byte) error { t.Fatal("changed source forwarded"); return nil })
				if err == nil {
					t.Fatal("changed full request accepted")
				}
				return
			}
			done := make(chan error, 1)
			forwarded := make(chan struct{}, 1)
			go func() {
				done <- c.ForwardRPC(replayConnection, events[0].Direction, []byte(events[0].Raw), func([]byte) error {
					forwarded <- struct{}{}
					return protocolError()
				})
			}()
			if mutation == "cancel" {
				cancel()
			} else {
				c.Abort()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("missing source input accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled causal waiter not released")
			}
			select {
			case <-forwarded:
				t.Fatal("unavailable causal input was invented")
			default:
			}
			if _, err := c.Finish(); err == nil {
				t.Fatal("cancelled source sealed")
			}
		})
	}
}

func TestMCPReplayRejectsAlreadyCancelledOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	if _, err := NewReplayCoordinator(ctx, replaySourceTrace(t), replayValidation, replayLiteral); err == nil {
		t.Fatal("cancelled owner accepted")
	}
}

func TestMCPReplayCancellationAfterCompleteCannotPublishPrefixOrTerminal(t *testing.T) {
	for _, stage := range []string{"prefix", "terminal"} {
		t.Run(stage, func(t *testing.T) {
			expected := replaySourceTrace(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := NewReplayCoordinator(ctx, expected, replayValidation, replayLiteral)
			if err != nil || c.Register(replayConnection) != nil {
				t.Fatal("replay")
			}
			if c.NativeInput(false, func() error { return nil }) != nil {
				t.Fatal("initial input")
			}
			for _, event := range expected[:3] {
				if c.ForwardRPC(event.Connection, event.Direction, []byte(event.Raw), func([]byte) error { return nil }) != nil {
					t.Fatal("handshake")
				}
			}
			if c.NativeOutput(func() error { return nil }) != nil || c.NativeInput(true, func() error { return nil }) != nil {
				t.Fatal("native source")
			}
			if c.ForwardRPC(replayConnection, expected[3].Direction, []byte(expected[3].Raw), func([]byte) error { return nil }) != nil || c.NativeOutput(func() error { return nil }) != nil || c.ForwardRPC(replayConnection, expected[4].Direction, []byte(expected[4].Raw), func([]byte) error { return nil }) != nil {
				t.Fatal("complete source")
			}
			if stage == "terminal" {
				if _, err := c.Checkpoint(); err != nil || c.CloseRPC(replayConnection, true, true, nil) != nil {
					t.Fatal("prefix/close")
				}
			}
			// Suppress the asynchronous observer before cancelling the real context,
			// reproducing a watcher that has not run yet without changing ctx semantics.
			if !c.replay.stopWatch() {
				t.Fatal("owner unexpectedly cancelled early")
			}
			cancel()
			if c.failed {
				t.Fatal("control depended on asynchronous failure")
			}
			if stage == "prefix" {
				if _, err := c.Checkpoint(); err == nil {
					t.Fatal("cancelled prefix accepted")
				}
			} else {
				if _, err := c.Finish(); err == nil {
					t.Fatal("cancelled terminal accepted")
				}
			}
		})
	}
}
