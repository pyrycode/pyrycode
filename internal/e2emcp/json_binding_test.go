package e2emcp

import (
	"bytes"
	"testing"
)

func TestJSONStringBindingPreservesAllUnboundNativeBytes(t *testing.T) {
	raw := []byte(` { "unknown": {"literal":"old-id <>&", "escaped":"\u003c", "ordered": [3, 1]}, "result" : { "isError" : false, "content" : [ {"type":"text", "text" : "the file was handed to the operator (attachment id old-id)", "same":"old-id"} ] }, "id":"literal-id", "jsonrpc":"2.0" } `)
	old := "the file was handed to the operator (attachment id old-id)"
	fresh := "the file was handed to the operator (attachment id fresh-id)"
	got, err := BindJSONString(raw, []string{"result", "content", "0", "text"}, old, fresh)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.Replace(raw, []byte(`"`+old+`"`), []byte(`"`+fresh+`"`), 1)
	if !bytes.Equal(got, expected) {
		t.Fatal("binding changed an unbound byte or unknown field")
	}
	got[0] = 'x'
	if raw[0] == 'x' {
		t.Fatal("binding aliases immutable source")
	}
	same, err := BindJSONString(raw, []string{"unknown", "escaped"}, "<", "<")
	if err != nil || !bytes.Equal(same, raw) {
		t.Fatal("same-value binding changed original escape spelling")
	}
}
func TestJSONStringBindingRejectsUnmatchedOrAmbiguousPaths(t *testing.T) {
	raw := []byte(`{"result":{"content":[{"text":"current"}]}}`)
	for _, path := range [][]string{nil, {"result", "missing"}, {"result", "content", "00", "text"}, {"result", "content", "-1", "text"}, {"result", "content", "1", "text"}, {"result", "content", "0"}, {"result", "content", "0", "text", "nested"}} {
		if _, err := BindJSONString(raw, path, "current", "replacement"); err == nil {
			t.Fatal("unmatched or non-string binding accepted")
		}
	}
	if _, err := BindJSONString(raw, []string{"result", "content", "0", "text"}, "foreign", "replacement"); err == nil {
		t.Fatal("foreign source scalar rebound")
	}
	duplicate := []byte(`{"result":{"content":[{"text":"current","text":"foreign"}]}}`)
	if _, err := BindJSONString(duplicate, []string{"result", "content", "0", "text"}, "current", "replacement"); err == nil {
		t.Fatal("ambiguous source rebound")
	}
}

func TestJSONStringBindingDoesNotRepairInvalidReplacementBytes(t *testing.T) {
	raw := []byte(`{"text":"current"}`)
	if got, err := BindJSONString(raw, []string{"text"}, "current", string([]byte{0xff})); err == nil || got != nil {
		t.Fatal("invalid replacement bytes repaired")
	}
}
