package protocol

import (
	"encoding/json"
	"testing"
)

func TestStopBackgroundTaskPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "stop_background_task.json")
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != TypeStopBackgroundTask {
		t.Fatalf("type = %q", env.Type)
	}
	var payload StopBackgroundTaskPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := StopBackgroundTaskPayload{ConversationID: "REMOTE_CONVERSATION_2791", TaskID: "REMOTE_TASK_2791"}
	if payload != want {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
	roundTripEnvelope(t, env, payload, raw)
}
