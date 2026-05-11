# Spec: `RegisterPushTokenPayload` (#275)

## Context

Phase 3 Track C — push-registration slice of the v1 payload catalog. The framing primitives (`Envelope`, `RoutingEnvelope`, error/type-name consts, `IsV1Compatible`) already live in `internal/protocol/`. This ticket adds the per-type Go struct for the `register_push_token` payload so the future dispatch handler decodes a typed value out of `Envelope.Payload (json.RawMessage)` instead of re-deriving the shape ad hoc.

The shape is fixed verbatim by `docs/protocol-mobile.md` § Message types → `register_push_token`. The example payload in the spec is this ticket's golden-file fixture, end of story. Pure DTO: no I/O, no methods, no constructors.

## Files to read first

- `docs/protocol-mobile.md:480-499` — `register_push_token` subsection. The example envelope (`id 8`, `type "register_push_token"`, fields `platform` / `token` / `device_name`) IS the golden fixture, copied verbatim into testdata. Read this first; everything else is plumbing.
- `internal/protocol/envelope.go:1-30` — package doc + `Envelope` struct. New file lives in the same package and mirrors this file's tone (struct-only, no methods, doc comment that points at the spec).
- `internal/protocol/codes.go:60-62` — `TypeRegisterPushToken = "register_push_token"` already declared. Use this constant in the test (`env.Type == TypeRegisterPushToken`), do **not** hardcode the string.
- `internal/protocol/envelope_test.go:11-27` — `canonical(t, b)` and `readFixture(t, name)` helpers. New test file in the same package reuses these directly; do not redefine.
- `internal/protocol/envelope_test.go:29-67` — `TestEnvelope_RoundTrip_Full`. Mirror this exact shape for the new test (unmarshal full envelope → check fields → re-marshal → `bytes.Equal` of the compacted forms).
- `internal/protocol/testdata/envelope_full.json` — formatting reference for the new fixture (single line, compact, no trailing newline behaviour preserved as-is by `os.ReadFile`).

## Design

### Production code

New file `internal/protocol/push.go`. Single-struct slice, no methods, no constructors.

```go
package protocol

// RegisterPushTokenPayload is the body of a register_push_token frame
// (docs/protocol-mobile.md § register_push_token). Phone → binary, sent
// on every WS connect; the binary persists (platform, token, device_name)
// in devices.json and de-duplicates against the stored triple.
//
// Platform is "fcm" (Android) or "apns" (iOS). The wire type stays a
// plain string: an enum would force a converter at every internal call
// site for no observable wire-format gain, and per-spec the dispatcher
// is the validation point.
//
// All three fields are required (no omitempty, no pointers).
type RegisterPushTokenPayload struct {
	Platform   string `json:"platform"`
	Token      string `json:"token"`
	DeviceName string `json:"device_name"`
}
```

That is the entire production diff. No exported helpers, no `Validate()`, no `String()`, no constructor — the ticket explicitly forbids them, and the dispatcher (future ticket) owns validation.

### Fixture

New file `internal/protocol/testdata/register_push_token.json`. Verbatim copy of the spec's example envelope, compacted to a single line matching the formatting of `envelope_full.json`:

```json
{"id":8,"type":"register_push_token","ts":"2026-05-08T10:33:14.012Z","payload":{"platform":"fcm","token":"f0r...","device_name":"Juhana's Pixel 8"}}
```

Notes:

- `ts` reuses the same timestamp as the other fixtures — there is no semantic constraint on `ts` for this payload, and reusing keeps the testdata corpus internally consistent.
- `payload.token` is the literal `"f0r..."` from the spec. It is a placeholder in the spec and it stays a placeholder here; the wire type does not interpret tokens.
- No `in_reply_to`, no `payload_encrypted` — neither appears in the spec example, and adding either would over-specify what this fixture proves.

### Test

New file `internal/protocol/push_test.go`. Same package as `envelope_test.go`, so it reuses the existing `canonical` and `readFixture` helpers without import.

Test shape (one function, mirroring `TestEnvelope_RoundTrip_Full`):

```go
func TestRegisterPushTokenPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "register_push_token.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRegisterPushToken {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRegisterPushToken)
	}

	var p RegisterPushTokenPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Platform != "fcm" {
		t.Errorf("Platform: got %q, want %q", p.Platform, "fcm")
	}
	if p.Token != "f0r..." {
		t.Errorf("Token: got %q, want %q", p.Token, "f0r...")
	}
	if p.DeviceName != "Juhana's Pixel 8" {
		t.Errorf("DeviceName: got %q, want %q", p.DeviceName, "Juhana's Pixel 8")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
```

Why decode-from-Envelope-Payload rather than decode-from-raw-payload: the AC explicitly requires validating that the payload struct slots into `Envelope.Payload (json.RawMessage)` once the dispatcher reads `Envelope.Type`. Testing the full envelope round-trip exercises that exact path. A standalone payload-only round-trip would not.

Why no separate "unknown field rejection" or "missing field" tests: stdlib `encoding/json` is permissive by default, and the ticket explicitly scopes those concerns out (dispatcher's job). One golden round-trip is the contract.

## Concurrency model

None. Pure data type. No goroutines, no `context.Context`, no channels.

## Error handling

None at this layer. `json.Unmarshal` returns its own errors to callers; `RegisterPushTokenPayload` adds no error semantics. Validation (e.g. rejecting `platform == "windows"`) belongs to the dispatcher, not the wire type.

## Testing strategy

Single golden-file round-trip (defined above). Verifies:

1. The fixture decodes cleanly into `Envelope` (existing primitive, already covered — this re-asserts integration).
2. `Envelope.Payload` decodes cleanly into `RegisterPushTokenPayload`.
3. The decoded payload matches the spec example field-by-field.
4. Re-marshalling the envelope produces bytes that compact-equal the original fixture.

Run locally with:

```bash
go test -race ./internal/protocol/
```

CI's existing `go vet` / `staticcheck` / `go test -race` matrix already covers this package; no workflow changes needed.

## Open questions

None. The spec pins every degree of freedom: field names, types, optionality, fixture content. The only architect-discretion call was file layout (own file vs. fold into an existing file), and the ticket explicitly permits either; `push.go` is the cleaner choice because it keeps the file-to-message-family mapping legible as more payload types land (the next slices will add their own `*.go` files in the same package).
