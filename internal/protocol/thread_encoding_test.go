package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testThreadEncode(t *testing.T, env Envelope, update any) [][]byte {
	t.Helper()
	parts, err := EncodeThreadUpdate(env, update)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range parts {
		if len(b) > MaxThreadEnvelopeBytes || !json.Valid(b) {
			t.Fatalf("invalid envelope size %d", len(b))
		}
	}
	return parts
}

func testThreadLogical(t *testing.T, parts [][]byte) []byte {
	t.Helper()
	var logical strings.Builder
	for i, b := range parts {
		var env Envelope
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatal(err)
		}
		var p ThreadUpdatePart
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Continuation == nil {
			if len(parts) != 1 {
				t.Fatal("ordinary multi-part update")
			}
			return env.Payload
		}
		c := p.Continuation
		if c.Index != uint64(i) || c.Offset != uint64(logical.Len()) || c.Final != (i == len(parts)-1) || !utf8.ValidString(p.Data) || p.Data == "" {
			t.Fatal("bad fragment progress")
		}
		logical.WriteString(p.Data)
		if i == len(parts)-1 {
			sum := sha256.Sum256([]byte(env.Type + "\x00" + logical.String()))
			if c.TotalBytes != uint64(logical.Len()) || c.UpdateID != hex.EncodeToString(sum[:]) {
				t.Fatal("bad assembly identity")
			}
		}
	}
	return []byte(logical.String())
}

func testThreadValue(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestThreadUpdateRoundTrip(t *testing.T) {
	t.Parallel()
	hostile := "\x00\b\f\n\r\t\"\\<>&\u2028\u2029🌲"
	large := strings.Repeat(hostile, MaxThreadEnvelopeBytes/len(hostile)*2)
	raw, _ := json.Marshal(map[string]any{"text": large, "unknown": map[string]any{"text": large, "n": json.Number("9007199254740991"), "clear": nil}})
	item := ThreadItem{ID: math.MaxUint64, Kind: "assistant_message", Order: math.MaxUint64, Rev: math.MaxUint64, EndedOrder: math.MaxUint64, Session: hostile, Agent: hostile, NoChild: true, Turn: hostile, Parent: math.MaxUint64, Status: hostile, Active: true, Shown: true, Summary: hostile, Subtype: hostile, Content: raw}
	cases := []struct {
		name   string
		update any
		split  bool
	}{
		{"small addition", ThreadItemAddedPayload{"conv", "epoch", 7, ThreadItem{ID: 1, Kind: "user_message", Rev: 7, Content: json.RawMessage(`{"text":"hi"}`)}}, false},
		{"small change", ThreadItemChangedPayload{"conv", "epoch", 8, 1, 7, 8, map[string]json.RawMessage{"summary": json.RawMessage(`""`)}}, false},
		{"small append", ThreadTextAppendPayload{"conv", "epoch", 9, 1, 8, 9, "suffix"}, false},
		{"assistant", ThreadItemAddedPayload{hostile, hostile, math.MaxUint64, item}, true},
		{"patch", ThreadItemChangedPayload{hostile, hostile, math.MaxUint64, 2, 1, math.MaxUint64, map[string]json.RawMessage{"summary": json.RawMessage(`""`), "active": json.RawMessage(`false`), "parent": json.RawMessage(`0`), "session": json.RawMessage(`null`), "content": raw}}, true},
		{"suffix", ThreadTextAppendPayload{hostile, hostile, math.MaxUint64, 3, math.MaxUint64 - 1, math.MaxUint64, large}, true},
	}
	for _, kind := range []string{"user_message", "tool_call", "agent", "unknown_kind"} {
		copy := item
		copy.Kind = kind
		cases = append(cases, struct {
			name   string
			update any
			split  bool
		}{kind, ThreadItemAddedPayload{hostile, hostile, math.MaxUint64, copy}, true})
	}
	// Each variable item field can exceed the cap independently of content.
	for _, field := range []string{"Kind", "Session", "Agent", "Turn", "Status", "Summary", "Subtype"} {
		copy := item
		copy.Content = json.RawMessage(`{}`)
		reflect.ValueOf(&copy).Elem().FieldByName(field).SetString(large)
		cases = append(cases, struct {
			name   string
			update any
			split  bool
		}{field, ThreadItemAddedPayload{hostile, hostile, math.MaxUint64, copy}, true})
	}
	session, _ := json.Marshal(hostile)
	env := Envelope{ID: 4, TS: time.Now().UTC(), SessionID: session}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.update)
			parts := testThreadEncode(t, env, tc.update)
			if (len(parts) > 1) != tc.split {
				t.Fatalf("parts=%d", len(parts))
			}
			got := testThreadLogical(t, parts)
			if !reflect.DeepEqual(testThreadValue(t, got), testThreadValue(t, before)) {
				t.Fatal("logical fields changed")
			}
			after, _ := json.Marshal(tc.update)
			if !bytes.Equal(before, after) || !bytes.Equal(session, env.SessionID) {
				t.Fatal("input mutated")
			}
			again := testThreadEncode(t, env, tc.update)
			if !reflect.DeepEqual(parts, again) {
				t.Fatal("non-deterministic detached encoding")
			}
			if !tc.split {
				var e Envelope
				_ = json.Unmarshal(parts[0], &e)
				e.Payload = before
				ordinary, _ := json.Marshal(e)
				if !bytes.Equal(ordinary, parts[0]) || bytes.Contains(e.Payload, []byte(`"continuation"`)) {
					t.Fatal("ordinary wire shape changed")
				}
			}
		})
	}
}

func testThreadMaxEnvelope() Envelope {
	n := uint64(math.MaxUint64)
	return Envelope{ID: n, TS: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -86340)), InReplyTo: &n, EventID: &n, HistoryEntryID: &n, SessionID: json.RawMessage(`"\u0000\u003c\u2028"`), SessionStateCleared: true, PayloadEncrypted: true}
}

func TestThreadUpdateBudgets(t *testing.T) {
	t.Parallel()
	env := testThreadMaxEnvelope()
	for _, kind := range []string{TypeThreadItemAdded, TypeThreadItemChanged, TypeThreadTextAppend} {
		t.Run(kind, func(t *testing.T) {
			makeUpdate := func(n int) any {
				text := strings.Repeat("x", n)
				switch kind {
				case TypeThreadItemAdded:
					return ThreadItemAddedPayload{"c", "e", math.MaxUint64, ThreadItem{ID: math.MaxUint64, Rev: math.MaxUint64, Summary: text, Content: json.RawMessage(`null`)}}
				case TypeThreadItemChanged:
					b, _ := json.Marshal(text)
					return ThreadItemChangedPayload{"c", "e", math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, map[string]json.RawMessage{"summary": b}}
				default:
					return ThreadTextAppendPayload{"c", "e", math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, text}
				}
			}
			p, _ := json.Marshal(makeUpdate(0))
			measure := env
			measure.Type = kind
			measure.Payload = p
			b, _ := json.Marshal(measure)
			n := MaxThreadEnvelopeBytes - len(b)
			exact := testThreadEncode(t, env, makeUpdate(n))
			if len(exact) != 1 || len(exact[0]) != MaxThreadEnvelopeBytes {
				t.Fatalf("exact fit: %d parts, %d bytes", len(exact), len(exact[0]))
			}
			split := testThreadEncode(t, env, makeUpdate(n+1))
			if len(split) < 2 {
				t.Fatal("one byte over did not split")
			}
			for _, part := range split {
				var e Envelope
				_ = json.Unmarshal(part, &e)
				e.ID = math.MaxUint64
				e.TS = env.TS
				e.InReplyTo = env.InReplyTo
				e.EventID = env.EventID
				e.HistoryEntryID = env.HistoryEntryID
				e.SessionStateCleared = true
				e.PayloadEncrypted = true
				var f ThreadUpdatePart
				_ = json.Unmarshal(e.Payload, &f)
				f.Continuation.Index = math.MaxUint64
				f.Continuation.Offset = math.MaxUint64
				f.Continuation.TotalBytes = math.MaxUint64
				f.Continuation.Final = false
				e.Payload, _ = json.Marshal(f)
				stamped, _ := json.Marshal(e)
				if len(stamped) > MaxThreadEnvelopeBytes {
					t.Fatal("stamp/progress reservation exceeded")
				}
			}
			var e Envelope
			_ = json.Unmarshal(split[0], &e)
			var f ThreadUpdatePart
			_ = json.Unmarshal(e.Payload, &f)
			f.Data = ""
			f.Continuation.Index = math.MaxUint64
			f.Continuation.Offset = math.MaxUint64
			f.Continuation.TotalBytes = math.MaxUint64
			f.Continuation.Final = false
			e.Payload, _ = json.Marshal(f)
			overhead, _ := json.Marshal(e)
			t.Logf("ordinary overhead=%d, reserved continuation overhead=%d, data budget=%d", len(b), len(overhead), MaxThreadEnvelopeBytes-len(overhead))
		})
	}
	// Reservations also hold when the caller supplied no optional numeric/boolean stamps.
	minimal := testThreadEncode(t, Envelope{}, ThreadTextAppendPayload{"c", "e", 7, 1, 6, 7, strings.Repeat("\x00🌲", MaxThreadEnvelopeBytes)})
	for _, b := range minimal {
		var e Envelope
		_ = json.Unmarshal(b, &e)
		e.ID = env.ID
		e.TS = env.TS
		e.InReplyTo = env.InReplyTo
		e.EventID = env.EventID
		e.HistoryEntryID = env.HistoryEntryID
		e.SessionStateCleared = true
		e.PayloadEncrypted = true
		stamped, _ := json.Marshal(e)
		if len(stamped) > MaxThreadEnvelopeBytes {
			t.Fatal("late stamps exceeded cap")
		}
	}
}

func TestThreadUpdateErrors(t *testing.T) {
	t.Parallel()
	meta := testThreadMaxEnvelope()
	meta.Type, meta.SessionID = TypeThreadTextAppend, json.RawMessage(`""`)
	base := uint64(2)
	meta.Payload, _ = json.Marshal(ThreadUpdatePart{ConversationID: "c", Epoch: "e", Version: 3, ItemID: 1, BaseRev: &base, Rev: 3, Continuation: &ThreadContinuation{UpdateID: strings.Repeat("0", 64), Index: math.MaxUint64, Offset: math.MaxUint64, TotalBytes: math.MaxUint64}})
	overhead, _ := json.Marshal(meta)
	for _, room := range []int{0, 1} {
		meta.SessionID, _ = json.Marshal(strings.Repeat("x", MaxThreadEnvelopeBytes-len(overhead)-room))
		parts, err := EncodeThreadUpdate(meta, ThreadTextAppendPayload{"c", "e", 3, 1, 2, 3, strings.Repeat("x", MaxThreadEnvelopeBytes*2)})
		if !errors.Is(err, ErrThreadUpdateMetadataTooLarge) || parts != nil {
			t.Fatalf("room=%d: error=%v parts=%d", room, err, len(parts))
		}
	}
	secret := "/private/SENTINEL-secret-thread-content"
	large := strings.Repeat(secret, MaxThreadEnvelopeBytes/len(secret)+1)
	valid := ThreadTextAppendPayload{"c", "e", 3, 1, 2, 3, "ok"}
	cases := []struct {
		name   string
		env    Envelope
		update any
		want   error
	}{
		{"conversation", Envelope{}, ThreadTextAppendPayload{large, "e", 3, 1, 2, 3, "ok"}, ErrThreadUpdateMetadataTooLarge},
		{"epoch", Envelope{}, ThreadTextAppendPayload{"c", large, 3, 1, 2, 3, "ok"}, ErrThreadUpdateMetadataTooLarge},
		{"session", Envelope{SessionID: json.RawMessage(`"` + large + `"`)}, valid, ErrThreadUpdateMetadataTooLarge},
		{"raw JSON", Envelope{}, ThreadItemAddedPayload{Item: ThreadItem{Content: json.RawMessage(secret)}}, ErrInvalidThreadUpdate},
		{"patch JSON", Envelope{}, ThreadItemChangedPayload{Changes: map[string]json.RawMessage{secret: json.RawMessage(`!`)}}, ErrInvalidThreadUpdate},
		{"UTF8", Envelope{}, ThreadTextAppendPayload{Text: string([]byte{0xff})}, ErrInvalidThreadUpdate},
		{"raw UTF8", Envelope{}, ThreadItemAddedPayload{Item: ThreadItem{Content: json.RawMessage{'"', 0xff, '"'}}}, ErrInvalidThreadUpdate},
		{"patch key UTF8", Envelope{}, ThreadItemChangedPayload{Changes: map[string]json.RawMessage{string([]byte{0xff}): json.RawMessage(`0`)}}, ErrInvalidThreadUpdate},
		{"session JSON", Envelope{SessionID: json.RawMessage(secret)}, valid, ErrInvalidThreadUpdate},
		{"session UTF8", Envelope{SessionID: json.RawMessage{'"', 0xff, '"'}}, valid, ErrInvalidThreadUpdate},
		{"time", Envelope{TS: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, valid, ErrInvalidThreadUpdate},
		{"unsupported", Envelope{}, secret, ErrInvalidThreadUpdate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := EncodeThreadUpdate(tc.env, tc.update)
			if !errors.Is(err, tc.want) || parts != nil {
				t.Fatalf("error=%v parts=%d", err, len(parts))
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("content leaked")
			}
		})
	}
}
