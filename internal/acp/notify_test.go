package acp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// notifyTransport builds a Transport whose writer is buf. The reader is empty —
// Notify never reads — and Serve is not started, so Notify is exercised in
// isolation against the shared writeMessage seam.
func notifyTransport(buf *bytes.Buffer) *Transport {
	return New(strings.NewReader(""), buf, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestNotify_WritesNotificationWithoutID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tr := notifyTransport(&buf)

	type payload struct {
		Foo string `json:"foo"`
	}
	if err := tr.Notify("session/update", payload{Foo: "bar"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output not valid JSON: %q: %v", buf.String(), err)
	}
	// A notification is method + params only: no id, no result, no error.
	for _, k := range []string{"id", "result", "error"} {
		if _, ok := m[k]; ok {
			t.Errorf("notification carries %q: %v", k, m)
		}
	}
	if m["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", m["jsonrpc"])
	}
	if m["method"] != "session/update" {
		t.Errorf("method = %v, want session/update", m["method"])
	}
	params, ok := m["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing or wrong type: %v", m["params"])
	}
	if params["foo"] != "bar" {
		t.Errorf("params.foo = %v, want bar", params["foo"])
	}
}

func TestNotify_NilParamsOmitsKey(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tr := notifyTransport(&buf)

	if err := tr.Notify("session/idle", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output not valid JSON: %q: %v", buf.String(), err)
	}
	if _, ok := m["params"]; ok {
		t.Errorf("nil params should omit the key: %v", m)
	}
	if _, ok := m["id"]; ok {
		t.Errorf("notification carries an id: %v", m)
	}
	if m["method"] != "session/idle" {
		t.Errorf("method = %v, want session/idle", m["method"])
	}
}

func TestNotify_MarshalFailureWritesNothing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tr := notifyTransport(&buf)

	// A channel value cannot be marshalled by encoding/json.
	err := tr.Notify("session/update", make(chan int))
	if err == nil {
		t.Fatal("Notify: want error for unmarshalable params, got nil")
	}
	if buf.Len() != 0 {
		t.Errorf("marshal failure wrote a partial frame: %q", buf.String())
	}
}
