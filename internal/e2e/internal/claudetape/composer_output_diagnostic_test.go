package claudetape

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestComposerControlResponseDiagnostic(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/claude-tapes/composer-stop.json")
	if err != nil {
		t.Fatal(err)
	}
	var suite LifecycleSuite
	if json.Unmarshal(raw, &suite) != nil {
		t.Fatal("reviewed Composer suite unavailable")
	}
	ordinal := 0
	var response []byte
	for _, frame := range suite.Roles["bootstrap"].Tape.Frames {
		if frame.Direction != "stdout" {
			continue
		}
		if ordinal == 68 {
			response = frame.Data
			break
		}
		ordinal++
	}
	var header struct{ Type string }
	if len(response) == 0 || json.Unmarshal(response, &header) != nil || header.Type != "control_response" {
		t.Fatal("recovered forwarding boundary changed")
	}
	home := "/tmp/composer-output-diagnostic"
	binding := LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: home + "/work"}
	p, err := NewPhysicalNativeObserver(context.Background(), home, "composer", binding)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, stage := range []string{"unique", "privacy", "envelope", "compact", "parser", "observer"} {
		start := time.Now()
		switch stage {
		case "unique":
			if !uniqueSuiteKeys(response) {
				t.Fatal("reviewed response has ambiguous keys")
			}
		case "privacy":
			err = safeCLIJSON(response)
		case "envelope":
			var envelope map[string]json.RawMessage
			err = json.Unmarshal(response, &envelope)
		case "compact":
			var compact bytes.Buffer
			err = json.Compact(&compact, response)
		case "parser":
			_, err = p.parser.Write(append(append([]byte(nil), response...), '\n'))
		case "observer":
			err = p.BeforeOutput(response)
		}
		if err != nil {
			t.Fatal("reviewed response rejected")
		}
		t.Logf("stage=%s elapsed_ns=%d bytes=%d", stage, time.Since(start).Nanoseconds(), len(response))
	}
	if len(p.facts) != 0 || len(p.operations) != 0 {
		t.Fatal("control response invented physical evidence")
	}
}
