package e2emcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func newTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(func(_ Direction, raw []byte) error { _, err := ParseRPC(raw); return err })
	if err != nil {
		t.Fatal(err)
	}
	if c.Register("current-epoch/files") != nil {
		t.Fatal("register")
	}
	return c
}
func forwardHandshake(t *testing.T, c *Coordinator) {
	t.Helper()
	for _, v := range []struct {
		d   Direction
		raw string
	}{
		{ClientToServer, ` {"params":{"protocolVersion":"2025-06-18"}, "id":0,"method":"initialize","jsonrpc":"2.0"} `},
		{ServerToClient, ` {"jsonrpc":"2.0", "id":0, "result":{"capabilities":{"tools":{}},"serverInfo":{"name":"pyry_files","version":"dev"}}} `},
		{ClientToServer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
	} {
		if err := c.ForwardRPC("current-epoch/files", v.d, []byte(v.raw), func(raw []byte) error {
			var dst bytes.Buffer
			if err := WriteLine(&dst, raw); err != nil {
				return err
			}
			if dst.String() != v.raw+"\n" {
				t.Fatal("forward changed literal wire")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCoordinatorPreservesSuccessfulForwardBarriersAndWire(t *testing.T) {
	c := newTestCoordinator(t)
	if c.NativeInput(true, func() error { return nil }) != nil || c.NativeOutput(func() error { return nil }) != nil {
		t.Fatal("native forwards")
	}
	forwardHandshake(t, c)
	if c.NativeInput(false, func() error { return nil }) != nil || c.NativeOutput(func() error { return nil }) != nil {
		t.Fatal("native control forward")
	}
	request := ` {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_file","arguments":{"path":"<>&\\u0041"}}} `
	response := ` {"result":{"content":[{"type":"text","text":"<>&\\u003e"}],"isError":false},"id":1,"jsonrpc":"2.0"} `
	for _, v := range []struct {
		d   Direction
		raw string
	}{{ClientToServer, request}, {ServerToClient, response}} {
		if c.ForwardRPC("current-epoch/files", v.d, []byte(v.raw), func(raw []byte) error { raw[0] = 'x'; return nil }) != nil {
			t.Fatal("forward")
		}
	}
	if c.CloseRPC("current-epoch/files", true, true, nil) != nil {
		t.Fatal("close")
	}
	got, err := c.Finish()
	if err != nil || len(got) != 5 {
		t.Fatal("finish", err)
	}
	for i, v := range got {
		want := 1
		if i >= 3 {
			want = 2
		}
		if v.Sequence != i+1 || v.UserOrdinal != 1 || v.NativeStdoutCount != want {
			t.Fatal("incorrect successful-forward barrier")
		}
	}
	if got[3].Raw != request || got[4].Raw != response {
		t.Fatal("caller changed retained raw")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []RPCEvent
	if json.Unmarshal(encoded, &decoded) != nil || decoded[3].Raw != request || decoded[4].Raw != response {
		t.Fatal("serialized ledger changed wire")
	}
	got[0].Raw = "changed"
	if c.events[0].Raw == "changed" {
		t.Fatal("finish aliases retained ledger")
	}
}
func TestCoordinatorRejectsFailedOrUnclosedActualForwards(t *testing.T) {
	for _, mutation := range []string{"unknown-connection", "duplicate-connection", "privacy", "write-error", "partial-write", "partial-eof", "wait-error", "missing-close", "post-close", "native-input-failure", "native-output-failure"} {
		t.Run(mutation, func(t *testing.T) {
			c := newTestCoordinator(t)
			forwardHandshake(t, c)
			request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			var err error
			switch mutation {
			case "unknown-connection":
				err = c.ForwardRPC("foreign", ClientToServer, request, func([]byte) error { t.Fatal("unowned request forwarded"); return nil })
			case "duplicate-connection":
				err = c.Register("current-epoch/files")
			case "privacy":
				c.validate = func(Direction, []byte) error { return errors.New("unsafe") }
				err = c.ForwardRPC("current-epoch/files", ClientToServer, request, func([]byte) error { t.Fatal("unsafe request forwarded"); return nil })
			case "write-error":
				err = c.ForwardRPC("current-epoch/files", ClientToServer, request, func([]byte) error { return io.ErrClosedPipe })
			case "partial-write":
				err = c.ForwardRPC("current-epoch/files", ClientToServer, request, func(raw []byte) error { return WriteLine(shortWriter{}, raw) })
			case "partial-eof":
				err = c.CloseRPC("current-epoch/files", true, false, nil)
			case "wait-error":
				err = c.CloseRPC("current-epoch/files", true, true, errors.New("actual Wait failed"))
			case "missing-close":
				_, err = c.Finish()
			case "post-close":
				if c.CloseRPC("current-epoch/files", true, true, nil) != nil {
					t.Fatal("close")
				}
				err = c.ForwardRPC("current-epoch/files", ClientToServer, request, func([]byte) error { t.Fatal("closed connection forwarded"); return nil })
			case "native-input-failure":
				err = c.NativeInput(true, func() error { return io.ErrClosedPipe })
			case "native-output-failure":
				err = c.NativeOutput(func() error { return io.ErrClosedPipe })
			}
			if err == nil {
				t.Fatal("invalid evidence accepted")
			}
			if _, err = c.Finish(); err == nil {
				t.Fatal("failed evidence sealed")
			}
		})
	}
}

type shortWriter struct{}

func (shortWriter) Write(raw []byte) (int, error) { return len(raw) - 1, nil }
