package streamsup

import (
	"encoding/json"
	"strings"
	"sync"
)

// controlResponseSuccess is the ONE response.subtype whose payload this parser
// will read. Byte-exact equality against a DAEMON-authored constant, never a fold
// or a prefix: it is the SHARED PRECONDITION of both of emitModelList's emits, and
// what refuses to read an inventory out of a response reporting FAILURE. It was
// HALF OF A CONJUNCTION until #1891, the other half being a non-empty models array;
// since that slice each array decides its own event below this test, so this
// constant gates both emits and neither array gates the other — emitModelList's
// THE DISCRIMINANT states the lattice that replaced the conjunction. Everything
// else — "error", a subtype claude invents later, an absent one — is not success
// and takes the nak rung, which is what makes that classification total.
const controlResponseSuccess = "success"

// controlResponseMsg is the ONE record every control_response produces, and the
// constants below are the closed set of reasons it carries. rateLimitDropMsg's
// shape and its argument: daemon-authored keywords, never claude's values, and one
// message string for the tests to filter on.
//
// The message is UNCHANGED from #1500 and every control_response still produces
// exactly one record; what #1811 added is `reason` and a `models` count on records
// that previously carried the type alone, and what #1812 added beside them is a
// `dropped` count, so a shortened list is not read as a whole one. All three are
// daemon-authored — a keyword from this set and two integers — so the content-free
// discipline is intact, and the side
// benefit is the gap consumeLine's own arm recorded as accepted: a NAK is no longer
// indistinguishable from a success in the log.
//
// #1890 added the fifth keyword and NARROWED the second. `ack` used to answer every
// non-emitting success, which folded two different payloads under one word: a success
// carrying neither array, and one carrying a slash-command inventory and no model
// list. Only the `commands` count told them apart on the record. The keyword now does,
// and the set below is FIVE rather than four.
//
// WHAT DECIDES THE NEW KEYWORD'S NAME is that it must name the payload's SHAPE rather
// than what the daemon did about it, and the constraint was two-sided: a name for the
// emit — `command_list`, parallel to `model_list` — was false in the state #1890
// shipped, where that rung emitted nothing, and a name spelling "ack" would have gone
// false the moment the rung started emitting. #1891 IS THAT MOMENT, and it arrived
// without touching this set: `commands_only` names the shape, so it survived the state
// change that each of the two rejected spellings would have failed on one side of. The
// set was decided ONCE, and the slice that added the emit re-authored a trailing
// comment rather than moving a keyword. That is why this block is the place the
// no-new-keyword boundary is READABLE rather than merely observed.
const (
	controlResponseMsg = "streamsup: consuming solicited control_response"

	controlResponseNAK          = "nak"           // subtype was not controlResponseSuccess
	controlResponseAck          = "ack"           // success, neither array on the line
	controlResponseUndecodable  = "undecodable"   // the nested shape did not decode
	controlResponseCommandsOnly = "commands_only" // success, a commands inventory and no model list; one turnevent.SlashCommandList emitted
	controlResponseModelList    = "model_list"    // one turnevent.ModelList emitted
)

func (p *Parser) endPermissionModeChild() {
	p.permissionModes.endChild()
}

func (p *Parser) registerPermissionMode(id, mode string) *pendingPermissionMode {
	return p.permissionModes.register(id, mode)
}

func (p *Parser) removePermissionMode(id string, pending *pendingPermissionMode) {
	p.permissionModes.remove(id, pending)
}

func (p *Parser) confirmedPermissionMode() (string, bool) {
	return p.permissionModes.confirmed()
}

// SetCanUseToolHandler installs h as the destination for claude's inbound
// can_use_tool permission asks (#2282). With none installed the parser consumes such
// a line and drops it silently.
//
// A SETTER RATHER THAN A CONSTRUCTOR PARAMETER because NewParser's signature has nine
// call sites and none of them answers an ask; the alternative was widening all nine to
// pass nil. PostureGate's doc makes the same trade in the other direction, for a value
// that had to exist ahead of both halves.
//
// CALL IT BEFORE THE PARSER IS HANDED TO A RUNNER, and that ordering is a CONTRACT,
// not advice. h is read on the os/exec forwarder goroutine, so installing one after
// the parser has started consuming stdout is a data race with no lock to save it. The
// composition root builds the parser, installs its holds, and only then wires it as
// Config.Stdout — the same window PostureGate() is read in.
//
// h IS CALLED SYNCHRONOUSLY, in stream order, on that forwarder goroutine — the same
// contract NewParser states for sink. It must not block: a handler that waits stalls
// every later line from the child, exactly as a blocking sink does. It must not call
// back into this parser. Answering the ask from inside it is safe, because the answer
// travels on the child's STDIN, a different fd from the stdout being drained here, so
// no write can deadlock against this reader.
//
// h receives untrusted, claude-authored data — see CanUseToolRequest, which says what
// that means for each field.
func (p *Parser) SetCanUseToolHandler(h func(CanUseToolRequest)) { p.canUseTool = h }

// noteControlAck releases the posture gate when line is the SUCCESS control_response
// for the request_id the gate is armed against, and opens it on no other input. AC 2 in
// full: a different id, a non-success subtype, or an undecodable line each leave the
// gate closed. A non-success subtype for the ARMED id is additionally RECORDED on the
// gate — still no release, and still not an event — so the turn path can distinguish
// claude's refusal from a round trip still in flight.
//
// The sibling id is real rather than hypothetical. RequestInitializeOnSpawn writes an
// initialize ask at the SAME spawn off the SAME counter, so its ack reaches this
// function too and is refused here by id alone.
//
// IT LOGS NOTHING, ON ANY PATH, and the decode error is the case that rule exists for:
// encoding/json QUOTES the offending input bytes into its error text, so `"err", err`
// would route claude's own strings into the daemon log through a channel no
// per-attribute check can see — emitModelList's undecodable arm argues this at length
// and the argument is not weaker here. Neither the request id nor any payload field is
// logged either. The one record every control_response produces remains
// logControlResponse's, written by emitModelList below this call.
func (p *Parser) noteControlAck(line []byte) {
	var ack controlAckLine
	if err := json.Unmarshal(line, &ack); err != nil {
		return
	}
	if ack.Response.Subtype != controlResponseSuccess {
		// A NAK is a DEFINITIVE answer, not silence, and dropping it here is what left a
		// NAK'd session refusing turns forever under the retryable classification. The
		// gate stays CLOSED — opening on a refusal is fail-open, and fatally so under
		// #2065 — and is merely marked, so the turn path can say which of the two closed
		// states it is in. Recovery is an operator's in-band posture change (retarget) or
		// the next spawn, never anything read off this line.
		//
		// An EMPTY subtype is not an answer and is deliberately excluded rather than
		// swept in with the refusals. The bound is "claude said something other than
		// success", not "claude said error": a subtype spelled differently in a later
		// version still terminates, while a line that names no subtype at all — a
		// truncated response, a shape this target does not model — leaves the gate in the
		// PENDING state, which is the safe classification for "the daemon does not know".
		// Only an answer may tell an operator that claude refused.
		if ack.Response.Subtype != "" {
			p.postureGate.refuse(ack.Response.RequestID)
		}
		return
	}
	p.postureGate.release(ack.Response.RequestID)
}

// noteConfirmedPermissionModeAck updates the informational current-child hold only
// for the exact successful SetPermissionMode response that echoes the requested
// mode. It logs nothing: request ids, modes, payloads, and decoder errors are all
// excluded from the daemon log.
func (p *Parser) noteConfirmedPermissionModeAck(line []byte) {
	var idLine permissionModeResponseIDLine
	if err := json.Unmarshal(line, &idLine); err != nil || idLine.Response.RequestID == "" {
		return
	}
	pending := p.permissionModes.take(idLine.Response.RequestID)
	if pending == nil || !pending.written() {
		return
	}

	var response permissionModeResponseLine
	if err := json.Unmarshal(line, &response); err != nil ||
		response.Response.Subtype != controlResponseSuccess ||
		response.Response.Response == nil ||
		response.Response.Response.Mode != pending.mode {
		return
	}
	p.permissionModes.confirmPending(pending)
}

// controlResponseLine is the decoded payload of one top-level control_response
// line. Kept separate from streamLine for systemTaskStartedLine's reason, and it
// is the family's first DOUBLE-nested target: the outer `response` is claude's
// wrapper carrying subtype and request_id, and its own `response` is the
// initialize payload the models array sits in.
//
// The nesting path is spelled out in full deliberately. The decode's input is the
// TOP-LEVEL line bytes, never a nested field — streamLine's doc states the
// property that preserves — so every level between the line and the array has to
// appear here, exactly as rateLimitEventLine spells rate_limit_info.
//
// request_id is deliberately absent FROM THIS TYPE, and so is `error`. Nothing
// correlates the id FOR CONTENT PROVENANCE — see emitModelList's provenance
// paragraph, whose conclusion is unchanged — and nothing reads the error string,
// which is claude's prose about a failure the daemon takes no action on; a field
// never declared cannot reach a log or an event, which is systemInitLine's
// argument for its own twenty-one omissions.
//
// CORRECTED 2026-09-03 (#2064): the id IS correlated now, on a DIFFERENT type
// (controlAckLine) answering a DIFFERENT question. That reader gates a locally-minted
// request the daemon is actively WAITING FOR; this type decides what a payload's
// CONTENT may be read as. The paragraph above was never about the former and does not
// pre-decide it. Context-usage replies now correlate content on their own
// `contextUsageResponseIDLine`, likewise without widening this model-list target.
//
// The separate type is also what keeps this one's decode behaviour fixed, and the
// reason is worth stating where the temptation is: adding `RequestID string` here
// would make a `request_id` that is not a JSON string fail the WHOLE-LINE decode, so a
// payload that reaches emitModelList's model-list rung today would newly land on its
// undecodable rung and emit nothing. Correlating on its own target makes that
// non-disturbance structural rather than argued.
//
// The initialize payload's other twelve top-level keys are absent for that same
// reason and one of them is why it matters: `account`. It is never decoded, never
// bounded, never retained and never logged, because it is not on this struct.
type controlResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response struct {
			Models []modelOptionLine `json:"models"`
			// A plain []commandEntryLine for Models' reason verbatim: absent, null and
			// empty are answered identically — a count of 0 — so a pointer would buy a
			// distinction nothing acts on.
			Commands []commandEntryLine `json:"commands"`
		} `json:"response"`
	} `json:"response"`
}

// controlAckLine is the decode target of the ACK CORRELATION (#2064), and it is
// deliberately a second, separate target rather than two fields added to
// controlResponseLine above — that type's doc states the rung-disturbance this
// separation makes structural.
//
// It declares `subtype` and `request_id` and NO PAYLOAD KEY AT ALL: not `mode`, which
// the ack does carry, not `models`, not `account`. Nothing from the payload can
// therefore reach the posture gate's decision, its path, or a log — the strongest
// form of systemInitLine's argument, since here the omission is total. The echoed
// mode is read separately by permissionModeResponseLine for the informational
// current-child hold; keeping that target separate prevents an invalid mode from
// changing this gate's established id/subtype decode.
//
// request_id sits on the INNER wrapper beside subtype, not top-level beside `type`.
// That is claude's own shape, captured verbatim in
// internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json, and
// it is the same inversion controlResponseLine's doc records for subtype.
//
// Its decode input is the TOP-LEVEL LINE BYTES like every sibling's, never a nested
// field: streamLine's doc states the property that preserves, and it is what stops a
// tool result whose text is literally a control shape from forging an ack and opening
// a gate the daemon is holding turns behind.
type controlAckLine struct {
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
	} `json:"response"`
}

// permissionModeResponseIDLine is the narrow first decode of a permission-mode
// acknowledgement. Retiring an exact id before decoding the echoed mode makes a
// matched malformed reply terminal instead of letting a later duplicate turn it
// into confirmation.
type permissionModeResponseIDLine struct {
	Response struct {
		RequestID string `json:"request_id"`
	} `json:"response"`
}

// permissionModeResponseLine declares only the success discriminator and echoed
// posture needed to confirm one pending SetPermissionMode write. Claude-authored
// error text and every sibling response field are structurally absent.
type permissionModeResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response *struct {
			Mode string `json:"mode"`
		} `json:"response"`
	} `json:"response"`
}

type pendingPermissionMode struct {
	mode       string
	generation uint64
	writeDone  chan struct{}
	writeOK    bool
	writeOnce  sync.Once
}

func newPendingPermissionMode(mode string, generation uint64) *pendingPermissionMode {
	return &pendingPermissionMode{
		mode:       strings.Clone(mode),
		generation: generation,
		writeDone:  make(chan struct{}),
	}
}

func (p *pendingPermissionMode) resolveWrite(ok bool) {
	p.writeOnce.Do(func() {
		p.writeOK = ok
		close(p.writeDone)
	})
}

func (p *pendingPermissionMode) written() bool {
	<-p.writeDone
	return p.writeOK
}

// confirmedPermissionModes is one child-scoped informational hold. Its mutex is a
// leaf: no method performs I/O, logging, channel operations, or another lock
// acquisition while holding it.
type confirmedPermissionModes struct {
	mu         sync.Mutex
	active     bool
	generation uint64
	mode       string
	available  bool
	pending    map[string]*pendingPermissionMode
}

func (m *confirmedPermissionModes) beginChild() {
	m.mu.Lock()
	m.generation++
	m.active = true
	m.mode = ""
	m.available = false
	clear(m.pending)
	m.mu.Unlock()
}

func (m *confirmedPermissionModes) endChild() {
	m.mu.Lock()
	m.generation++
	m.active = false
	m.mode = ""
	m.available = false
	clear(m.pending)
	m.mu.Unlock()
}

func (m *confirmedPermissionModes) confirmInit(mode string) {
	if mode == "" {
		return
	}
	m.mu.Lock()
	if m.active {
		m.mode = strings.Clone(mode)
		m.available = true
	}
	m.mu.Unlock()
}

func (m *confirmedPermissionModes) register(id, mode string) *pendingPermissionMode {
	m.mu.Lock()
	pending := newPendingPermissionMode(mode, m.generation)
	if m.pending == nil {
		m.pending = make(map[string]*pendingPermissionMode)
	}
	m.pending[id] = pending
	m.mu.Unlock()
	return pending
}

func (m *confirmedPermissionModes) take(id string) *pendingPermissionMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := m.pending[id]
	delete(m.pending, id)
	return pending
}

func (m *confirmedPermissionModes) remove(id string, pending *pendingPermissionMode) {
	m.mu.Lock()
	if m.pending[id] == pending {
		delete(m.pending, id)
	}
	m.mu.Unlock()
}

func (m *confirmedPermissionModes) confirmPending(pending *pendingPermissionMode) {
	m.mu.Lock()
	if m.active && pending.generation == m.generation {
		m.mode = strings.Clone(pending.mode)
		m.available = true
	}
	m.mu.Unlock()
}

func (m *confirmedPermissionModes) confirmed() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active || !m.available {
		return "", false
	}
	return strings.Clone(m.mode), true
}

// canUseToolSubtype is the one inbound control-request subtype this parser claims.
// Matched by EQUALITY against this daemon-authored constant, never by prefix and
// never case-folded, which is what keeps a near-miss spelling — a later
// can_use_tool_v2, a differently-cased variant — on the unrecognized lane where it
// belongs rather than silently landing in a decoder written for a shape it is not.
// permissionModeAllowed makes the same argument for the write direction.
const canUseToolSubtype = "can_use_tool"

// CanUseToolRequest is one inbound can_use_tool control request, decoded (#2282).
// claude writes it on the same stdout this parser reads when the child was launched
// with --permission-prompt-tool stdio, and it is the ask a client modal renders: far
// more than an approval MCP tool receives, which is the whole reason for reading it
// here.
//
// EVERY FIELD ON THIS TYPE IS CLAUDE-AUTHORED AND UNTRUSTED. That has to be said out
// loud because the field names read like daemon-authored UI strings — Title,
// DisplayName, Description — and a handler that renders one unescaped, or joins
// BlockedPath onto a filesystem path, is the foreseeable misuse. This type IS the
// trust boundary's marker: nothing outside the decode below constructs one, so a
// value of this type in hand means "subprocess bytes". No field is length-capped
// here, which is deliberate and bounded — see the parser's arm for why the cap
// belongs to whoever first puts these on the wire (#2286), and defaultMaxParseBuf
// for what bounds them in the meantime.
//
// FIELD TYPING FOLLOWS ONE RULE, and it is a consequence of provenance. The shapes
// come from the Agent SDK type definitions the ticket cites rather than from a line a
// live claude has been sent, and no fixture in this repo can check them. So a field
// the ticket's own phrasing pins to a scalar is declared as that scalar, and every
// field whose JSON shape is NOT pinned is json.RawMessage — which accepts any JSON
// and therefore cannot turn a wrong guess into a failed decode and a LOST PERMISSION
// ASK. A lost ask is the expensive failure here: claude waits for an answer that
// never comes.
//
// RequestID is tagged `json:"-"` because it is the ENVELOPE's field, not the inner
// request's, and canUseToolLine fills it after decoding. Declaring the 18 inner
// fields once on this type rather than twice across a wrapper and a payload struct is
// what that tag buys.
//
// DecisionReasonType is carried VERBATIM and is not validated against the eleven
// spellings the ticket lists. Deciding what an unrecognised reason means belongs to
// whoever renders or answers the ask; a vocabulary gate here would silently drop an
// ask on a spelling a later claude adds. The contrast with permissionModeAllowed is
// exact and worth holding: that gate bounds a value the daemon SENDS, where refusing
// an unknown spelling is the safe direction. This is a value the daemon RECEIVES,
// where refusing one throws away news.
type CanUseToolRequest struct {
	// RequestID is CLAUDE'S OWN correlation id, read off the envelope. An answer must
	// echo it — see WriteCanUseToolAllow — or claude cannot match the two.
	RequestID string `json:"-"`

	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	// AgentID is set only when the ask comes from inside a subagent.
	AgentID string `json:"agent_id"`

	// Input is the tool's arguments, carried BYTE-VERBATIM so an answerer can hand
	// them straight back as an allow's updatedInput without a re-encode changing a
	// single byte. Nothing here interprets them.
	Input json.RawMessage `json:"input"`
	// PermissionSuggestions is claude's proposed rule changes — what a client's
	// "don't ask again" is built from. Carried opaquely and byte-verbatim for Input's
	// reason; #2286 is the slice that interprets it.
	PermissionSuggestions json.RawMessage `json:"permission_suggestions"`

	// BlockedPath is a path claude reports IT refused. Never opened, stat-ed, joined
	// or canonicalised by anything in this package — it is a string to show, and a
	// consumer that later resolves it inherits the traversal question whole.
	BlockedPath string `json:"blocked_path"`

	// DecisionReason is why claude is asking, and DecisionReasonType classifies it.
	// The reason is json.RawMessage under the typing rule above: the ticket pins the
	// TYPE to one of eleven strings but says nothing about the reason's own shape, and
	// a structured reason beside a classifying enum is at least as likely as prose.
	DecisionReason     json.RawMessage `json:"decision_reason"`
	DecisionReasonType string          `json:"decision_reason_type"`
	// MatchedAskRule is the rule that produced the ask, shape unpinned by the ticket
	// and so carried opaquely under the same rule.
	MatchedAskRule json.RawMessage `json:"matched_ask_rule"`

	ClassifierApprovable    bool `json:"classifier_approvable"`
	SuppressAlwaysAllowRule bool `json:"suppress_always_allow_rule"`
	DefaultToNo             bool `json:"default_to_no"`
	RequiresUserInteraction bool `json:"requires_user_interaction"`

	// The three strings a modal shows. Untrusted like every field here.
	Title       string `json:"title"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// controlRequestSubtypeLine reads ONE question off an inbound control_request: which
// subtype it is. It exists as its own target for controlAckLine's reason verbatim — a
// field added to one target must not change another's decode outcome — and here that
// separation does concrete work: a can_use_tool payload this parser cannot decode
// must still be identifiable AS a can_use_tool, so the arm can ring the bell about
// the right thing rather than silently mistaking it for a stranger's subtype.
//
// The nesting inverts the top level exactly as controlResponseLine's does: subtype
// sits under `request`, so streamLine.Subtype decodes empty for this type and there
// is nothing for an emitSystemSubtype-shaped dispatch to match on.
type controlRequestSubtypeLine struct {
	Request struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
}

// canUseToolLine is the full decode target for a request already known to be a
// can_use_tool. Its input is the TOP-LEVEL LINE BYTES like every sibling's, never a
// nested field — streamLine's doc states the property that preserves, and it is what
// stops a tool result whose text is literally a control shape from being read as one.
type canUseToolLine struct {
	RequestID string            `json:"request_id"`
	Request   CanUseToolRequest `json:"request"`
}

// decodeCanUseTool decodes one inbound can_use_tool line, reporting whether it could.
// The envelope's request_id is copied onto the returned value, which is the one thing
// the nested decode cannot do for itself.
//
// IT LOGS NOTHING on the failure path, and that is the package's standing rule rather
// than a missed diagnostic: encoding/json QUOTES the offending input bytes into its
// error text, so `"err", err` would route claude's own strings into the daemon log
// through a channel no per-attribute check can see. noteControlAck states the rule at
// length and it is not weaker here. The caller reports the failure as an unrecognized
// line, which carries the raw bytes to the wire — where a truncation cap applies —
// rather than to the log.
func decodeCanUseTool(line []byte) (CanUseToolRequest, bool) {
	var decoded canUseToolLine
	if err := json.Unmarshal(line, &decoded); err != nil {
		return CanUseToolRequest{}, false
	}
	req := decoded.Request
	req.RequestID = decoded.RequestID
	return req, true
}

// logControlResponse writes emitModelList's ONE record, and it exists so the
// content-free rule is decided in a single place rather than on each of the rungs.
// Every control_response produces exactly one of these, whatever it was a reply to.
//
// Seven attributes and NOTHING else. `type` is a constant here rather than
// sl.Type, which the case arm's match makes byte-identical; `reason` comes from the
// closed keyword set at controlResponseMsg; `models` is the emitted entry count,
// `dropped` how many maxModelListEntries cut, and `levels_dropped` how many effort
// levels maxModelEffortLevelCount cut in TOTAL across the RETAINED entries — all
// three 0 on every rung but the model-list one, which is the only rung that reads a
// models array at all. `commands` and `commands_dropped` are that first pair one array
// over: the EMITTED slash-command count and how many maxSlashCommandListEntries cut,
// both 0 on every rung but the two that read the commands array. Levels belonging to
// entries `dropped`
// removed are not counted again there. No value, no resolvedModel, no displayName, no
// level string, no request_id, no error string,
// no unmarshal err, no line bytes. The three strings are precisely what #833's
// posture — restated across internal/relay's v2session_settings.go and
// internal/sessions' pool.go as "model / effort / YOLO values are NEVER logged at
// any level" — exists to keep out of a log, and a drop site explaining itself with
// the value it dropped is how that rule usually breaks. All four integers are
// DAEMON-computed and carry none of claude's bytes, which is what admits them where
// no string from the payload is admitted.
//
// `dropped` is here rather than omitted (#1812) for the reason the wire field's own
// doc argues about a permanent zero, one layer down: `models=6` on a reply that
// carried forty reads as "claude offers six models". emitBackgroundTaskRoster logs
// no count and does not oppose this — its only record is the UNDECODABLE drop, so it
// has no success record to complete, while this path has one and completing it is
// consistent. The entry count is no longer this record's alone:
// turnbridge.MapEvent's ModelList arm carries turnevent.ModelList.DroppedModels
// through verbatim (#1848) and cmd/pyry's interactiveTurnEmitterV2.Handle puts it
// on the wire as dropped_models (#1849), where
// protocol.ModelListPayload.DroppedModels documents it as client-facing. The record
// keeps its own reason, on two facts the wire field cannot supply. AN
// OPERATOR-FACING SIGNAL IS NOT A CLIENT-FACING ONE — the wire field tells a phone
// its menu is short, this record tells an operator, on the daemon's own timeline.
// And the wire field is not a RELIABLE observable of the cap: no conn need be
// interactive when the initialize exchange happens, and the live send is droppable
// at the fan-in, so a cap can fire with no frame reaching anyone. This record always
// exists. So a cap firing in production is still a cap no OPERATOR can know fired
// without it — that would take a phone having been connected and having reported
// back — and the first evidence that 10 is the wrong number would arrive as a user's
// short menu.
//
// `levels_dropped` is here for that argument VERBATIM, one dimension down, and here
// the record is still the only place the NUMBER appears at all.
// turnevent.ModelOption.TruncatedFields does reach a client — MapEvent's arm crosses
// it as the slice it is (#1848), and protocol.ModelOption.MarshalJSON deliberately
// exempts it so nothing-was-cut arrives as null — but what crosses is a NAME,
// "effort_levels", at most once per entry, saying the same thing whether one level
// was cut or ninety were dropped. The MAGNITUDE reaches nowhere else, which
// turnevent.ModelOption.EffortLevels' own "WHAT THAT GIVES UP" paragraph states
// from the other side: the true level count is not recoverable from the event, where
// ModelList's true entry count is recoverable as len(Models) + DroppedModels. The
// operator-versus-client and best-effort points from `dropped` above apply here
// unchanged. So without this the level bound would be a cap on subprocess-supplied
// data with no count anywhere, and the first evidence that maxModelEffortLevelCount
// is the wrong number would arrive as a user's short effort menu. It is admissible
// under this function's own rule for the same reason the other two counts are — a
// DAEMON-computed integer derived from slice lengths, carrying none of claude's
// bytes — and the level STRINGS it counts are exactly the "effort values are NEVER
// logged at any level" half of #833's posture, so none of them goes anywhere near
// this record.
//
// `commands` is the initialize payload's slash-command entry count (#1853), and its
// argument is `dropped`'s SIMPLER and STRONGER: this record is the ONLY observable
// that decode has. The operator-versus-client half does not transfer — `dropped`
// completes a client-facing wire field, and this number has no wire field to
// complete — which is NOT what changed when turnbridge.MapEvent's arm mapped
// `dropped` onto protocol.SlashCommandListPayload.DroppedCommands (#2001): that
// mapping gave THAT counter its wire field, and this one still has none. What it no
// longer lacks is an event, a daemon-internal value and a retention: since #1877 the
// entries this counts are copied into a turnevent.SlashCommandList and retained for
// that event's lifetime, each name under maxSlashCommandName. Without it a
// `commands` array that stopped decoding would be a change nobody could know
// happened. IT COUNTS WHAT WAS EMITTED, exactly as `models` does since #1826 gave this
// array an entry-count cap of its own. That is a CORRECTION rather than a widening: this
// paragraph argued for years that the two counts had different meanings, `models` being
// post-cut and `commands` being a raw decode count, and concluded from the absence of an
// entry-count cap that no seventh attribute was needed. maxSlashCommandListEntries
// falsified the premise, so the conclusion went with it and the two halves of this record
// now read alike. Nothing became unobservable
// either: a non-empty `commands` now always means emitted on BOTH rungs that read the
// array — the model-list one and the commands-only one (#1891) — and the rungs that
// emit nothing no longer rest on this count at all. #1890 took the keyword decision
// this sentence used to defer, so a commands-only success logs
// controlResponseCommandsOnly and the narrowed `ack` means "neither array". The
// separation is the KEYWORD's, and this count qualifies it rather than carrying it.
// Admissible on the other three counts' footing exactly — a DAEMON-computed
// integer derived from a slice length, carrying none of claude's bytes. No name, no
// argumentHint, no description and no alias reaches this record on any rung — and
// SINCE #1825 THE DISTINCTION BEHIND THAT SENTENCE IS DRAWN ONE LAST TIME, which is
// worth redrawing rather than renumbering. commandEntryLine declares ALL FOUR keys now,
// so NOTHING is UNREACHABLE by omission any more: the category that used to hold the
// undeclared keys — a value that never becomes a Go string cannot be written — is
// EMPTY. The description crossed out of it in #1904, the argument hint in #1957 and the
// aliases in #1825, so all four are UNWRITTEN, held in a decoded struct and in a
// constructed event, and kept out of this record by what this function chooses to log
// rather than by what the decode target can hold. Each slice moved one key across that
// line and the sentence above stayed true for a different reason each time, which is
// why it was redrawn rather than renumbered; this is the last redraw the sequence has,
// and what the sentence rests on from here is this function's own choice alone. That
// is the weaker of
// the two guarantees and it is the one #833's posture actually asks for elsewhere; on
// the cmd/pyry lane it is pinned deterministically rather than left advisory, by
// TestInteractiveTurnEmitterV2's log-leak negative over
// emitterSlashCommandListSentinels, whose own doc records that the enumeration does NOT
// grow with the field set and that each field-adding slice therefore owes it a
// sentinel. The record carries SEVEN attributes and five daemon-computed integers since
// #1826, and no decoded content on any rung.
//
// `commands_dropped` is how many entries maxSlashCommandListEntries cut, and it is
// `dropped`'s argument one array over: without it `commands=128` on a reply that carried
// a thousand reads as "the workspace offers 128 commands". It is the SEVENTH attribute
// the paragraph above spent years declining, and the decline was correct while its
// premise held — the premise was that no entry-count cap existed, so the decoded count
// was the emitted count and there was nothing to disambiguate. What it never rested on
// is a claim that seven attributes are too many.
//
// STILL NO ATTRIBUTE FOR THE ALIAS DROP, and that decline is UNAFFECTED because it rests
// on something else entirely. `levels_dropped` earns its place because the magnitude
// reaches nowhere else AND this record is the emit's only observable; the alias drop's
// report channel is per ENTRY and already exists, turnevent.SlashCommand.TruncatedFields
// naming "aliases", which is what that cardinality bound's honesty rests on. The
// threading objection that used to sit beside it — that a count computed inside
// emitSlashCommandList could not reach a record already written — is what the ENTRY-COUNT
// cut answers by living in emitModelList instead, and it answers it only for the count
// that is cut there. An alias drop is decided per entry inside the emitter's loop and
// would still have to be threaded back out.
//
// THE TWO COMMAND INTS ARE THE LAST TWO PARAMETERS AND THE LAST TWO ATTRIBUTES, so the
// two orders are one order a reader checks once. The record is now two pairs and a
// qualifier: `models` with `dropped`, `commands` with `commands_dropped`, and
// `levels_dropped` qualifying the first pair from inside its own dimension. Inserting
// either new int between the model trio would split a group that reads as a unit. Five
// adjacent ints is a swap hazard and it is PINNED rather than designed away, on the same
// rung the four-int version was pinned on: the controlResponseCommandsOnly one, where
// the model trio is all-zero and the command pair is not. Since #1826 that rung pins one
// swap more, the pair against itself, and only because the two members can differ — a
// fixture whose drop happens to equal its emitted count would make the pair
// self-symmetric and that swap invisible again.
//
// The attribute set is FIXED at seven on every rung, which is why a rung with no models
// array to describe passes 0 rather than omitting the key.
func (p *Parser) logControlResponse(reason string, models, dropped, levelsDropped, commands, commandsDropped int) {
	p.log.Debug(controlResponseMsg,
		"type", "control_response", "reason", reason, "models", models, "dropped", dropped,
		"levels_dropped", levelsDropped, "commands", commands, "commands_dropped", commandsDropped)
}
