# Protocol boundaries

Round-trip tests must marshal the decoded payload back into the envelope. Comparing
an untouched raw payload with itself does not check struct tags or new fields.
Explicitly assert decoded field values. Distinct fixture values detect accidental
field swaps. To distinguish an absent key from a present null or empty string,
inspect the JSON produced by marshalling the decoded DTO too; decoded zero
values alone cannot prove a required key was emitted.

Pin public request keys in raw JSON, independently of production DTOs. A test
and handler can agree on the same wrong struct tag while the required client
key is ignored and an optional guard acts unbound. For `accepted_item_id`,
`TestQueueCommandItemBinding` uses literal requests for both Drop and Send now;
`TestQueueActionItemBindingWire` separately checks encoding. Range tests must
make every other predicate pass: bind the target to the supplied out-of-range
ID so identity mismatch cannot hide missing numeric validation, then test the
maximum valid identity and first invalid identity through each action.

A failed `json.Unmarshal` can leave earlier valid target fields populated.
Duplicate keys can reproduce this: a valid queue id followed by a wrong-typed
queue id returns an error while retaining the first value. Rejection tests must
target a live entry and assert no action, write, change notification, terminal
callback or wire output; assuming zero values would miss the bypass. Exercise
malformed unbound requests separately from invalid bindings so one early-return
branch cannot mask the other. See `TestQueueBindingDecodeFailure` and the
authenticated `TestV2Session_DequeueMessage_DecodeFailureIsInert`.

For optional pointer fields, check omission on the original wire bytes too:
both a missing key and JSON `null` decode to nil, and re-marshalling hides an
incorrect explicit null. For encrypted delivery, expose the authenticated
decrypted bytes rather than assert only the decoded envelope.
`TestV2Session_Reconnect_HistoryEntryID` uses this seam to pin omitted history
metadata and distinct durable/ring ids; see [envelope identities](protocol-package-types-envelope.md#replay-cursors-and-durable-read-marks).

The current protocol test helper uses `json.Compact`, which removes whitespace but
preserves key order; do not describe it as sorting keys. It also does not normalise
string escaping — `encoding/json.Marshal` escapes a literal `<`, `>` or `&` byte to
its `\u00XX` form, and `json.Compact` leaves a fixture's own bytes exactly as
written, so a fixture carrying hostile-looking markup must already spell those three
characters as `\u00XX` or a byte-exact round-trip fails for a reason that looks like
a struct-tag bug rather than an unescaped fixture.

An aggregate envelope-fit test and a producer's single-field limit prove different
things. Reference the constant in the package that owns it. A second literal in
another package can remain green after the real limit changes.

Derive hostile fixtures from the producer's actual accepted boundary and encode
the complete envelope, including source/correlation metadata and its clear.
Raw question input below a 16384-byte cap can expand beyond the envelope cap when
`<` becomes `\u003c`; an owner-rejection test alone would stay green while supported
producer output is unretainable. `TestParseEnvelopeBound` binary-searches acceptance
for literal `<`, `>` and `&` in each content field and a four-question/four-option
batch. It checks the largest accepted input and first rejected input, asserts that
the raw guard did not mask encoded rejection, and marshals actual shown/clear
envelopes with worst-case metadata. `TestControlLiveQuestionEnvelopeBound` is
enabled: it checks accepted-boundary owner admission and authenticated answer/refusal
through the original batch ID, with repeated snapshots and one-shot resolution.
`TestParseEnvelopeBoundaryRegistryIdentity` checks lookup/snapshot identity and
content retention at the boundary. See
[question producer bounds](questionbridge-package.md#bounds) and
[daemon envelope admission](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#control-and-on-demand-readings).

An outer `Encoder.SetEscapeHTML(false)` does not prevent a protocol value's custom
`MarshalJSON` from pre-escaping its strings via `json.Marshal`. A boundary fixture
must contain literal HTML-sensitive characters in the raw tool input to expose
sixfold expansion; already escaped input can reach the raw cap first and leave
the encoded guard untested. `testEscapingQuestionInput` restores those literals
after marshalling its protocol-shaped fixture (#3109).

Compare a decoded payload whole, with `reflect.DeepEqual` against a literal expected
struct, rather than field by field. That also catches a nil-vs-`[]` difference in a
slice field that field-by-field equality checks can miss. Print the mismatch with
`%#v`: `%v` and `%+v` both render a nil slice and an empty one as `[]` and hide the
distinction being tested.

Check every field before widening a provenance or logging claim. Metadata derived
by the daemon does not make an echoed client identifier daemon-authored. A queue's
client-visible text can differ from the delivered prompt, which may contain a host
path. Use the client-safe projection when persisting or serving conversation history.

When declaring a frame ahead of its producer, record that state in both the protocol
table row and the section text. Update both when emission lands. Review current group
counts and their other mentions, using the last correction as a locator. Leave dated
changelog statements historical.

[Development verification overview](development-verification.md).
