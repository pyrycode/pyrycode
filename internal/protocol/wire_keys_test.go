package protocol

import (
	"encoding/json"
	"testing"
)

func assertWireKeys(t *testing.T, b []byte, want ...string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal to key map: %v", err)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q", k)
		}
	}
	wanted := make(map[string]bool, len(want))
	for _, k := range want {
		wanted[k] = true
	}
	for k := range got {
		if !wanted[k] {
			t.Errorf("unexpected wire key %q — the key set is the published contract", k)
		}
	}
}
