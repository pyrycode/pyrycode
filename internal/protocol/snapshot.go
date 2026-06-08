package protocol

import "time"

// Screen-snapshot v2 wire payloads. These are the request/response pair for
// the always-available, parser-independent screen snapshot — the floor of
// ADR 025's safe-degradation strategy (docs/protocol-mobile.md § Screen
// snapshot). The phone may ask for a one-shot text picture of the current
// claude screen at any time; because the snapshot depends on no screen
// parser it survives any parser break and backs the stall fallback.
//
// This file is wire vocabulary only: pure structs and their
// (de)serialization. The interception of request_snapshot at the v2 dispatch
// boundary, the render via tui-driver, and the push of screen_snapshot back
// live in the consumer (the screen-snapshot handler child), NOT here.
//
// No field carries omitempty: every field is always present on the wire so
// the testdata fixtures pin the full shape and boundary values like an empty
// conversation_id or a zero ts do not silently vanish.

// RequestSnapshotPayload is the body of an Envelope whose Type ==
// TypeRequestSnapshot (docs/protocol-mobile.md § Screen snapshot). Phone →
// binary direction; an on-demand request for a one-shot text picture of the
// current screen.
//
// This is an inbound v2 *control* envelope, structurally like
// TypeRekeyRequest: the v2 session manager intercepts it at the dispatch
// boundary before dispatch.Route is called. There is NO dispatch.Route
// handler for it — the interception, render, and push are the consumer
// ticket's job, so the next reader should not look for a handler that isn't
// there.
type RequestSnapshotPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ScreenSnapshotPayload is the body of an Envelope whose Type ==
// TypeScreenSnapshot (docs/protocol-mobile.md § Screen snapshot). Binary →
// phone direction; the one-shot text picture answering a request_snapshot.
//
// Text is plain rendered text only, NEVER raw terminal control codes. This
// preserves ADR 025's no-raw-bytes invariant and the substrate seal: the
// snapshot is a literal-screen picture rendered to text, not a stream of
// escape sequences. TS records when the snapshot was rendered; it is a
// time.Time whose monotonic-clock reading strips on JSON marshal, so callers
// MUST compare it with time.Time.Equal, never == or reflect.DeepEqual.
type ScreenSnapshotPayload struct {
	ConversationID string    `json:"conversation_id"`
	Text           string    `json:"text"`
	TS             time.Time `json:"ts"`
}
