package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

const testHostedAppID = "22222222-2222-4222-8222-222222222222"

// testHostedExamples reads the normative JSON fences without changing their keys.
func testHostedExamples(t *testing.T, heading string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("../../docs/specs/architecture/3120-hosted-app-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("missing example %q", heading)
	}
	_, section, ok = strings.Cut(section, "```json\n")
	if !ok {
		t.Fatal("missing JSON fence")
	}
	fixture, _, ok := strings.Cut(section, "\n```")
	if !ok {
		t.Fatal("unterminated JSON fence")
	}
	if strings.HasPrefix(fixture, "[") {
		var frames []json.RawMessage
		if err := json.Unmarshal([]byte(fixture), &frames); err != nil {
			t.Fatal(err)
		}
		return frames
	}
	return []json.RawMessage{json.RawMessage(fixture)}
}

func testHostedKeys(t *testing.T, raw []byte, keys ...string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(keys) {
		t.Fatalf("keys: got %s, want %v", raw, keys)
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing %q in %s", key, raw)
		}
	}
	return fields
}

func testHostedJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func testHostedRoundTrip[T any](t *testing.T, raw []byte, typ string, id uint64, reply *uint64, want T) {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != typ || env.ID != id || !reflect.DeepEqual(env.InReplyTo, reply) {
		t.Fatalf("envelope: got %#v, want type %s id %d reply %v", env, typ, id, reply)
	}
	var payload T
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload: got %#v, want %#v", payload, want)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.Payload = encoded
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(testHostedJSON(t, raw), testHostedJSON(t, out)) {
		t.Fatalf("round trip: got %s, want %s", out, raw)
	}
	// The exact envelope key set rules out conversation/session/replay metadata
	// and payload_encrypted on both original and re-encoded bytes.
	keys := []string{"id", "type", "ts", "payload"}
	if reply != nil {
		keys = append(keys, "in_reply_to")
	}
	for _, frame := range [][]byte{raw, out} {
		fields := testHostedKeys(t, frame, keys...)
		testHostedPayloadKeys(t, typ, fields["payload"])
	}
}

func testHostedPayloadKeys(t *testing.T, typ string, raw []byte) {
	t.Helper()
	switch typ {
	case TypeListApps:
		var p ListAppsPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		keys := []string{}
		if p.Cursor != "" {
			keys = append(keys, "cursor")
		}
		testHostedKeys(t, raw, keys...)
	case TypeApps:
		fields := testHostedKeys(t, raw, "revision", "items", "next_cursor")
		if bytes.Equal(fields["items"], []byte("null")) {
			t.Fatal("items must be an array")
		}
		var records []json.RawMessage
		if err := json.Unmarshal(fields["items"], &records); err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			testHostedPayloadKeys(t, TypeAppUpdated, record)
		}
	case TypeAppUpdated:
		fields := testHostedKeys(t, raw, "app_id", "title", "desired", "state", "active_release", "pending_release", "last_error", "revision")
		if !bytes.Equal(fields["last_error"], []byte("null")) {
			testHostedKeys(t, fields["last_error"], "code", "message")
		}
	case TypeAppRemoved:
		testHostedKeys(t, raw, "app_id", "revision")
	case TypeAppCancel, TypeAppCancelled:
		var p AppCancelPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		keys := []string{"request_id"}
		if p.AppID != "" {
			keys = append(keys, "app_id")
		}
		testHostedKeys(t, raw, keys...)
	case TypeError:
		testHostedKeys(t, raw, "code", "message", "retryable", "retry_after_s")
	}
}

func TestHostedApps_SharedExamples(t *testing.T) {
	t.Parallel()
	release := "1.0.0"
	starting := HostedAppRecord{AppID: testHostedAppID, Title: "Orchestrator", Desired: "available", State: "starting", PendingRelease: &release, Revision: 7}
	running := HostedAppRecord{AppID: testHostedAppID, Title: "Orchestrator", Desired: "available", State: "running", ActiveRelease: &release, Revision: 8}
	discovery := testHostedExamples(t, "Discovery plus lifecycle update, including required nulls:")
	listID := uint64(2)
	t.Run("first-list", func(t *testing.T) { testHostedRoundTrip(t, discovery[0], TypeListApps, 2, nil, ListAppsPayload{}) })
	t.Run("apps", func(t *testing.T) {
		testHostedRoundTrip(t, discovery[1], TypeApps, 2, &listID, AppsPayload{Revision: 7, Items: []HostedAppRecord{starting}})
	})
	t.Run("update", func(t *testing.T) { testHostedRoundTrip(t, discovery[2], TypeAppUpdated, 3, nil, running) })
	cancellation := testHostedExamples(t, "Cancellation of a new in-flight read;")
	cancelID := uint64(7)
	cancel := AppCancelPayload{AppID: testHostedAppID, RequestID: 6}
	t.Run("resource-cancel", func(t *testing.T) { testHostedRoundTrip(t, cancellation[1], TypeAppCancel, 7, nil, cancel) })
	t.Run("resource-cancelled", func(t *testing.T) { testHostedRoundTrip(t, cancellation[2], TypeAppCancelled, 8, &cancelID, cancel) })
	t.Run("removed", func(t *testing.T) {
		raw := testHostedExamples(t, "Removal is host-scoped and does not remove saved data:")[0]
		testHostedRoundTrip(t, raw, TypeAppRemoved, 10, nil, AppRemovedPayload{AppID: testHostedAppID, Revision: 9})
	})
	t.Run("error", func(t *testing.T) {
		raw := testHostedExamples(t, "Bridge error is distinct from HTTP failure:")[1]
		requestID, retry := uint64(8), 1
		testHostedRoundTrip(t, raw, TypeError, 9, &requestID, ErrorPayload{Code: "app.busy", Message: "app request capacity is full", Retryable: true, RetryAfterS: &retry})
	})
	// These fixtures assert DTO vocabulary only, not a live capability intersection.
	negotiation := testHostedExamples(t, "Negotiation (hello travels")
	helloID := uint64(1)
	t.Run("hello", func(t *testing.T) {
		testHostedRoundTrip(t, negotiation[0], TypeHello, 1, nil, HelloClientPayload{Role: "client", DeviceName: "Example phone", ClientVersion: "pyrycode-mobile/1.0.0", ProtocolVersions: []string{"v2"}, Capabilities: []string{CapabilityHostedAppsV1, "future_example"}, Token: "synthetic-example-token"})
	})
	t.Run("hello-ack", func(t *testing.T) {
		testHostedRoundTrip(t, negotiation[1], TypeHelloAck, 1, &helloID, HelloAckPayload{ProtocolVersion: "v2", ServerID: "11111111-1111-4111-8111-111111111111", ConnID: "example-connection", Capabilities: []string{CapabilityHostedAppsV1}})
	})
}

func TestHostedApps_AdditionalShapes(t *testing.T) {
	t.Parallel()
	cursor, active, pending := "opaque-next-page", "1.0.0", "1.0.1"
	record := HostedAppRecord{AppID: testHostedAppID, Title: "Orchestrator", Desired: "available", State: "running", ActiveRelease: &active, PendingRelease: &pending, Revision: 11}
	record.LastError = &struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "app.io_failed", Message: "candidate start failed"}
	cases := []struct {
		name, typ, payload string
		want               any
		reply              *uint64
	}{
		{"cursor-request", TypeListApps, `{"cursor":"opaque-next-page"}`, ListAppsPayload{Cursor: cursor}, nil},
		{"empty-page", TypeApps, `{"revision":0,"items":[],"next_cursor":null}`, AppsPayload{Items: []HostedAppRecord{}}, testHostedUint(0)},
		{"next-page", TypeApps, `{"revision":12,"items":[],"next_cursor":"opaque-next-page"}`, AppsPayload{Revision: 12, Items: []HostedAppRecord{}, NextCursor: &cursor}, testHostedUint(4)},
		{"record-error", TypeAppUpdated, `{"app_id":"` + testHostedAppID + `","title":"Orchestrator","desired":"available","state":"running","active_release":"1.0.0","pending_release":"1.0.1","last_error":{"code":"app.io_failed","message":"candidate start failed"},"revision":11}`, record, nil},
		{"list-cancel", TypeAppCancel, `{"request_id":2}`, AppCancelPayload{RequestID: 2}, nil},
		{"list-cancelled", TypeAppCancelled, `{"request_id":2}`, AppCancelPayload{RequestID: 2}, testHostedUint(5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := testHostedFrame(tc.typ, 6, tc.reply, tc.payload)
			switch want := tc.want.(type) {
			case ListAppsPayload:
				testHostedRoundTrip(t, raw, tc.typ, 6, tc.reply, want)
			case AppsPayload:
				testHostedRoundTrip(t, raw, tc.typ, 6, tc.reply, want)
			case HostedAppRecord:
				testHostedRoundTrip(t, raw, tc.typ, 6, tc.reply, want)
			case AppCancelPayload:
				testHostedRoundTrip(t, raw, tc.typ, 6, tc.reply, want)
			default:
				t.Fatalf("unexpected fixture type %T", want)
			}
		})
	}
}

func testHostedUint(n uint64) *uint64 { return &n }

func testHostedFrame(typ string, id uint64, reply *uint64, payload string) []byte {
	correlation := ""
	if reply != nil {
		correlation = fmt.Sprintf(`,"in_reply_to":%d`, *reply)
	}
	return []byte(fmt.Sprintf(`{"id":%d,"type":%q,"ts":"2026-10-10T18:00:00Z"%s,"payload":%s}`, id, typ, correlation, payload))
}

func TestHostedApps_ExactIntegers(t *testing.T) {
	t.Parallel()
	for _, n := range []uint64{0, 9007199254740991} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			reply := testHostedUint(n)
			raw := testHostedFrame(TypeApps, n, reply, fmt.Sprintf(`{"revision":%d,"items":[],"next_cursor":null}`, n))
			testHostedRoundTrip(t, raw, TypeApps, n, reply, AppsPayload{Revision: n, Items: []HostedAppRecord{}})
			for _, appID := range []string{"", testHostedAppID} {
				payload := fmt.Sprintf(`{"request_id":%d}`, n)
				if appID != "" {
					payload = fmt.Sprintf(`{"app_id":%q,"request_id":%d}`, appID, n)
				}
				for _, typ := range []string{TypeAppCancel, TypeAppCancelled} {
					correlation := (*uint64)(nil)
					if typ == TypeAppCancelled {
						correlation = reply
					}
					testHostedRoundTrip(t, testHostedFrame(typ, n, correlation, payload), typ, n, correlation, AppCancelPayload{AppID: appID, RequestID: n})
				}
			}
		})
	}
	for _, n := range []uint64{1, 9007199254740991} {
		t.Run("revision-"+fmt.Sprint(n), func(t *testing.T) {
			record := HostedAppRecord{AppID: testHostedAppID, Title: "Unpublished", Desired: "stopped", State: "stopped", Revision: n}
			payload := fmt.Sprintf(`{"app_id":%q,"title":"Unpublished","desired":"stopped","state":"stopped","active_release":null,"pending_release":null,"last_error":null,"revision":%d}`, testHostedAppID, n)
			testHostedRoundTrip(t, testHostedFrame(TypeAppUpdated, 0, nil, payload), TypeAppUpdated, 0, nil, record)
			payload = fmt.Sprintf(`{"app_id":%q,"revision":%d}`, testHostedAppID, n)
			testHostedRoundTrip(t, testHostedFrame(TypeAppRemoved, 0, nil, payload), TypeAppRemoved, 0, nil, AppRemovedPayload{AppID: testHostedAppID, Revision: n})
		})
	}
}

func TestAppsPayload_NilItemsEncodeAsArray(t *testing.T) {
	t.Parallel()
	p := AppsPayload{}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	fields := testHostedKeys(t, raw, "revision", "items", "next_cursor")
	if string(fields["items"]) != "[]" || string(fields["next_cursor"]) != "null" {
		t.Fatalf("zero page: %s", raw)
	}
	if p.Items != nil {
		t.Fatal("marshal mutated the payload")
	}
}
