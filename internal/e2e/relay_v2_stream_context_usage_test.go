//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The arrival proof for #2371, on relay_v2_stream_session_facts_test.go's terms:
// internal/turnbridge's MapEvent gained its turnevent.ContextUsage arm and cmd/pyry's
// interactive v2 emitter pushes the frame, so context_usage flows on the live turn
// lane with nothing proving it ARRIVES. Every layer has its own coverage — the
// parser's construction of the variant, the post-turn ask, the mapper's arm, the
// emitter's push — and nothing drives one solicited reading through all of them to an
// encrypted frame on a connected client.
//
// TWO STRUCTURAL DEPARTURES FROM THAT ANALOGUE, both forced by this frame rather than
// chosen, and each one is why a reader should not "align" this file with it:
//
//  1. THERE IS NO RIDER, SO THERE IS NO RIDER-OFF CONTROL. cmd/pyry's
//     turnEndContextUsageRequester asks unconditionally after every completed turn
//     (#2289) — no env knob gates it — so the negative half that makes the
//     session_facts count meaningful has no analogue here. What replaces it: the two
//     milestones are asserted FIRST and FATALLY, so a count read off a run that never
//     completed cannot pass; the count is EXACTLY one rather than at-least one, which
//     a second emitting source would break; and the ORDER claim below is only
//     satisfiable by a frame that followed the turn, so a frame published from
//     somewhere else on the path would have to reproduce the arrival order too.
//
//  2. THE DRAIN MUST CONTINUE PAST turn_end. The analogue terminates ON turn_end.
//     This frame is SOLICITED BY turn_end and therefore always follows it, so a loop
//     that stopped there would collect zero of them, every time, and read as a
//     regression rather than as a broken test. The loop drains to turn_end, records
//     its ordinal, then keeps draining through a bounded settle window — the idiom
//     relay_v2_stream_slash_command_list_reconcile_test.go uses — which is what makes
//     "exactly one" an exact claim rather than a first sighting.
//
// THE CANNED READING IS DELIBERATELY HOSTILE (fakeclaude's cannedContextUsage): the
// lists are unsorted, the MCP-tool list exceeds streamsup's 32-entry bound, and one
// MCP name and one memory path exceed its 256-byte string bound. What survives is
// MEASURED below rather than assumed, because the three dropped counts are the frame's
// least inferable values.
const (
	contextUsageInitialUUID = "77777777-7777-4777-8777-777777777777"
	contextUsageConvID      = "88888888-8888-4888-8888-888888888888"
	contextUsageUserText    = "e2e-contextusage:hello\n"
	contextUsageEchoNeedle  = "e2e-contextusage:hello"
	contextUsageSendReqID   = uint64(2371)

	// The reading's scalars, transcribed from fakeclaude's cannedContextUsage.
	// Duplicated as literals because fakeclaude is a separate main package this file
	// cannot import — the discipline the session_facts and rate-limit specs follow.
	contextUsageModel      = "claude-fixture-context"
	contextUsageTotal      = 9500
	contextUsageMax        = 200000
	contextUsagePercentage = 5

	// The 317-byte MCP tool name the producer REJECTS whole. Asserting its absence is
	// what proves the entry was dropped rather than truncated: maxContextUsageStringBytes
	// rejects the complete entry instead of inventing a cut name for a client to show.
	contextUsageOverlongToolNeedle = "tool_16"
	// The 325-byte memory path, rejected the same way.
	contextUsageOverlongPathNeedle = "long-fixture-segment-"
)

// The three dropped counts the canned reading produces, MEASURED against streamsup's
// boundContextUsageEntries rather than assumed:
//
//	categories:    4 entries, none over the 256-byte bound  → 4 retained, 0 dropped
//	mcp tools:    33 entries, one 317-byte name rejected     → 32 retained, 1 dropped
//	memory files:  3 entries, one 325-byte path rejected     →  2 retained, 1 dropped
//
// NOTE FOR A LATER READER: #2371's ticket text predicted "three different values, one
// of them zero". Measured, they are 0/1/1 — two distinct values. The assertion still
// earns its place, because the pairs remain independently observable: zero separates
// no-loss from loss, and the two ones are reached from different retained counts (32
// against a full cap, 2 against a nearly empty list). A mapper that cross-wired the
// MCP and memory counts would still pass here, which is why the unit tier carries a
// fixture whose three counts are mutually distinct (3/5/7) and this tier carries the
// live numbers.
const (
	contextUsageWantDroppedCategories  = 0
	contextUsageWantDroppedMCPTools    = 1
	contextUsageWantDroppedMemoryFiles = 1
	contextUsageWantMCPTools           = 32
)

// contextUsageObservation is what one driven turn put on the wire: every context_usage
// frame as RAW payload bytes, the ordinal at which each arrived, the ordinal of the
// turn's turn_end, and the two positive milestones.
//
// The frames are kept RAW rather than decoded because one assertion needs the payload's
// own KEY SET, not its decoded fields. A protocol.ContextUsagePayload cannot answer
// "did a twelfth key arrive": decoding into it discards anything it does not declare,
// which is precisely the thing under test.
//
// The milestones are VALUES rather than helper-side assertions, the analogue's reason
// unchanged: a helper that fataled on them would leave the caller's own milestone
// checks dead code.
type contextUsageObservation struct {
	contextUsage    []json.RawMessage
	contextUsageAt  []int
	turnEndAt       int
	unrecognized    int
	sawEcho         bool
	sawTurnEnd      bool
	receivedOrdinal int
}

// driveContextUsageTurn spawns a stream-interactive daemon, drives one ordinary turn
// from a connected interactive v2 client, and returns what that turn put on the wire —
// including the tail after turn_end, which is where this frame lives.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport and
// decode faults.
func driveContextUsageTurn(t *testing.T) contextUsageObservation {
	t.Helper()

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind the conversation to the bootstrap session so the drain gate passes. This
	// UUID MUST equal the one handed to StartStreamInteractiveWithRelay below: a
	// mismatch drops every event at the gate and hangs the drain for the full
	// deadline, presenting as an unexplained timeout rather than a clean failure.
	seedBoundConversation(t, home, contextUsageConvID, contextUsageInitialUUID)

	// NO EXTRA ENV. The post-turn ask is unconditional, which is the whole reason
	// this spec has no rider-off twin.
	h := StartStreamInteractiveWithRelay(t, home, contextUsageInitialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	// Interactive — and load-bearing, not incidental. This stream is capability-gated:
	// frames reach only a phone whose interactive capability was echoed in hello_ack
	// (docs/protocol-mobile.md § Interactive events (v2, capability-gated)). The
	// non-interactive half of that gate is proven at the unit tier, where a second
	// conn can be constructed without the grant; emit filters once for every frame
	// type, so a second gate is not what this tier is looking for.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendA.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phoneA, ciphertext)
	}

	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phoneA.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone A receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("phone A decode inner frame: %v", err)
			}
			// The receive nonce is sequential, so every noise_msg MUST be decrypted in
			// receive order — filter AFTER decrypting, never before, or the CipherState
			// desyncs and every later decrypt fails with a misleading error.
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvA), true
		}
	}

	sealSend(protocol.Envelope{
		ID:   contextUsageSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: contextUsageConvID,
			MessageID:      "m-contextusage-1",
			Text:           contextUsageUserText,
		}),
	})

	obs := contextUsageObservation{turnEndAt: -1}
	record := func(env protocol.Envelope) {
		obs.receivedOrdinal++
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeContextUsage:
			// Retained VERBATIM, not decoded: the key-set assertion downstream is the
			// only thing that can see a field the payload type does not declare.
			obs.contextUsage = append(obs.contextUsage, append(json.RawMessage(nil), env.Payload...))
			obs.contextUsageAt = append(obs.contextUsageAt, obs.receivedOrdinal)
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
			if obs.turnEndAt < 0 {
				obs.turnEndAt = obs.receivedOrdinal
			}
		case protocol.TypeUnrecognizedMessage:
			obs.unrecognized++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, contextUsageEchoNeedle) {
				obs.sawEcho = true
			}
		}
	}

	// Phase one: drain to the turn's end. The reading is solicited BY that end, so
	// nothing this phase collects can be the frame under test.
	deadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Logf("drain deadline reached before turn_end: context_usage=%d unrecognized=%d echo=%v",
				len(obs.contextUsage), obs.unrecognized, obs.sawEcho)
			return obs
		}
		record(env)
	}

	// Phase two: the settle window, which is what makes the count below an EXACT
	// claim rather than a first sighting. The ask is written as TurnEnd is sunk and
	// fakeclaude answers it inline, so the reply is already in flight here; the
	// window covers the parse and the push, not a poll interval.
	settleDeadline := time.Now().Add(2 * time.Second)
	for {
		env, ok := nextEnv(settleDeadline)
		if !ok {
			break
		}
		record(env)
	}
	return obs
}

// TestRelayV2_StreamContextUsageReachesConnectedPhone is AC 3: one completed turn, and
// the connected interactive v2 client receives EXACTLY ONE context_usage frame, AFTER
// that turn's turn_end, carrying the reading the producer's bounds actually left.
func TestRelayV2_StreamContextUsageReachesConnectedPhone(t *testing.T) {
	obs := driveContextUsageTurn(t)

	// The milestones first and fatally: a count read off a run that never completed
	// proves nothing in either direction.
	if !obs.sawEcho {
		t.Fatalf("the reply never arrived (no assistant_delta carrying %q) — the run did not "+
			"complete, so its counts prove nothing; context_usage=%d unrecognized=%d turn_end=%v "+
			"(a UUID mismatch between seedBoundConversation and StartStreamInteractiveWithRelay "+
			"is the first suspect)",
			contextUsageEchoNeedle, len(obs.contextUsage), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the reply arrived but the turn never closed (no turn_end) — the post-turn ask "+
			"is solicited BY that end, so nothing could have produced the frame; context_usage=%d "+
			"unrecognized=%d", len(obs.contextUsage), obs.unrecognized)
	}
	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0 — the control_response carrying the "+
			"reading reached the parser's fallback instead of its context-usage arm", obs.unrecognized)
	}

	if len(obs.contextUsage) != 1 {
		t.Fatalf("context_usage frames: got %d, want exactly 1 — one completed turn produces one "+
			"post-turn ask, so the producer must emit once and the frame must reach the phone\n%s",
			len(obs.contextUsage), obs.contextUsage)
	}
	// AC 2's ordering claim, observed end to end rather than at the emitter: the frame
	// FOLLOWS the turn it describes. An arm that opened a turn on this event would
	// also have put a turn_state after this ordinal — see the unit tier.
	if obs.contextUsageAt[0] <= obs.turnEndAt {
		t.Errorf("context_usage arrived at ordinal %d, at or before turn_end at %d — the reading is "+
			"solicited by the turn's end and must follow it", obs.contextUsageAt[0], obs.turnEndAt)
	}

	raw := obs.contextUsage[0]
	var p protocol.ContextUsagePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode context_usage payload: %v\n%s", err, raw)
	}

	// The conversation identity is the BRIDGE's contribution: the parser's event
	// carries none, so this proves the mapper supplied it rather than leaving it empty.
	if p.ConversationID != contextUsageConvID {
		t.Errorf("conversation_id: got %q, want %q", p.ConversationID, contextUsageConvID)
	}
	// The scalars are claude's own, crossing four layers unrecomputed. Percentage in
	// particular is NOT derivable from the two totals and must not be recomputed:
	// 9500/200000 is 4.75%, and the reading says 5.
	if p.Model != contextUsageModel {
		t.Errorf("model: got %q, want %q (claude's own label, verbatim)", p.Model, contextUsageModel)
	}
	if p.TotalTokens != contextUsageTotal || p.MaxTokens != contextUsageMax {
		t.Errorf("totals: got (%d,%d), want (%d,%d)", p.TotalTokens, p.MaxTokens,
			contextUsageTotal, contextUsageMax)
	}
	if p.Percentage != contextUsagePercentage {
		t.Errorf("percentage: got %d, want %d — claude's own value, never recomputed from the "+
			"totals (which would give 4)", p.Percentage, contextUsagePercentage)
	}

	assertContextUsageCategories(t, p)
	assertContextUsageMCPTools(t, p)
	assertContextUsageMemoryFiles(t, p)

	// The three counts, MEASURED. Their independence is the property under test: each
	// list's original size must stay recoverable as len(list) + its OWN count.
	if p.DroppedCategories != contextUsageWantDroppedCategories {
		t.Errorf("dropped_categories: got %d, want %d — no category exceeds the producer's bounds, "+
			"so nothing may be reported cut", p.DroppedCategories, contextUsageWantDroppedCategories)
	}
	if p.DroppedMCPTools != contextUsageWantDroppedMCPTools {
		t.Errorf("dropped_mcp_tools: got %d, want %d — 33 tools in, one name over the 256-byte "+
			"bound, 32 retained", p.DroppedMCPTools, contextUsageWantDroppedMCPTools)
	}
	if p.DroppedMemoryFiles != contextUsageWantDroppedMemoryFiles {
		t.Errorf("dropped_memory_files: got %d, want %d — 3 files in, one path over the 256-byte "+
			"bound, 2 retained", p.DroppedMemoryFiles, contextUsageWantDroppedMemoryFiles)
	}

	assertContextUsageKeySetsOnTheWire(t, raw)
}

// assertContextUsageCategories pins the four surviving categories IN THE PRODUCER'S
// DESCENDING ORDER. The canned list is fed unsorted, so this is simultaneously the
// proof that the producer ranked it and that nothing downstream re-ordered it.
func assertContextUsageCategories(t *testing.T, p protocol.ContextUsagePayload) {
	t.Helper()
	want := []protocol.ContextUsageCategory{
		{Name: "Fixture tools", Tokens: 4200},
		{Name: "Fixture messages", Tokens: 2600},
		{Name: "Fixture system prompt", Tokens: 1800},
		{Name: "Fixture deferred tools", Tokens: 900},
	}
	if len(p.Categories) != len(want) {
		t.Fatalf("categories: got %d rows, want %d\n%+v", len(p.Categories), len(want), p.Categories)
	}
	for i, row := range want {
		if p.Categories[i] != row {
			t.Errorf("category %d: got %+v, want %+v — the fed list is UNSORTED, so a mismatch "+
				"here is either a lost ranking or a re-sort downstream", i, p.Categories[i], row)
		}
	}
}

// assertContextUsageMCPTools pins the cap's behaviour rather than 32 literal rows: the
// count, the descending order, the uniform server name, and — the load-bearing one —
// the ABSENCE of the over-bound entry. maxContextUsageStringBytes rejects a complete
// entry rather than inventing a cut name, so a truncated survivor would be a different
// and worse behaviour that a count-only assertion could not tell from this one.
func assertContextUsageMCPTools(t *testing.T, p protocol.ContextUsagePayload) {
	t.Helper()
	if len(p.MCPTools) != contextUsageWantMCPTools {
		t.Fatalf("mcp_tools: got %d rows, want %d", len(p.MCPTools), contextUsageWantMCPTools)
	}
	for i, tool := range p.MCPTools {
		if tool.ServerName != "fixture-server" {
			t.Errorf("mcp_tools[%d].server_name: got %q, want %q", i, tool.ServerName, "fixture-server")
		}
		if len(tool.Name) > 256 {
			t.Errorf("mcp_tools[%d].name is %d bytes, over the producer's 256-byte bound — the "+
				"entry should have been rejected whole, not truncated", i, len(tool.Name))
		}
		if strings.Contains(tool.Name, contextUsageOverlongToolNeedle) {
			t.Errorf("mcp_tools[%d] carries the over-bound entry %q; it must be dropped, not cut",
				i, tool.Name)
		}
		if i > 0 && p.MCPTools[i-1].Tokens < tool.Tokens {
			t.Errorf("mcp_tools is not in descending token order at %d: %d then %d — a producer-side "+
				"count cut keeps the HEAVIEST entries, so the ranking is the only signal saying which",
				i, p.MCPTools[i-1].Tokens, tool.Tokens)
		}
	}
	// The extremes, which pin the ranking's endpoints rather than only its monotonicity.
	if p.MCPTools[0].Tokens != 262 {
		t.Errorf("heaviest mcp tool: got %d tokens, want 262", p.MCPTools[0].Tokens)
	}
	if last := p.MCPTools[len(p.MCPTools)-1]; last.Tokens != 40 {
		t.Errorf("lightest retained mcp tool: got %d tokens, want 40", last.Tokens)
	}
}

// assertContextUsageMemoryFiles pins the two survivors and, in the same breath, the
// no-normalisation rule: these paths are descriptive text, and nothing on this path
// joins, cleans, resolves or opens them. They are fictional under /__pyry_fake__/.
func assertContextUsageMemoryFiles(t *testing.T, p protocol.ContextUsagePayload) {
	t.Helper()
	want := []protocol.ContextUsageMemoryFile{
		{Path: "/__pyry_fake__/memory/preferences.md", Type: "", Tokens: 31},
		{Path: "/__pyry_fake__/memory/project.md", Type: "", Tokens: 19},
	}
	if len(p.MemoryFiles) != len(want) {
		t.Fatalf("memory_files: got %d rows, want %d\n%+v", len(p.MemoryFiles), len(want), p.MemoryFiles)
	}
	for i, row := range want {
		if p.MemoryFiles[i] != row {
			t.Errorf("memory_files[%d]: got %+v, want %+v — the path crosses byte for byte; a "+
				"cleaned or resolved value here means some layer treated it as a file handle",
				i, p.MemoryFiles[i], row)
		}
	}
	for i, file := range p.MemoryFiles {
		if strings.Contains(file.Path, contextUsageOverlongPathNeedle) {
			t.Errorf("memory_files[%d] carries the over-bound path %q; it must be dropped, not cut",
				i, file.Path)
		}
	}
}

// assertContextUsageKeySetsOnTheWire is AC 3's key-set half and the reason this spec
// keeps the raw bytes. The canned reading carries keys the daemon's decode target does
// not declare — a per-category `color` and `isDeferred`, a per-tool `isLoaded`, and the
// reading's own `rawMaxTokens`, `autocompactSource`, `autoCompactThreshold` and
// `isAutoCompactEnabled`. A frame that had grown any of them would satisfy every
// assertion above and fail only here. Decoding into protocol.ContextUsagePayload cannot
// make this claim: it discards what it does not declare, which is the thing under test.
func assertContextUsageKeySetsOnTheWire(t *testing.T, raw json.RawMessage) {
	t.Helper()

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode context_usage payload as an object: %v\n%s", err, raw)
	}
	assertExactKeys(t, "context_usage payload", top, []string{
		"categories", "conversation_id", "dropped_categories", "dropped_mcp_tools",
		"dropped_memory_files", "max_tokens", "mcp_tools", "memory_files", "model",
		"percentage", "total_tokens",
	})

	for _, row := range []struct {
		field string
		want  []string
	}{
		{"categories", []string{"name", "tokens"}},
		{"mcp_tools", []string{"name", "server_name", "tokens"}},
		{"memory_files", []string{"path", "tokens", "type"}},
	} {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(top[row.field], &rows); err != nil {
			t.Fatalf("decode %s rows: %v", row.field, err)
		}
		if len(rows) == 0 {
			t.Fatalf("%s carried no rows; its key-set claim would be vacuous", row.field)
		}
		for _, got := range rows {
			assertExactKeys(t, row.field+" row", got, row.want)
		}
	}
}

func assertExactKeys(t *testing.T, where string, got map[string]json.RawMessage, want []string) {
	t.Helper()
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("%s key set: got %v, want %v — the frame carries exactly what the payload type "+
			"declares and nothing the canned reading smuggled through", where, keys, want)
	}
}
