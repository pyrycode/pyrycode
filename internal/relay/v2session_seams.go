package relay

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// V2SessionConfig parameterises V2SessionManager. The handshake/transport
// fields are required; NewV2SessionManager validates and panics or errors on
// missing required values per the documentation below. Optional seams document
// their nil behaviour on each field.
//
// SECURITY: StaticPriv is the binary's 32-byte X25519 static private
// key. It MUST NOT be logged, wrapped into an error message, or emitted
// on any wire surface. internal/keys and internal/noise document the
// same contract for the same bytes; this struct extends the contract
// to the manager's holding site.
type V2SessionConfig struct {
	// WorkspaceBase is a caller-resolved base advertised only to admitted peers
	// in the encrypted hello_ack. Nil uses WorkspaceRoot(); a supplied absolute
	// value is reported verbatim, while empty or relative values omit the key
	// without fallback. Advertisement performs no filesystem access. The caller
	// must not mutate the pointed-to value while the manager runs.
	WorkspaceBase *string

	// Frames is the inbound RoutingEnvelope stream from a relay.Connection
	// (or an in-memory channel in tests). Run consumes until Frames
	// closes or ctx is done.
	Frames <-chan protocol.RoutingEnvelope

	// Outbound forwards a single binary→relay RoutingEnvelope. Production
	// wiring passes (*relay.Connection).Send. Non-nil errors are logged
	// at debug and dropped — the relay leg's reconnect handles recovery
	// (mirrors v1's cmd/pyry/relay.go forwarder posture).
	Outbound func(protocol.RoutingEnvelope) error

	// Connected reports whether the relay transport leg is currently up. The
	// push drain (drainOnce) consults it BEFORE sealing a queued envelope: a
	// false result leaves the head un-popped and unsealed, so no Noise
	// send-nonce is burned for a frame that cannot reach the phone (a burned
	// nonce gaps the phone's recv nonce → 4421 close of a still-live session,
	// #874). This extends queuedEnv's "held unsealed" invariant from the
	// enqueue side to the drain side.
	//
	// Optional: nil ⇒ always-connected — the drain never holds, preserving the
	// pre-#874 drop-on-send posture for foreground / unwired / existing tests.
	// Production wires (*relay.Connection).Connected, a level poll of the
	// transport leg's live-conn state. A true result is best-effort: the conn
	// may drop between the poll and the send (a single-frame residual race at
	// the up→down transition instant), but a false result reliably holds. NOT
	// a security decision — it gates only the timing of a seal that would
	// otherwise happen anyway; V2StateOpen and per-conn addressing stay in
	// forwardEnvelope, downstream of the probe.
	Connected func() bool

	// Reconnect, when non-nil, is an edge-triggered signal that fires once per
	// fresh relay transport conn. On each fire, Run re-signals the push drain so
	// a control envelope held while the leg was down (see Connected) flushes the
	// instant the leg recovers, without waiting for the next Push (#875).
	// Production wires (*relay.Connection).Reconnected. Cap-1 drop-on-full,
	// single observer.
	//
	// Optional: nil ⇒ no new wake source. Run's select arm reads a nil channel,
	// which is never ready, so the drain flushes only on the pre-#875
	// Push-driven re-signal — byte-identical to the foreground / unwired /
	// existing-test posture. This wakes the drain only; it seals nothing.
	// drainOnce still consults Connected before the pop, so a conn that drops
	// again between this edge and the pop burns no nonce (#874). NOT a security
	// decision.
	Reconnect <-chan struct{}

	// RekeyInterval overrides the scheduled re-key cadence — the timer that
	// fires emitRekeyRequest("scheduled") on an open session. Optional: zero ⇒
	// the rekeyInterval package default (1h). Read only by armRekeyTimer.
	//
	// Test-only seam (#920): the e2e suite lives in package e2e and cannot
	// mutate internal/relay's unexported timing vars the way the in-package
	// tests do, so the scheduled-rekey wire path is otherwise untriggerable
	// from an e2e test. Production (cmd/pyry) leaves it zero; it is never
	// sourced from the wire, a file, or operator input, so it widens no trust
	// boundary. A smaller value only raises rotation frequency (strictly more
	// forward secrecy) — no value weakens the shipped 1h posture or disables
	// rotation, and it touches no key material, peer-static pin, or AEAD.
	RekeyInterval time.Duration

	// RekeyReplyTimeout overrides the bounded window between emitting a
	// rekey_request and tearing the session down when the phone's fresh
	// noise_init never arrives. Optional: zero ⇒ the rekeyReplyTimeout package
	// default (30s). Read only by armRekeyReplyTimer. Same test-only population
	// and security posture as RekeyInterval.
	RekeyReplyTimeout time.Duration

	// MinClientVersions holds the minimum app version, as MAJOR.MINOR.PATCH,
	// keyed by the app name a hello's client_version carries (#2578). Once any
	// entry is non-empty, a hello whose token is accepted but whose
	// client_version is unparsable, or below its app's minimum, is refused with
	// client.update_required and close 4412. Optional: nil or an empty value ⇒
	// no minimum for that app, and with none set no hello is refused for its
	// version. NewV2SessionManager errors on a value that does not parse.
	//
	// Production (cmd/pyry) passes ShippedMinClientVersions(), the build
	// constants; tests pass their own map so the shipped constants never change
	// for a test. Never sourced from the wire or operator input.
	MinClientVersions map[string]string

	// RekeyRetryInterval overrides the short re-arm cadence used when a
	// scheduled re-key wake fires while the relay leg is down and the emit is
	// deferred (#912). Optional: zero ⇒ the rekeyRetryInterval package default
	// (1m). Read only by armRekeyRetryTimer. Same test-only population and
	// security posture as RekeyInterval.
	RekeyRetryInterval time.Duration

	// StaticPriv is the binary's 32-byte X25519 static private key.
	StaticPriv []byte

	// Devices is the token-validation predicate for hello.Token.
	Devices *devices.Registry

	// DevicesPath is the on-disk devices.json path reloaded into Devices
	// immediately before each handshake's token Validate (#782), so a device
	// paired via `pyry pair` after daemon startup is accepted on its next
	// connection without a restart. Optional: "" disables the reload — the
	// handshake validates against the startup-loaded in-memory set only
	// (keeps existing tests byte-stable; mirrors the claudeSessionsDir=""
	// opt-out idiom). On a reload read error the handshake proceeds against
	// the retained in-memory set (fail closed — accept set not widened,
	// loaded devices not lost).
	DevicesPath string

	// ServerID is surfaced into the hello_ack early-data payload.
	ServerID string

	// Logger receives lifecycle and reject events. Token, key bytes,
	// payload bytes, AEAD ciphertext, and base64 forms thereof MUST NOT
	// appear in any logged field.
	Logger *slog.Logger

	// Handlers is the application-layer envelope-type → handler table
	// used for v2 open-state dispatch. Optional: nil or empty map means
	// no app handlers are registered, and every open-state envelope falls
	// through to a sealed protocol.unsupported reply via dispatch.Route.
	// Mirror v1's internal/dispatch.Dispatcher.Register registration
	// shape — production wires Handlers via the daemon, same handlers as
	// v1.
	//
	// SECURITY: handlers run on the addressed conn's app-frame worker
	// goroutine (#965), NOT on the manager's Run goroutine — so a slow
	// handler no longer stalls Run, but the worker processes one frame at a
	// time, so a handler MUST still return in bounded time or it stalls that
	// conn's subsequent frames (bound long waits with a ctx timeout, as
	// create_conversation does). Handlers MUST NOT touch s.send / s.recv or
	// any Noise/session state — the worker never holds them; every reply is
	// sealed back on the Run goroutine (forwardAppReply), keeping the send
	// CipherState single-owner. A handler MUST NOT spawn a long-lived
	// background goroutine that retains the *dispatch.Conn passed in — the
	// conn's outbound channel is per-frame and is drained only while
	// routeAppFrame runs; sends from a forked goroutine after the handler
	// returns are silently lost (the channel is leaked but capacity-bounded,
	// and reclaimed by GC).
	Handlers map[string]dispatch.Handler

	// KnownConversation reports whether conversationID names a conversation
	// this daemon hosts. handleRequestSnapshot and handleMCPStatusRequest use it
	// to reject an unknown/foreign id with conversation.not_found before any
	// other reply or resolver call.
	// request_session_settings consulted it between #1586 and #1610 and no longer
	// does — a pure membership check reads a known but UNBOUND conversation as
	// addressable, so that verb resolves through RunConfigFor instead, which
	// refuses the unknown and the unbound identically. Optional: when nil, every
	// request_snapshot is rejected as not-found. Production wires it to a
	// conversations.Registry membership check, which takes that registry's mutex
	// and linear-scans its slice; it is not a map lookup.
	KnownConversation func(conversationID string) bool

	// CodexConversation reports whether conversationID's bound session runs
	// Codex (#2644). forwardEnvelope consults it, through withheldFromConn, to
	// keep every pushed frame about such a conversation from a conn that did not
	// negotiate protocol.CapabilityMultiAgent. Called on the Run goroutine, per
	// frame to such a conn; production wires a conversations-registry lookup plus
	// sessions.Pool.HarnessFor, the two locks RetainedModelLists already takes on
	// Run. An unknown conversation, one with no bound session and a harness miss
	// all report false. Optional: when nil no conversation is Codex and every
	// frame is delivered as before #2644.
	CodexConversation func(conversationID string) bool

	// ConversationAgent answers the agent of conversationID — protocol.AgentClaude
	// or protocol.AgentCodex — by the rule list_conversations tags rows with
	// (#2669). ok is false for a conversation it does not know. forwardEnvelope and
	// forwardAppReply consult it, through agentTaggedForConn, per
	// conversation_updated sealed for a conn that negotiated
	// protocol.CapabilityMultiAgent, on the Run goroutine; production wires the
	// same registry lookup plus sessions.Pool.HarnessFor that CodexConversation
	// takes. Optional: when nil every conversation_updated is delivered without an
	// agent, as before #2669.
	ConversationAgent func(conversationID string) (agent string, ok bool)

	// MergedModelOptions answers, for a pushed model_list's Claude entries, the
	// list a conn that negotiated protocol.CapabilityMultiAgent is sent instead
	// (#2652): those entries tagged as Claude's, then the Codex entries the daemon
	// holds, tagged as #2651's replies tag them. forwardEnvelope consults it,
	// through mergedForConn, per pushed model_list to such a conn — live or
	// replayed, the frames carrying an EventID — on the Run goroutine; #2651's
	// reply and connect-time reconcile, already merged, never reach it. It must return a slice it owns and never
	// write through its argument. Optional: when nil every pushed model_list is
	// delivered unchanged to every conn, as before #2652.
	MergedModelOptions func(claude []protocol.ModelOption) []protocol.ModelOption

	// HistoryPage serves one backward step of a conversation-history walk for an
	// inbound request_history (#2116), over the daemon's durable on-disk log
	// (#2112). handleRequestHistory is its sole reader. Optional: when nil the
	// frame is CONSUMED BUT INERT — no reply, and not one byte of its payload
	// parsed — mirroring the nil AttachmentIntake / AttachmentResolve guards and
	// buying the same property, that an unwired daemon performs zero parsing of
	// remote-authored bytes.
	//
	// PRECONDITION, AND IT IS THE CALLER'S: conversationID MUST already have
	// passed KnownConversation. history.Store.Page's own block states it — the id
	// becomes a path component below this seam — and handleRequestHistory is the
	// caller that discharges it, before it ever reaches here.
	//
	// cursor IS PASSED THROUGH UNPARSED, always. The wire declares it opaque and
	// history.parseCursor is the only validator anywhere; nothing in
	// internal/relay may decode one, log one, or branch on its contents.
	//
	// limit ARRIVES ALREADY POSITIVE AND ALREADY NARROWED: the handler substitutes
	// its own page size for the client's 0, refuses a negative, and caps the rest
	// at maxHistoryPageEntries. An implementation MUST NOT size any buffer from it
	// — see HistoryPageResult.Entries.
	//
	// BOUNDED TIME, but NOT on the Run goroutine: this seam is called from the
	// addressed conn's appFrameWorker, which is why it is allowed to read files at
	// all. It still stalls that conn's later frames while it runs, so an
	// implementation that blocks indefinitely is a bug — production wires it to a
	// store whose work is bounded by the page rather than by the log.
	HistoryPage HistoryPager

	// RunConfigFor reports the NAMED conversation's own run configuration — its
	// bound session id, that session's model / effort / YOLO, and that session's
	// context-window used / window figures — as one RunConfig describing one
	// session (#1609). handleRequestSessionSettings is its reader, and since #1610
	// its ONLY run-configuration source: a client is told about the conversation
	// it is actually in rather than about the shared bootstrap session. It
	// replaced a bootstrap-scoped session-id seam; the agreement a client depends
	// on — that the reported id names the session the reported values came from —
	// is a property of RunConfig now rather than a rule spanning separate fields.
	//
	// Comma-ok rather than a flag inside RunConfig: false means the conversation is
	// not addressable — unknown to this daemon, bound to nothing, or bound to a
	// session the pool no longer holds — and every field of the returned RunConfig
	// is at its zero, which a caller MUST NOT read. true with an empty Model or
	// Effort is a real answer, not a degraded one (see RunConfig), which is why the
	// refusal cannot be expressed in the values.
	//
	// Optional: nil ⇒ no consumer can resolve any conversation (foreground / v1 /
	// unwired). Primitive-typed in both directions (a
	// string in, a RunConfig of scalars out) so internal/relay imports neither
	// internal/sessions nor internal/contextwindow; production composes it at the
	// cmd/pyry wiring point from a conversations-registry + pool resolver and the
	// by-id context-window reader.
	//
	// SECURITY: the refusal IS the control. conversationID is untrusted network
	// input and stays a lookup key into the daemon's own registry — it is never
	// returned, joined into a path, logged, or wrapped into an error. The reported
	// session id comes out of the daemon's own registry record and the producer
	// confirms the pool holds it before reporting anything, so an unresolvable
	// conversation addresses NOTHING: never the bootstrap session, and never a
	// session a caller named. The reported id is a routing key, not a secret — it
	// already crosses the wire outbound on session_transition and inbound on
	// set_session_settings. Read-only reflection — it reports the YOLO control
	// that only SettingsUpdater, the write path, can change.
	RunConfigFor func(conversationID string) (RunConfig, bool)

	// EffectiveEffortFor reports Claude's applied effort for the current child of
	// the NAMED conversation. handleRequestSessionSettings calls it only after
	// RunConfigFor accepts that same non-empty conversation, and never uses it as
	// a source for the saved Effort or any other RunConfig field (#2516).
	//
	// The pointer is the nullable result: non-nil is a confirmed string, while nil
	// with true is confirmed JSON null (Claude reported no effort parameter). The
	// comma-ok is availability: false means no current reading, and the pointer
	// MUST be ignored even when non-nil. Optional: nil has the same unavailable
	// posture, omitting effective_effort while preserving every saved field.
	//
	// BOUNDED TIME, but NOT on Run: this seam runs on the addressed connection's
	// appFrameWorker because a production implementation may wait on a child round
	// trip. It MUST honor ctx so manager shutdown releases the worker. The relay
	// adds no cache; every accepted request calls the provider once. The contract
	// carries only one nullable scalar, never a full child settings response.
	EffectiveEffortFor func(ctx context.Context, conversationID string) (*string, bool)

	// MemorySearchFor supplies a wire-ready search-access report for the named
	// conversation and the session ID accepted by RunConfigFor. The handler calls
	// it once per fully decoded, resolved settings request; it retains no result.
	// An error produces an unknown report with no providers. Optional: nil omits
	// memory_search. This runs on the conn's app-frame worker and must honor ctx.
	MemorySearchFor func(ctx context.Context, conversationID, sessionID string) (protocol.MemorySearchReport, error)

	// CapabilitiesFor reports the agent-and-model half of a session's capability
	// list (#2646) for a multi_agent conn's session_settings reply.
	// handleRequestSessionSettings calls it once, only after RunConfigFor accepted,
	// with THAT RunConfig's SessionID and Model — ids from the daemon's own record,
	// never the caller's conversation id. false means no session with a known
	// agent, and the reply then carries no capability object. The relay adds the
	// permission modes and attachment types itself, and drops any value its own
	// shape checks would refuse.
	//
	// The lists must be built from the same code the set_session_settings checks
	// run, so every listed option is accepted for that session. Runs on the conn's
	// app-frame worker, like EffectiveEffortFor. Optional: nil omits the object.
	CapabilitiesFor func(sessionID, model string) (AgentCapabilities, bool)

	// ModelListFor reports the NAMED conversation's model menu, already shaped as a
	// marshal-ready model_list payload, for an inbound request_model_list (#2125).
	// handleRequestModelList is its sole reader.
	//
	// CONVERSATION-KEYED, NOT ENUMERATE-ALL, and that is the whole difference from
	// RetainedModelLists below. That seam enumerates because a V2Session carries no
	// conversation id, so there is nothing to key a connect-time reconcile on; this
	// path has an id in the request, so RunConfigFor above is the shape it copies —
	// a string in, a payload and a comma-ok out. Do not reach for the enumerator
	// here: it would resolve every conversation the registry carries to answer about
	// one.
	//
	// IT DECIDES NOTHING ABOUT WHICH VOCABULARY ANSWERS. That decision — a bound
	// session's own retained list, else the daemon-wide copy (#2124) — lives inside
	// the cmd/pyry resolver, whose block forbids a caller forking it. This seam
	// answers "what is that conversation's menu" and the handler's separate
	// KnownConversation call answers "is this conversation ours"; neither is a second
	// opinion on the other's question.
	//
	// Comma-ok rather than an empty payload, and this is the ONE PLACE the model-list
	// family cannot follow RunConfigFor's neighbour request_session_settings, whose
	// all-zero reply is a real answer. turnevent.ModelList.Models is documented
	// never-empty, so AN EMPTY Models MUST NEVER STAND IN FOR "UNKNOWN": false means
	// no menu exists to send and the handler turns it into a coded error frame. A
	// caller MUST NOT read the payload on false — the production producer happens to
	// zero its refusal return, but that is a property of cmd/pyry rather than of this
	// contract.
	//
	// Optional: nil ⇒ the verb refuses every request it has already accepted as
	// hosted, with the same retryable model_list.unavailable a resolver refusal
	// earns. The merge is deliberate: distinguishing them would publish whether the
	// host's model-list source is wired, which is a fact about the machine rather
	// than about the request. Foreground / v1 wirings leave it nil, as they leave
	// RetainedModelLists nil, and no existing construction site changes.
	//
	// A closure returning protocol.ModelListPayload rather than a *sessions.Pool or a
	// turnevent value: internal/relay imports neither internal/sessions nor
	// internal/turnevent, and protocol is already imported both sides, so the payload
	// crosses with no new import and no cycle — RetainedModelLists' reason, unchanged.
	//
	// SECURITY: conversationID is untrusted network input and reaches this seam only
	// AFTER KnownConversation has passed on it. It stays a lookup key into the
	// daemon's own registry — never returned, never joined into a path, never wrapped
	// into an error — and the reported conversation_id in the payload comes out of
	// the daemon's own registry record rather than being echoed back, RunConfigFor's
	// posture. This seam accepts ALREADY-BOUNDED payloads only and applies no bound
	// of its own: the entry count and each row's fields are capped at construction
	// (ModelListPayload.DroppedModels, ModelOption.TruncatedFields, frozen by
	// #1704/#1705), so a second cap here would be a second place the limit is decided.
	// The payload text is claude-authored and untrusted (ModelOption's own doc) and
	// is NEVER logged on this path.
	//
	// BOUNDED TIME, on the Run goroutine. The handler answers inline rather than
	// handing off to the conn's appFrameWorker, so an implementation MUST stay a
	// bounded in-memory read — production wires a registry lookup plus a copy of at
	// most ten model rows. An implementation that reads a file or enumerates the
	// registry belongs off Run, and moving it there means switching the handler's
	// emit from forwardEnvelope to forwardToRun in the same change.
	//
	// multiAgent is the asking conn's negotiated multi_agent decision (#2651): true
	// asks for the merged list of both agents' entries, tagged with agent and family;
	// false asks for today's Claude-only list, byte-identical. The capability is per
	// conn while the menu is per conversation, which is why the handler passes it.
	ModelListFor func(conversationID string, multiAgent bool) (protocol.ModelListPayload, bool)

	// MCPStatusFor reports the current MCP server status for one hosted
	// conversation, already shaped as the existing mcp_status payload. The
	// mcp_status_request handler is its sole reader (#2381).
	//
	// The handler calls this seam only after the request payload decodes, the
	// connection has negotiated the interactive capability, and KnownConversation
	// accepts the id. The id remains an untrusted lookup key and MUST NOT be logged,
	// joined into a path, or returned as the answer's ConversationID. Every string
	// in the returned payload is Claude-authored and MUST NOT be logged either.
	//
	// Comma-ok distinguishes a current empty-server snapshot (true) from no current
	// status (false). A caller MUST NOT inspect the payload when false; the relay
	// translates that outcome to retryable mcp_status.unavailable and never falls
	// back to a retained or empty frame.
	//
	// Optional: nil makes the inbound type consumed but inert before payload decode,
	// membership, or reply. The production daemon leaves it nil until #2382 wires
	// live-child request correlation.
	//
	// This call runs on the addressed connection's appFrameWorker, not Run, because
	// a live implementation may wait for a child round trip. It MUST honor ctx so
	// manager shutdown terminates the wait. Replies return through forwardToRun;
	// implementations must never touch V2Session or Noise state.
	MCPStatusFor func(ctx context.Context, conversationID string) (protocol.MCPStatusPayload, bool)

	// ContextUsageFor reports one hosted conversation's context-window breakdown —
	// current where one can be taken, last known otherwise — already shaped as the
	// existing context_usage payload, for an inbound request_context_usage (#2431).
	// handleRequestContextUsage is its sole reader.
	//
	// MCPStatusFor's shape above, deliberately and in full: a context and a
	// conversation id in, a payload and a comma-ok out, so internal/relay imports
	// neither internal/sessions nor internal/streamsup and protocol is already
	// imported on both sides.
	//
	// THE READING IS FRESH AND EXPENSIVE WHEN ONE CAN BE TAKEN, which is the whole
	// reason the verb exists; see the AsOf paragraph below for when one cannot.
	// The implementation asks claude at detail:"full" — a token-count API call per
	// category — where the automatic post-turn frame (#2371) carries the cheap
	// detail:"summary" estimate. The DETAIL IS THE IMPLEMENTATION'S CHOICE and is
	// deliberately absent from the request payload; see RequestContextUsagePayload.
	//
	// IT MAY WAIT TWICE AND THE RELAY BOUNDS NEITHER. An implementation defers a
	// request that arrives mid-turn until that turn ends, then waits on a child round
	// trip. Both waits belong to the implementation, which is why this seam takes a
	// context and MUST honor it so manager shutdown terminates them.
	//
	// CLOSELY-SPACED ASKS COLLAPSE BELOW THIS SEAM, NEVER ABOVE IT. Each ask costs
	// real tokens, so an implementation answers asks arriving close together from one
	// round trip. That belongs below here because the collapse is PER CONVERSATION,
	// not per connection — two clients watching one conversation is the case it exists
	// for — and this package has no conversation-keyed state to do it in. A caller
	// must therefore not assume its own ask caused the round trip it is answered from.
	//
	// Comma-ok distinguishes a reading to show (true) from none at all (false). A
	// caller MUST NOT inspect the payload when false; the relay translates that
	// outcome to retryable context_usage.unavailable and substitutes NOTHING of its
	// own — no zero payload, no previous answer, no empty frame. That matters more
	// here than on most seams, because a ContextUsagePayload of all zeros is
	// indistinguishable from a genuine empty context, so the refusal cannot be
	// expressed in the values.
	//
	// A TRUE IS NOT A PROMISE THAT CLAUDE WAS JUST ASKED (#2461). When no fresh
	// reading can be taken — no bound session, no live child, a child that never
	// answers — an implementation MAY answer from a reading it stored earlier, and
	// the production one does, so a dormant conversation shows a figure instead of an
	// error. That is a licence for THIS seam only, and it is why the paragraph above
	// says "a reading to show" rather than "a current reading".
	//
	// AN IMPLEMENTATION THAT DOES SO MUST STAMP ContextUsagePayload.AsOf, and the
	// requirement is not stylistic. A stored reading carries the five headline values
	// and empty inventories, while that payload's own contract makes an empty
	// inventory a POSITIVE reading — claude reporting no MCP tools. The two are
	// byte-identical without the key, so an unstamped remembered answer would tell a
	// client that a conversation has no memory files rather than that nobody looked.
	// A fresh reading omits it. The relay neither sets nor inspects the key; it
	// forwards the payload it is handed.
	//
	// Optional: nil makes the inbound type consumed but INERT before payload decode,
	// membership, or reply — the nil HistoryPage / MCPActuator posture, buying the
	// same property, that an unwired daemon parses zero remote-authored bytes.
	// Foreground and v1 wirings leave it nil.
	//
	// SECURITY: conversationID is untrusted network input and reaches this seam only
	// AFTER KnownConversation has passed on it. It stays a lookup key into the
	// daemon's own registry — never returned, never joined into a path, never logged,
	// never wrapped into an error — and the reported conversation_id in the payload
	// comes out of the daemon's own registry record rather than being echoed back,
	// RunConfigFor's and ModelListFor's posture. The returned payload is
	// MIXED-PROVENANCE (ContextUsagePayload's own doc): its ConversationID is
	// daemon-authored and EVERY OTHER STRING is claude- or workspace-authored,
	// including memory-file paths off the operator's own filesystem. None of it is
	// logged on this path at any level, and it reaches the wire only over the unicast,
	// AEAD-sealed reply to the conn that asked.
	//
	// This call runs on the addressed connection's appFrameWorker, NOT on Run, for the
	// waits above. It MUST NOT touch V2Session or any Noise state — replies return
	// through forwardToRun for Run-owned sealing, and emitting from the worker would
	// be a concurrent Encrypt under the single-owner send CipherState.
	ContextUsageFor func(ctx context.Context, conversationID string) (protocol.ContextUsagePayload, bool)

	// MCPActuator performs an inbound mcp_reconnect or mcp_toggle against one
	// conversation's live claude child (#2419). handleMCPReconnect and
	// handleMCPToggle are its sole readers, and the seam's own doc block carries the
	// contract — the authorization decision and its audit record are the
	// implementation's, the accepted answer's status payload crosses back rather than
	// being re-read here, and ServerName reaches it validated by nothing.
	//
	// The WRITE half of the MCP pair whose read half is MCPStatusFor above, which is
	// why it gets a per-device gate where that one needs none: these two verbs change
	// a running child's configuration.
	//
	// Optional: when nil the frame is CONSUMED BUT INERT — no reply, and not one byte
	// of its payload parsed — mirroring the nil HistoryPage / AttachmentIntake /
	// PairingMint guards and buying the same property, that an unwired daemon performs
	// zero parsing of remote-authored bytes. That is what leaves every non-production
	// construction site (unit tests, the fake-daemon e2e harness, foreground/v1)
	// compiling and behaving unchanged, and it is the whole of the #2419 slice's
	// fail-safety.
	//
	// SECURITY — NOTHING MAY BE WIRED HERE before the per-device actuation gate
	// (#2420) exists; the seam's block states the obligation and why. And note the
	// interface-nil trap, called out on THIS field because here the nil check is a
	// security gate rather than a convenience: assigning a nil-valued CONCRETE type
	// (`cfg.MCPActuator = (*impl)(nil)`) leaves this field non-nil, so dispatchAppFrame
	// admits the frame and the handler calls a method on a nil pointer. internal/relay
	// contains no recover(), so that is a crash rather than a refusal — fail-closed for
	// authorization, since no actuation reaches a child, but a remote-triggerable one
	// once the wiring bug exists. Wire a concrete non-nil implementation or leave the
	// field unset.
	MCPActuator MCPActuator

	// SystemPromptFor reports the NAMED conversation's stored system prompt and how
	// the running session's spawned-with value compares to it, already shaped as a
	// marshal-ready system_prompt payload, for an inbound request_system_prompt
	// (#2152). handleRequestSystemPrompt is its sole reader.
	//
	// CONVERSATION-KEYED, ModelListFor's shape above: a string in, a payload and a
	// comma-ok out, so this package imports neither internal/sessions nor
	// internal/conversations and protocol is already imported both sides.
	//
	// IT ANSWERS TWO QUESTIONS AT ONCE AND THAT IS THE POINT. The stored value comes
	// from the conversations registry; the spawned-with value comes from the live
	// session (#2150's session-keyed Pool.SystemPromptFor). A client needs both,
	// because a stored prompt takes effect only at the conversation's NEXT session
	// start — so the stored value alone would tell an operator who edits it and keeps
	// typing that their change is live when it is not. The producer resolves both and
	// reports the COMPARISON rather than the second value; see the collapse rule
	// below.
	//
	// THE COMPARISON IS COMPUTED ON THE COLLAPSED STORED VALUE, and getting this
	// wrong is the single most likely way this path ships broken. The registry stores
	// a tri-state (nil = no prompt, non-nil "" = explicitly empty, otherwise text)
	// and Pool.SystemPromptFor returns "" for BOTH no-bytes states by design, because
	// composing is the sessions package's business. So a conversation storing an
	// explicitly empty prompt whose session spawned with no operator text MATCHES,
	// and must not be reported as differing. The rule lives in the cmd/pyry producer,
	// which is the only place holding both halves; this package neither re-derives it
	// nor second-guesses it.
	//
	// TWO DIFFERENT RESOLUTION FAILURES, NOT ONE, and the seam keeps them apart even
	// though the wire merges them. false means this daemon does not host the named
	// conversation. A hosted conversation with no running session is TRUE, with the
	// payload's SessionPromptStatus at protocol.SystemPromptStatusNoSession — the
	// stored value is still reported, because "there is nothing running" is exactly
	// when an operator most needs to see what is stored. Collapsing the two at the
	// producer would make that impossible to express.
	//
	// Comma-ok, and a caller MUST NOT read the payload on false — the rule
	// RunConfigFor and ModelListFor both state, and it is pinned here against a
	// POISONED refusal double rather than borrowed from the producer's habit of
	// zeroing its refusal return. Unlike ModelListFor, though, false is not turned
	// into an error frame: this verb answers the constant no-session reply, which is
	// a real answer, so an unhosted conversation is indistinguishable from a hosted
	// one holding nothing and the verb is not a membership oracle.
	//
	// Optional: nil ⇒ every request is answered with that same constant reply
	// (foreground / v1 / unwired), never an error and never a silent drop. This is
	// deliberately NOT SettingsUpdater's nil posture: that seam is a write path that
	// owes a distinguishable "unavailable", where a read documented as always
	// answering one shape has nothing to gain from a second one.
	//
	// BOUNDED TIME, on the Run goroutine. The handler answers inline rather than
	// handing off to the conn's appFrameWorker, so an implementation MUST stay a
	// bounded in-memory read — production wires a registry lookup plus one pool map
	// read. An implementation that reads a file belongs off Run, and moving it there
	// means switching the handler's emit from forwardEnvelope to forwardToRun in the
	// same change (see the handler's file header).
	//
	// SECURITY: conversationID is untrusted network input and stays a lookup key into
	// the daemon's own registry — never returned, never joined into a path, never
	// logged, never wrapped into an error. The SESSION id the producer reads the
	// spawned-with value under is daemon-authored, taken from the resolved registry
	// record, so a caller can never reach another conversation's session through this
	// seam (the #678 hazard, closed by construction). The returned SystemPrompt is
	// OPERATOR-AUTHORED TEXT that becomes standing instructions to a claude child: it
	// is never logged at any level, and it reaches the wire only over the unicast,
	// AEAD-sealed reply to the conn that asked — never the broadcast push path, which
	// is why the write half's conversation_updated ack carries no prompt either.
	// Read-only reflection: this seam holds nothing that can start, restart, rotate or
	// interrupt a session, which is the structural half of "reading the prompt leaves
	// a running session alone". Do not widen it.
	SystemPromptFor func(conversationID string) (protocol.SystemPromptPayload, bool)

	// PairingMint mints a pairing for another device on behalf of the conn's
	// authenticated one, for an inbound mint_pairing (#2127). handleMintPairing is
	// its sole reader, and the seam's own doc block carries the contract — the
	// authorization decision and its audit record are the implementation's, the
	// minted device is always unprivileged, and the label arrives shape-validated.
	//
	// Optional: when nil the frame is CONSUMED BUT INERT — no reply, and not one
	// byte of its payload parsed — mirroring the nil AttachmentIntake /
	// AttachmentResolve / HistoryPage guards and buying the same property, that an
	// unwired daemon performs zero parsing of remote-authored bytes. That is what
	// leaves every non-production construction site (unit tests, the fake-daemon
	// e2e harness, foreground/v1) compiling and behaving unchanged.
	//
	// SECURITY: this is the only seam on the manager that MINTS A CREDENTIAL, and
	// the returned string is a plaintext bearer token. It reaches exactly one place
	// — the pairing field of the AEAD-sealed pairing_minted reply, unicast to the
	// conn that asked — and is never logged, never broadcast, and never wrapped
	// into an error. Wiring it widens docs/protocol-mobile.md § Security model
	// threat 4 on purpose (#2126's published decision): minting authority moves
	// from "a shell on the host" to "any privileged paired device", bounded by the
	// minted device being always unprivileged and by an unredeemed token expiring
	// at devices.RedemptionWindow.
	PairingMint PairingMinter

	// ModalResolver resolves inbound modal_answer / modal_cancel control
	// frames. Optional: when nil, both are inert no-ops (the modal bridge is
	// simply unwired — foreground, or pre-#708 before the producer is live).
	// Production wires the cmd/pyry resolver.
	ModalResolver ModalResolver

	// QuestionResolver resolves inbound question_answer / question_refused control
	// frames against the daemon's outstanding clarifying-question batches (#1984).
	// Optional: when nil, BOTH FRAMES ARE STILL CONSUMED by the interception —
	// they no longer reach dispatch.Route and so no longer draw its unknown-type
	// error reply — but nothing is decoded, nothing is handed off and nothing is
	// broadcast. That is the whole of the nil behaviour, and it is why the #1984
	// slice lands with no wiring site changed: every existing construction leaves
	// this field nil.
	//
	// Its own field rather than a method grown onto ModalResolver, following
	// OutstandingQuestions' reasoning (#1979): a question batch is its own frame
	// family (#1962), and growing the neighbour would force every ModalResolver
	// implementer to grow with it.
	//
	// SECURITY: the seam's own doc block carries the obligations — the payload is
	// remote-authored and validated by nothing beyond the decode, the index is
	// never range-checked, and NOTHING MAY BE WIRED HERE before the per-device
	// answer gate (#1986) exists, since the relay handler applies no authorization
	// and the nil seam is what makes the interception fail-safe today.
	QuestionResolver QuestionResolver

	// AttachmentIntake receives inbound attachment_chunk frames and releases a
	// departing conn's uploads (#1897). Optional: when nil the frame is STILL
	// CONSUMED by the interception — it no longer reaches dispatch.Route and so no
	// longer draws its unknown-type error reply — but nothing is decoded, nothing
	// is stored and nothing is replied, and closeWith releases nothing. That is
	// the whole of the nil behaviour, and it is what leaves every non-production
	// construction site (unit tests, the fake-daemon e2e harness, foreground/v1)
	// compiling and behaving unchanged.
	//
	// ONE PER DAEMON. attachments.Intake owns a registry whose in-flight ceiling
	// is daemon-wide, so a second instance would be a second budget of the same
	// size and the bound would stop meaning what it says. Production constructs
	// exactly one, in cmd/pyry's startRelayV2.
	//
	// SECURITY: the seam's own doc block carries the obligations — the payload is
	// remote-authored, the errors coming back wrap host paths and a sanitised
	// filename and are therefore neither loggable nor repliable, and Receive's
	// single-feeder precondition is discharged by the per-conn appFrameWorker
	// rather than by any lock.
	AttachmentIntake AttachmentIntake

	// AttachmentResolve answers the on-host path of one stored attachment inside
	// one conversation (#2054) — the READ half of the seam above, and the retrieval
	// leg's only filesystem reach. handleRequestAttachment is its sole reader.
	// Optional: when nil the request_attachment frame is STILL CONSUMED by the
	// interception — it no longer reaches dispatch.Route and so no longer draws its
	// unknown-type error reply — but nothing is decoded, nothing is resolved and
	// nothing is replied. That is the whole of the nil behaviour, matching
	// AttachmentIntake's, and it is what leaves every non-production construction
	// site compiling and behaving unchanged.
	//
	// COMMA-OK, NOT AN ERROR, and the collapse is the point rather than a
	// simplification. attachments.ResolvePath answers ONE sentinel for an unknown
	// id, a non-canonically-shaped id and an id resolving outside the named
	// conversation's directory alike, precisely so a dispatch site cannot branch on
	// what CodeAttachmentNotFound's deliberate merge forbids distinguishing; and its
	// shape-invalid refusal formats the RAW client-supplied id into its message,
	// which docs/protocol-mobile.md § Attachments forbids logging. So the error dies
	// at the one adapter that ever holds it — cmd/pyry's attachmentResolve closure,
	// already built there for handlers.SendMessage — and this seam receives only the
	// bool. Primitive-typed in both directions, like KnownConversation, so
	// internal/relay imports neither internal/attachments nor internal/conversations
	// for it.
	//
	// IT DISCHARGES NO REGISTRY CHECK WHATSOEVER. attachments.ResolvePath's stated
	// precondition is that conversationID is one the CALLER has already validated
	// against the daemon's registry — "this function cannot check that, and a caller
	// that gets it wrong defeats every check below" — and the production closure
	// validates nothing. handleRequestAttachment is that caller: it consults
	// KnownConversation BEFORE either id reaches this seam, which is what keeps an
	// identifier § Attachments repeatedly calls not a capability from becoming one.
	//
	// SECURITY: the only sanctioned implementation wraps attachments.ResolvePath.
	// StreamAttachment re-validates nothing it is handed, so a path from anywhere
	// else carries NO containment guarantee — the traversal defence is that
	// function's full-path equality check — and a path naming a NON-REGULAR FILE
	// misbehaves rather than erroring: os.ReadFile on a FIFO blocks indefinitely,
	// which here would wedge the conn's appFrameWorker and silently stall every
	// later frame on that conn. ResolvePath answers only regular files at exactly
	// the path its two ids build, so both are unreachable through it. The returned
	// path is NOT fully daemon-authored — its leaf is a sanitised client filename —
	// so it must never be logged, and no reply derived from it ever reaches the wire.
	AttachmentResolve func(conversationID, attachmentID string) (path string, ok bool)

	// WorkspaceFileRead reads one regular file LIVE from the workspace or
	// admitted read folders (#2598) — the path a client names, not a stored copy.
	// handleReadWorkspaceFile is its sole reader. Optional: nil makes the
	// read_workspace_file frame consumed and inert, as a nil AttachmentResolve
	// does for request_attachment.
	//
	// COMMA-OK FOR AttachmentResolve's REASON. Every refusal — denied name,
	// no admitted root, missing file, out-of-tree path, non-regular file,
	// over the size bound — collapses into false inside cmd/pyry's
	// workspaceFileReader, so the handler cannot branch on what the one
	// attachment.not_found answer must not distinguish, and never holds a
	// filesystem error that prints a host path.
	//
	// IT DISCHARGES NO REGISTRY CHECK. The handler consults KnownConversation
	// before the conversation id reaches this seam. Confinement to the admitted
	// roots, the two-leaf secret-name rule and the checked read are the
	// implementation's, and the only sanctioned one is workspaceFileReader,
	// which reuses the attach_file verb's confineFile and readChecked.
	WorkspaceFileRead func(conversationID, path string) (WorkspaceFile, bool)

	// Interrupter stops the running turn in the conversation an inbound
	// interactive `interrupt` control frame names (#707, #2103) — or, when the
	// frame names none, in the one the daemon's cursor points at. Optional: nil ⇒
	// interrupt is inert (no actuation) — the foreground / unwired case.
	// Production wires cmd/pyry's activeInterrupter, which owns the shape check
	// and the registry resolution this package cannot perform.
	Interrupter Interrupter

	// BackgroundTaskStopper is optional; production installs the bound-child
	// adapter in cmd/pyry. Unwired callers leave a true nil interface, never
	// a typed nil pointer: dispatchAppFrame's nil gate must consume the verb before
	// payload decode or enqueue. Negotiated interactive is the only other gate;
	// this paired-device action bypasses the tool-permission gate.
	BackgroundTaskStopper BackgroundTaskStopper

	// SessionStarter starts a fresh session in the conversation an inbound
	// interactive `new_session` control frame names (#831, #2099) — or, when the
	// frame names none, in the one the daemon's cursor points at. Optional: nil ⇒
	// new_session is inert — the foreground / unwired case. Production wires
	// cmd/pyry's activeSessionStarter, which owns the shape check and the registry
	// resolution this package cannot perform.
	SessionStarter SessionStarter

	// AgentSwitcher handles switch_agent asynchronously. Nil replies binary_offline.
	AgentSwitcher AgentSwitcher

	// QueueRemover drops a queued message named by an inbound dequeue_message
	// control frame (#723). Optional: nil ⇒ dequeue_message is inert (foreground
	// / unwired). Production wires *msgqueue.Queue.
	QueueRemover QueueRemover

	// QueueSender writes a queued message named by an inbound send_queued_now
	// control frame into the running claude turn (#2729). Optional: nil ⇒
	// send_queued_now is inert. Production wires *msgqueue.Queue.
	QueueSender QueueSender

	// DebugBundler assembles the current session's debug bundle (recent daemon
	// logs plus the newest recording when present) as one in-memory archive, for
	// an inbound request_debug_bundle control frame (#813). Optional: nil ⇒
	// request_debug_bundle replies with a deterministic unavailable error, never
	// a silent drop (foreground / unwired). Production wires a closure over
	// debugbundle.Assemble(recordingsDir, logRing.Snapshot) — the closure returns
	// only (archive, err) so internal/relay never imports internal/debugbundle
	// (the Manifest travels inside the archive as manifest.json, not out-of-band).
	//
	// SECURITY: the returned bytes are the plaintext bundle (recording + logs) —
	// the highest-value secret surface in the system. They MUST NOT be logged.
	// handleDebugBundleRequest streams them ONLY over the AEAD-sealed push path
	// and logs a byte count on success / the failure event on error, never any
	// content byte.
	DebugBundler func() (archive []byte, err error)

	// SettingsUpdater persists an inbound set_session_settings change — a paired
	// interactive phone's per-session model / effort / YOLO (#845). Optional: nil
	// ⇒ set_session_settings replies "unavailable" deterministically, never a
	// silent drop (foreground / unwired). Production wires a *sessions.Pool
	// adapter. The three presence pointers carry no secret; the fail-safe is the
	// pointer-nil semantics (an absent YOLO never enables bypass).
	SettingsUpdater SettingsUpdater

	// OutstandingModals enumerates the daemon's currently-outstanding modals as
	// marshal-ready modal_shown payloads (each already stamped with its original
	// modal_id) for connect-time reconcile (#877). Called on the Run goroutine
	// from handleNoiseInit's interactive-open tail; the returned payloads are
	// unicast to the just-opened conn only. A pure read: it mints no nonce and
	// retires nothing, so it neither re-arms the deny-on-timeout nor changes
	// answerability — a re-sent modal_id stays answerable exactly once, governed
	// by the registry's one-shot Resolve, which this path never calls.
	//
	// A closure returning []protocol.ModalShownPayload, not a *modalbridge.Registry:
	// internal/relay does not import internal/modalbridge, and protocol is already
	// imported, so the payload crosses the boundary with no new import and no cycle
	// (define the dependency where it is consumed).
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#877 / foreground /
	// existing-test posture. Production wires modalbridge.Registry.Snapshot.
	OutstandingModals func() []protocol.ModalShownPayload

	// OutstandingQueues enumerates the daemon's current per-conversation queued
	// backlogs as marshal-ready queue_state payloads (one per non-empty
	// conversation) for connect-time reconcile (#878), the queue twin of
	// OutstandingModals. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened
	// conn only. queue_state is snapshot-shaped full state, so the re-send is
	// idempotent by construction. A pure read: it mints no id and dequeues nothing.
	//
	// A closure returning []protocol.QueueStatePayload, not a *msgqueue.Queue:
	// internal/relay does not import internal/msgqueue, and protocol is already
	// imported, so the payload crosses the boundary with no new import and no cycle
	// (matching OutstandingModals — define the dependency where it is consumed).
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#878 / foreground /
	// existing-test posture. Production wires the cmd/pyry outstandingQueues adapter.
	OutstandingQueues func() []protocol.QueueStatePayload

	// RetainedModelLists enumerates the daemon's currently-retained model lists as
	// marshal-ready model_list payloads (one per session holding a list) for
	// connect-time reconcile (#1863) — the third Mode B instance after
	// OutstandingModals and OutstandingQueues. Called on the Run goroutine from
	// handleNoiseInit's interactive-open tail; the returned payloads are unicast to
	// the just-opened conn only. model_list is snapshot-shaped full state ("a
	// SNAPSHOT of what claude will accept, not a delta", ModelListPayload's own
	// doc), so the re-send is idempotent by construction — re-connecting re-sends
	// the same snapshot. A pure read: it mints nothing, retires nothing, and
	// changes no daemon state.
	//
	// A closure returning []protocol.ModelListPayload, not a *sessions.Pool or a
	// turnevent value: internal/relay imports neither internal/sessions nor
	// internal/turnevent (sessions appears only transitively via internal/control,
	// so a go list -deps reading looks like a contradiction and is not one), and
	// protocol is already imported, so the payload crosses the boundary with no new
	// import and no cycle (matching OutstandingModals / OutstandingQueues — define
	// the dependency where it is consumed).
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id
	// — it holds connID, state, resp, send, recv, device, interactive and
	// peerStatic — so there is no "this conn's conversation" to key on at connect
	// time. OutstandingQueues' one-per-conversation enumerate-all shape is the
	// precedent; RunConfigFor is the conversation-keyed variant and is the wrong
	// shape here.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many payloads are returned, not on
	// any entry's text — because the bound is decided at construction upstream
	// (ModelListPayload.DroppedModels on the aggregate, ModelOption.TruncatedFields
	// per entry, frozen by #1704/#1705). A second cap here would be a second place
	// the limit is decided and the two could disagree silently, so the obligation
	// stays the producer's. The payload text is claude-authored and untrusted
	// (ModelOption's own doc) and is NEVER logged on the reconcile path.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#1863 / foreground /
	// existing-test posture. #1864 wires the daemon-side producer.
	//
	// multiAgent is the opening conn's negotiated multi_agent decision (#2651), with
	// ModelListFor's meaning: the merged, tagged list when true, today's Claude-only
	// list when false.
	RetainedModelLists func(multiAgent bool) []protocol.ModelListPayload

	// OutstandingQuestions enumerates the daemon's currently-outstanding clarifying-
	// question batches as marshal-ready question_shown payloads (each already stamped
	// with its own question_batch_id and conversation_id) for connect-time reconcile
	// (#1979) — the fourth Mode B instance after OutstandingModals, OutstandingQueues
	// and RetainedModelLists. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened conn
	// only. A pure read: it mints no nonce and retires no batch, so it neither re-arms
	// the approval window nor changes answerability — a re-sent question_batch_id stays
	// answerable exactly once, governed by the registry's one-shot Resolve, which this
	// path never calls.
	//
	// The reconcile exists because question_shown has no other path to a late client:
	// the raise-time broadcast reaches only whoever is connected at the instant claude
	// asks, and the frame carries no event id, so it is not in the #647 turn-event
	// replay ring either. reconcileModals' doc block records the twist that makes the
	// miss cost more than a plain miss — the daemon counts an approval answerable while
	// ANY interactive conn is open, so a reconnected client that was never sent the
	// batch re-arms the window at every expiry while being structurally unable to
	// answer it.
	//
	// A closure returning []protocol.QuestionShownPayload, not a
	// *questionbridge.Registry: internal/relay imports neither internal/questionbridge
	// nor internal/modalbridge, and protocol is already imported, so the payload
	// crosses the boundary with no new import and no cycle (matching the three seams
	// above — define the dependency where it is consumed). modalbridge carries nothing
	// for this batch: it is its own frame family (#1962), because denyByClass makes
	// DefaultOptionID the deny option and a clarifying question has no deny option and
	// no safe default.
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id —
	// it holds connID, state, resp, send, recv, device, interactive and peerStatic — so
	// there is no "this conn's conversation" to key on at connect time.
	// RetainedModelLists' doc block states the same reasoning.
	//
	// Order is not part of the contract, and a caller MUST correlate a batch by its
	// question_batch_id rather than by its position in the returned slice: the
	// production producer walks a map, whose order Snapshot's own doc leaves
	// unspecified.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many batches are returned, not on any
	// entry's text — because the bound is decided upstream at parse time
	// (questionbridge.Parse: 1-4 questions, 2-4 options per question, over a
	// maxInputBytes-capped tool input). A second cap here would be a second place the
	// limit is decided and the two could disagree silently, so the obligation stays the
	// producer's. Note that the per-batch bounds above do NOT bound how many batches
	// can be outstanding at once: the registry holds no cardinality cap, so the
	// aggregate is bounded only by claude's own ask concurrency and by every terminal
	// path retiring its batch (#1973). On this path pushQueue's byte ceiling is the
	// backstop; a cardinality cap, if one is ever wanted, belongs to questionbridge and
	// not to either half of this reconcile. The four strings a batch carries — a
	// Question's Text and Header, a QuestionOption's Label and Description — are
	// claude-authored, untrusted text (Question's own doc) and are NEVER logged on the
	// reconcile path.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#1979 / foreground /
	// existing-test posture. #1980 wires the daemon-side producer to
	// questionbridge.Registry.Snapshot.
	OutstandingQuestions func() []protocol.QuestionShownPayload

	// RetainedSlashCommandLists enumerates the daemon's currently-retained
	// slash-command lists as marshal-ready slash_command_list payloads (one per
	// session holding a list, each already stamped with its own conversation_id)
	// for connect-time reconcile (#2006) — the fifth Mode B instance after
	// OutstandingModals, OutstandingQueues, RetainedModelLists and
	// OutstandingQuestions. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened
	// conn only. slash_command_list is snapshot-shaped full state — it is decoded
	// from one initialize reply, it is session configuration rather than a turn
	// event, and receiving one neither opens nor closes a turn — so the re-send is
	// idempotent by construction: re-connecting re-sends the same snapshot. A pure
	// read: it mints nothing, retires nothing, and changes no daemon state.
	//
	// The reconcile exists because the live turn lane is the only path carrying this
	// frame today and three independent loss points sit in front of it: the
	// emitter's empty-conversation early return, unconditional for the bootstrap
	// child because the conversation cursor is only ever set by a successful route
	// while the initialize ask fires at child spawn; the droppable classification
	// under droppableCap; and forwardEnvelope's last_event_id dedup, a reconnect
	// mechanism with no fresh-connect backfill. A client attaching later has no path
	// to the list at all, so its command menu stays empty until a turn that may
	// never come.
	//
	// A closure returning []protocol.SlashCommandListPayload, not a *sessions.Pool
	// or a turnevent value: internal/relay imports neither internal/sessions nor
	// internal/turnevent (sessions appears only transitively via internal/control,
	// so a go list -deps reading looks like a contradiction and is not one), and
	// protocol is already imported, so the payload crosses the boundary with no new
	// import and no cycle (matching the four seams above — define the dependency
	// where it is consumed).
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id
	// — it holds connID, state, resp, send, recv, device, interactive and peerStatic
	// — so there is nothing to key on at connect time; each payload self-identifies
	// by its own conversation_id. RetainedModelLists and OutstandingQuestions state
	// the same reasoning. cmd/pyry's resolveBoundSlashCommandList (#2005) is the
	// conversation-keyed variant and is deliberately the WRONG shape here; bridging
	// the two is #2007's job.
	//
	// Order is not part of the contract, and a caller MUST correlate a list by its
	// conversation_id rather than by its position in the returned slice — the
	// envelope id this path stamps is fixed and non-load-bearing for the same
	// reason.
	//
	// BOUNDED TIME, like every seam the manager calls on its Run goroutine: an
	// implementation that blocks stalls Run and with it every conn the manager
	// services. ModalResolver's doc block states the same obligation and this one is
	// not hypothetical — the #2007 producer walks a conversation registry under that
	// registry's mutex, which is exactly the shape that can block.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many payloads are returned, not on
	// any entry's text — because the bound is decided upstream at construction
	// (SlashCommandListPayload.DroppedCommands on the aggregate,
	// SlashCommand.TruncatedFields per entry, over a producer cut measured against
	// marshalled bytes so the envelope stays under the v2 application-envelope cap).
	// A second cap here would be a second place the limit is decided and the two
	// could disagree silently, so the obligation stays the producer's. Note that
	// those per-payload bounds do NOT bound how many payloads can be returned at
	// once; on this path pushQueue's byte ceiling is the backstop, and a cardinality
	// cap, if one is ever wanted, belongs to the producer and not to either half of
	// this reconcile. The four strings a row carries — Name, ArgumentHint,
	// Description and each entry of Aliases — are WORKSPACE-authored, untrusted text
	// that crossed the subprocess trust boundary (SlashCommand's own doc, which
	// grades that origin below claude-authored), and they are NEVER logged on the
	// reconcile path. They are also forwarded UNSANITISED — no control-character or
	// terminal-escape stripping happens here, and Description is measured to carry
	// newlines — which is SlashCommand's own documented decision and not an
	// omission: the render boundary owing the sanitisation is the client's.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#2006 / foreground /
	// existing-test posture, the nil-resolver posture the other optional control
	// seams share. #2007 wires the daemon-side producer.
	RetainedSlashCommandLists func() []protocol.SlashCommandListPayload

	// RetainedBackgroundTaskRosters enumerates the background-task rosters the
	// daemon currently holds as marshal-ready background_task_roster payloads (one
	// per session holding a roster, each already stamped with its own
	// conversation_id) for connect-time reconcile (#2078) — the sixth Mode B
	// instance after OutstandingModals, OutstandingQueues, RetainedModelLists,
	// OutstandingQuestions and RetainedSlashCommandLists. Called on the Run
	// goroutine from handleNoiseInit's interactive-open tail; the returned payloads
	// are unicast to the just-opened conn only. background_task_roster is
	// snapshot-shaped full state ("A SNAPSHOT, not a delta",
	// BackgroundTaskRosterPayload's own doc) — it reports what is alive at one
	// moment rather than what changed, it is conversation-scoped rather than
	// turn-scoped, and receiving one neither opens nor closes a turn — so the
	// re-send is idempotent by construction: re-connecting re-sends the same
	// snapshot. A pure read: it mints nothing, retires nothing, and changes no
	// daemon state.
	//
	// The reconcile exists because neither recovery mode in
	// docs/protocol-mobile.md § Reconnect / Backfill semantics serves this family
	// today. Mode A (cursor replay) needs the client to advertise
	// hello.last_event_id and pyrycode-desktop advertises none, whose stated
	// consequence is no replay at all; Mode B did not cover this frame until this
	// seam. So a client opening an interactive session sees an empty background-task
	// panel until claude next CHANGES the roster, which on a quiet session may never
	// happen — the roster is only ever emitted on the live turn lane.
	//
	// AN EMPTY ROSTER IS A POSITIVE STATEMENT that nothing is alive, and this is the
	// one place the five seams above give the wrong answer by analogy. Their
	// producers filter an empty aggregate away; a producer for this seam MUST NOT,
	// because "nothing is running" is exactly the signal a consumer of #1240's
	// symptom needs, and BackgroundTaskRosterPayload.MarshalJSON exists to guarantee
	// such a payload serialises as "tasks":[] rather than null. The consumer
	// likewise sends it rather than skipping it.
	//
	// A closure returning []protocol.BackgroundTaskRosterPayload, not a
	// *sessions.Pool or a turnevent value: internal/relay imports neither
	// internal/sessions nor internal/turnevent (sessions appears only transitively
	// via internal/control, so a go list -deps reading looks like a contradiction
	// and is not one), and protocol is already imported, so the payload crosses the
	// boundary with no new import and no cycle (matching the five seams above —
	// define the dependency where it is consumed).
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id
	// — it holds connID, state, resp, send, recv, device, interactive and peerStatic
	// — so there is nothing to key on at connect time; each payload self-identifies
	// by its own conversation_id. RetainedModelLists, OutstandingQuestions and
	// RetainedSlashCommandLists state the same reasoning.
	//
	// Order is not part of the contract, and a caller MUST correlate a roster by its
	// conversation_id rather than by its position in the returned slice — the
	// envelope id this path stamps is fixed and non-load-bearing for the same
	// reason.
	//
	// BOUNDED TIME, like every seam the manager calls on its Run goroutine: an
	// implementation that blocks stalls Run and with it every conn the manager
	// services. ModalResolver's doc block states the same obligation, and it is not
	// hypothetical here — the #2079 producer walks a conversation registry under
	// that registry's mutex, which is exactly the shape that can block.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many payloads are returned, not on
	// any row's text — because the bound is decided upstream at construction
	// (BackgroundTaskRosterPayload.DroppedTasks on the aggregate,
	// BackgroundTask.TruncatedFields per row, over internal/streamsup's
	// maxTaskRosterEntries and maxTaskRosterDescription caps). A second cap here
	// would be a second place the limit is decided and the two could disagree
	// silently, so the obligation stays the producer's. Note that those per-payload
	// bounds do NOT bound how many payloads can be returned at once; on this path
	// pushQueue's byte ceiling is the backstop, and a cardinality cap, if one is ever
	// wanted, belongs to the producer and not to either half of this reconcile. All
	// four strings a task row carries — TaskID, TaskType, Description and each entry
	// of TruncatedFields — are claude-authored, untrusted text (BackgroundTask's own
	// doc) and are NEVER logged on the reconcile path. Description is a literal
	// command line for the local_bash task type, which BackgroundTask's doc grades
	// as the more tempting shape of this family precisely because a LIST of command
	// lines invites being fed somewhere structured; it is safe to RENDER as inert
	// text and never to execute, re-shell, or feed to an HTML sink, an attribute or
	// a URL.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#2078 / foreground /
	// existing-test posture, the nil-resolver posture the other optional control
	// seams share. #2079 wires the daemon-side producer.
	RetainedBackgroundTaskRosters func() []protocol.BackgroundTaskRosterPayload

	// RunningTurnPhases enumerates the current phase of every running turn as
	// marshal-ready turn_state payloads (each stamped with its own
	// conversation_id) for connect-time reconcile (#2712) — the seventh Mode B
	// instance. A running turn's phase is control state, but turn_state otherwise
	// travels only on the Mode A event stream: a reconnect replays events after
	// hello.last_event_id, the turn's thinking was sent before the client left, and
	// the emitter de-duplicates transitions, so a long single-phase turn sends
	// nothing new. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened
	// conn only.
	//
	// "Current phase" is the thinking or responding the daemon last SENT for a
	// turn still open. An implementation MUST NOT derive a phase that was never
	// sent, and MUST NOT return an idle conversation: nothing is the idle answer,
	// which the client reads under the reset-on-reconnect rule. The cmd/pyry
	// producer (turnPhaseSnapshot) returns at most one payload today, because its
	// emitter holds at most one open turn.
	//
	// ORDERING OBLIGATION, which is why the reconcile runs after replayMissed
	// rather than beside its six twins: the producer must record a transition
	// before the emitter appends that transition's event to the replay ring and
	// asks ActiveConns for the fan-out. Then a turn ending while a conn opens
	// either reads as ended here, or its idle carries an event id above the conn's
	// replayThrough and reaches the conn live, behind the reconciled frame.
	//
	// A pure read in bounded time (the producer takes one leaf mutex and does no
	// I/O). The payload carries only a server-minted conversation_id and a phase
	// from turnbridge's closed vocabulary — no claude-authored text.
	//
	// Optional: nil ⇒ no reconcile, the posture the six seams above share.
	RunningTurnPhases func() []protocol.TurnStatePayload

	// ReplySuggestions enumerates the producer's current suggested-reply state as
	// marshal-ready reply_suggestion payloads for connect-time reconcile (#2830) —
	// the eighth Mode B instance. One payload per conversation that has state; a
	// conversation whose suggestion was cleared is returned with SuggestedReply nil,
	// because the clear is real state a reconnecting client must apply, not an
	// absence. Called on the Run goroutine from handleNoiseInit's interactive-open
	// tail; the payloads are unicast to the just-opened conn only.
	//
	// A pure read in bounded time. It runs on Run, so it MUST NOT call anything
	// that round-trips through Run (ActiveConns), or the handshake deadlocks; a
	// leaf mutex over the producer's map is the expected shape. Ordering against a
	// live publish racing this read needs nothing from the producer beyond its
	// per-conversation revision: forwardEnvelope drops a reply_suggestion at or
	// below the highest revision already delivered for that conversation on that
	// conn (replySuggestionStale).
	//
	// SuggestedReply is claude-derived text, so the reconcile never logs a payload.
	//
	// Optional: nil ⇒ no reconcile, the posture the seven seams above share.
	ReplySuggestions func() []protocol.ReplySuggestionPayload
}
