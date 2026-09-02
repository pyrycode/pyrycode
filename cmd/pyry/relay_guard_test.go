package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// Structural guard: every inbound client→daemon v2 wire message type MUST have
// a registered handler on one of the two inbound v2 dispatch surfaces. It exists
// because #949's promote_conversation had a type constant, a payload, and a
// registry op, but NO dispatch entry — so it fell through dispatch.Route to
// protocol.unsupported and save-as-channel silently failed on every client,
// caught by no test. Definition and registration live in unrelated files with
// nothing tying them together; this guard is that tie, for every future verb, at
// make-check time.
//
// The two inbound v2 dispatch surfaces (a verb correctly handled if in EITHER;
// a verb in NEITHER is the #949 failure mode):
//
//  1. Map-dispatched — the V2SessionConfig.Handlers map literal in relay.go,
//     consulted by dispatch.Route.
//  2. Switch-intercepted — the switch in V2SessionManager.dispatchAppFrame
//     (internal/relay/v2session.go), which runs before dispatch.Route.
//
// Rather than re-declare a shadow of that wiring (which could drift exactly the
// way the dispatch map did), the guard AST-reads the ACTUAL surfaces and compares
// them against a hand-classified inbound set whose totality is drift-guarded.
// Everything is keyed by CONSTANT NAME (the AST identifier, e.g.
// "TypePromoteConversation") — all three extractors and both hand-lists speak
// names, so no name↔value bridge is needed. No production code is touched: the
// switch and the map are read, never refactored.
//
// Adding a THIRD inbound dispatch surface in future: any verb dispatched only
// there will fail the coverage assertion (it appears inbound but registered in
// neither known surface), forcing you here — add an extractor for the new surface
// and union it into registeredTypes. That "loud + documented" path is AC #2.
//
// codes.go-home convention: allAppTypes assumes every application Type* constant
// lives in internal/protocol/codes.go (as it does today, and as compat_test.go
// already assumes — its own type list breaks first if the convention breaks). The
// Noise-transport TypeNoise* constants live in v2envelope.go and are excluded by
// construction (this guard parses codes.go only). Keep new app constants in
// codes.go so they stay inside the totality tie below.

// Source paths, resolved relative to this package's directory (go test runs with
// CWD = package dir). If a surface moves, the parse fails loudly → update the
// guard.
const (
	codesPath      = "../../internal/protocol/codes.go"
	relayPath      = "relay.go"
	v2sessionPath  = "../../internal/relay/v2session.go"
	handlersField  = "Handlers"
	dispatchFnName = "dispatchAppFrame"
)

// inboundTypes classifies every client→daemon v2 request verb by the surface it
// is dispatched on. The value is the surface, for legibility in review — a verb
// mis-filed here is caught by the coverage/reverse ties below. (Source: the
// #950 spec audit against main.)
var inboundTypes = map[string]string{
	// Surface #1 — map-dispatched via dispatch.Route (relay.go Handlers map).
	"TypeSendMessage":           "map-dispatched",
	"TypeListConversations":     "map-dispatched",
	"TypeCreateConversation":    "map-dispatched",
	"TypePromoteConversation":   "map-dispatched",
	"TypeRenameConversation":    "map-dispatched",
	"TypeDeleteConversation":    "map-dispatched",
	"TypeArchiveConversation":   "map-dispatched",
	"TypeUnarchiveConversation": "map-dispatched",
	"TypeChangeWorkspace":       "map-dispatched",
	"TypeCreateWorkspaceFolder": "map-dispatched",
	"TypeRecentWorkspaces":      "map-dispatched",
	"TypeRegisterPushToken":     "map-dispatched",

	// Surface #2 — switch-intercepted before dispatch.Route (dispatchAppFrame).
	"TypeRekeyRequest":           "switch-intercepted",
	"TypeRequestSnapshot":        "switch-intercepted",
	"TypeModalAnswer":            "switch-intercepted",
	"TypeModalCancel":            "switch-intercepted",
	"TypeInterrupt":              "switch-intercepted",
	"TypeNewSession":             "switch-intercepted",
	"TypeDequeueMessage":         "switch-intercepted",
	"TypeRequestDebugBundle":     "switch-intercepted",
	"TypeSetSessionSettings":     "switch-intercepted",
	"TypeRequestSessionSettings": "switch-intercepted",
	"TypeQuestionAnswer":         "switch-intercepted",
	"TypeQuestionRefused":        "switch-intercepted",
	// The upload leg of the one BIDIRECTIONAL type here (#1897). It moved up from
	// excludedTypes' "pending handler" when dispatchAppFrame gained its case, the
	// move that entry named in advance. Filed on the same rule as its twelve
	// neighbours — the guard reads case SELECTORS, not case bodies, so a case that
	// tags the frame and hands it to the conn's appFrameWorker (where the upload's
	// hashing and writing run, off Run) registers exactly as an inline one does.
	"TypeAttachmentChunk": "switch-intercepted",
}

// excludedTypes classifies every non-inbound Type* constant with its reason, so
// a request verb mis-filed as reply/push/etc. is visually obvious in review.
var excludedTypes = map[string]string{
	// handshake — consumed on the Noise_IK v2 handshake path
	// (v2session_handshake.go), never reaches dispatchAppFrame. Borderline
	// (hello is phone→binary), called out explicitly per AC #4: classifying it
	// inbound would false-positive.
	"TypeHello": "handshake",

	// outbound reply — correlated to a request via in_reply_to.
	"TypeHelloAck":               "reply",
	"TypeAck":                    "reply",
	"TypeError":                  "reply",
	"TypeConversations":          "reply",
	"TypeConversationCreated":    "reply",
	"TypeConversationUpdated":    "reply",
	"TypeConversationDeleted":    "reply",
	"TypeWorkspaceFolderCreated": "reply",
	"TypeRecentWorkspacesList":   "reply",
	"TypeSessionSettingsUpdated": "reply",
	"TypeSessionSettings":        "reply",

	// outbound reply — the v2 attachment upload's success frame (#1895). Filed
	// here rather than beside its own bidirectional sibling below, and the label
	// is the same decision as the docs row and the payload's shape: correlation
	// rides the envelope's in_reply_to, so "reply" is literally the definition
	// this map gives above. TypeSessionSettingsUpdated is the shape copied — an
	// outbound v2 confirmation of an inbound control frame carrying only the id
	// it confirms — and the reject half of this same leg already correlates that
	// way (attachment.stream_aborted is a TypeError via in_reply_to), so a
	// success correlating differently would split one leg across two mechanisms.
	// Mandatory from the moment the constant exists rather than from the moment
	// something emits it (the producer is #1897): Assertion #3 reports an
	// unclassified constant, not an unemitted one. Unlike the chunk below this
	// frame has no inbound leg at all, so no entry ever moves to inboundTypes.
	"TypeAttachmentStored": "reply",

	// outbound push / event — binary→phone, never dispatched inbound.
	"TypeMessage":             "push",
	"TypeTurnState":           "push",
	"TypeAssistantDelta":      "push",
	"TypeToolUse":             "push",
	"TypeToolResult":          "push",
	"TypeTurnEnd":             "push",
	"TypeStall":               "push",
	"TypeApiRetry":            "push",
	"TypeCompacting":          "push",
	"TypeUnrecognizedMessage": "push",
	"TypeScreenSnapshot":      "push",
	"TypeResync":              "push",
	"TypeSessionTransition":   "push",
	"TypeModalShown":          "push",
	"TypeModalDismissed":      "push",
	"TypeQueueState":          "push",
	"TypeDebugBundleChunk":    "push",
	"TypeDebugBundleDone":     "push",
	"TypeSessionError":        "push",

	// outbound push — v2 background-task frames (#1393; the turnbridge mapping
	// landed in #1394). TestEveryInboundV2TypeHasHandler
	// enumerates every Type* constant in internal/protocol/codes.go, so an
	// outbound-only type must be excluded here from the moment it exists.
	"TypeBackgroundTaskStarted": "push",
	"TypeBackgroundTaskUpdated": "push",
	"TypeBackgroundTaskRoster":  "push",

	// outbound push — the v2 thinking-progress reading (#1386). Outbound-only
	// like the three above, so it must be excluded here from the moment the
	// constant exists or Assertion #3 reports it unclassified.
	"TypeThinkingProgress": "push",

	// outbound push — the v2 usage-limit report (#1405). Outbound-only like the
	// four above, and mandatory here from the moment the constant exists rather
	// than from the moment something emits it (the turnbridge mapping is #1410) —
	// Assertion #3 reports an unclassified constant, not an unemitted one.
	"TypeRateLimited": "push",

	// outbound push — the v2 announced-model report (#1616). Outbound-only like
	// the five above, and mandatory here from the moment the constant exists
	// rather than from the moment something emits it (the turnbridge mapping is
	// #1638) — Assertion #3 reports an unclassified constant, not an unemitted
	// one.
	"TypeModelAnnounced": "push",

	// outbound push — the v2 model-list report (#1704). Outbound-only like the six
	// above, and mandatory here from the moment the constant exists rather than
	// from the moment something emits it (the turnbridge mapping is #1848, the
	// emission #1849) — Assertion #3 reports an unclassified constant, not an
	// unemitted one. It is a push and not a reply because this slice declares no
	// inbound request verb: an inbound type needs a handler in Handlers or
	// dispatchAppFrame or Assertion #1 fails, and filing a handler-less verb here
	// to dodge that would be a lie to the guard. It shipped as a PUSH rather than a
	// reply: #1849 emits it from interactiveTurnEmitterV2.Handle on the interactive
	// turn lane, so this entry stays "push". A later ticket picking request/reply
	// would still have to declare the verb together with its handler.
	"TypeModelList": "push",

	// outbound push — the v2 slash-command-list report (#1726). Outbound-only like
	// the seven above, and mandatory here from the moment the constant exists
	// rather than from the moment something emits it (the producer is #1720) —
	// Assertion #3 reports an unclassified constant, not an unemitted one. It is a
	// push and not a reply because this slice declares no inbound request verb: an
	// inbound type needs a handler in Handlers or dispatchAppFrame or Assertion #1
	// fails, and filing a handler-less verb here to dodge that would be a lie to
	// the guard. If #1720 picks request/reply, the verb and its handler land
	// together and this entry becomes "reply".
	"TypeSlashCommandList": "push",

	// TypeAttachmentChunk is NO LONGER HERE. It sat here as "pending handler"
	// while #1752 had declared the constant and nothing dispatched it; #1897 added
	// the dispatchAppFrame case, so it moved up to inboundTypes as
	// "switch-intercepted", beside the question and modal arms. That move was
	// mandatory rather than tidy-up — Assertion #2 fails a wired type still
	// sitting in this map. Its old entry credited #1744, which was split into
	// #1895/#1896/#1897 and no longer exists as work; the note below carries the
	// same repair.
	//
	// The reason it was excluded rather than filed as a push is still worth
	// keeping, because the frame is the map's only BIDIRECTIONAL one: upload
	// rides it client→daemon and retrieval rides it daemon→client, so "push" and
	// the reason its neighbours give for it ("this slice declares no inbound
	// request verb") would both have been false. Its retrieval leg (#1746) adds
	// no entry anywhere — an outbound use of an already-inbound type needs none.

	// outbound push — the v2 clarifying-question batch (#1962). Outbound-only like
	// the eight pushes above rather than bidirectional like the attachment chunk,
	// and mandatory here from the moment the constant exists rather than from the
	// moment something emits it (the producer is #1973) — Assertion #3 reports an
	// unclassified constant, not an unemitted one. It is a push and not a reply
	// because this slice declares no inbound request verb: an inbound type needs a
	// handler in Handlers or dispatchAppFrame or Assertion #1 fails, and filing a
	// handler-less verb here to dodge that would be a lie to the guard. If the
	// inbound answer (#1907) picks request/reply, the verb and its handler land
	// together and that entry is filed then.
	"TypeQuestionShown": "push",

	// outbound push — the question dismissal that retires a batch (#1974). Same
	// classification as the batch above and for the same reason: outbound-only,
	// no inbound leg at all, so "push" is true rather than borrowed. Mandatory
	// from the moment the constant exists — the producer is #1973 and the answer
	// half #1907, and Assertion #3 reports an unclassified constant, not an
	// unemitted one.
	"TypeQuestionDismissed": "push",

	// pending handler — the v2 attachment RETRIEVAL REQUEST verb (#2052). Filed
	// under its own true label rather than borrowed from a neighbour, following
	// TypeQuestionAnswer / TypeQuestionRefused (which sat here as "pending handler
	// (#1984)") and TypeAttachmentChunk before them. Both alternatives are wrong in
	// a checkable way: inboundTypes fails Assertion #1, which requires an inbound
	// type to be wired into the Handlers map or dispatchAppFrame, and this slice
	// ships no dispatch; "push" is false for a frame that is exclusively inbound,
	// so filing it there to dodge Assertion #1 would be a lie to the guard.
	// Mandatory from the moment the constant exists rather than from the moment
	// something dispatches it — Assertion #3 reports an unclassified constant, not
	// an unhandled one. #2054 adds the dispatchAppFrame case, at which point
	// Assertion #2 FORCES this entry up to inboundTypes as "switch-intercepted";
	// a wired type left in this map fails.
	"TypeRequestAttachment": "pending handler (#2054)",

	// The v2 question answer and refusal are NOT here. They were, as "pending
	// handler (#1984)" while #1983 had declared the constants and nothing
	// dispatched them; #1984 added the dispatchAppFrame cases, so both moved up to
	// inboundTypes as "switch-intercepted", beside the analogous modal_answer /
	// modal_cancel. That move was mandatory rather than tidy-up — Assertion #2
	// fails a wired type still sitting in this map. TypeAttachmentChunk made the
	// same move one step later, in #1897.
}

func TestEveryInboundV2TypeHasHandler(t *testing.T) {
	t.Parallel()

	allAppTypes := toSet(appTypeConstNames(t, codesPath))
	mapHandlerTypes := toSet(mapLiteralKeys(t, relayPath, handlersField))
	switchTypes := toSet(switchCaseSelectors(t, v2sessionPath, dispatchFnName))

	// registeredTypes is the real coverage: the union of both surfaces.
	registeredTypes := map[string]bool{}
	for name := range mapHandlerTypes {
		registeredTypes[name] = true
	}
	for name := range switchTypes {
		registeredTypes[name] = true
	}

	// Assertion #4 (surface-located, or loud): the extractors already Fatal if a
	// surface cannot be located; a non-empty result is the last belt on that. If
	// either surface ever reads empty, the coverage guard would vacuously pass —
	// fail instead so a moved/renamed surface is caught, not hidden.
	if len(mapHandlerTypes) == 0 {
		t.Fatalf("Handlers map in %s yielded no keys — did the wiring move? Update the guard.", relayPath)
	}
	if len(switchTypes) == 0 {
		t.Fatalf("dispatchAppFrame switch in %s yielded no cases — did the wiring move? Update the guard.", v2sessionPath)
	}

	// Assertion #1 (the point, AC #1): every inbound type is registered on some
	// surface. A member in neither is the #949 promote_conversation failure class.
	if missing := notIn(keysOf(inboundTypes), registeredTypes); len(missing) > 0 {
		for _, name := range missing {
			t.Errorf("%s is an inbound v2 type (%s) but is registered in NEITHER the Handlers "+
				"map nor the dispatchAppFrame switch — the #949 promote_conversation failure class.",
				name, inboundTypes[name])
		}
	}

	// Assertion #2 (reverse tie): anything wired into either surface is by
	// definition an inbound request, so a wired type absent from inboundTypes is
	// either a real bug or a missing classification. Together with #1 this pins
	// inboundTypes == registeredTypes on a healthy main.
	if extra := notIn(keysOf(registeredTypes), toSet(keysOf(inboundTypes))); len(extra) > 0 {
		for _, name := range extra {
			t.Errorf("%s is wired into an inbound dispatch surface but is not classified in "+
				"inboundTypes — add it (it needs no handler check, it HAS one) or fix the wiring.", name)
		}
	}

	// Assertion #3 (totality tie, AC #2/#3): every app constant is classified in
	// exactly one of inboundTypes / excludedTypes — no silent new types.
	for name := range allAppTypes {
		_, in := inboundTypes[name]
		_, ex := excludedTypes[name]
		switch {
		case in && ex:
			t.Errorf("%s is in BOTH inboundTypes and excludedTypes; the classification must be disjoint.", name)
		case !in && !ex:
			t.Errorf("%s is unclassified — mark it inbound (needs a handler) or excluded (with a reason).", name)
		}
	}
	// The inverse: a classified name with no matching app constant (a typo, or a
	// constant that was removed from codes.go).
	for _, name := range append(keysOf(inboundTypes), keysOf(excludedTypes)...) {
		if !allAppTypes[name] {
			t.Errorf("%s is classified but is not an application Type* constant in %s — typo, or a removed/moved constant?", name, codesPath)
		}
	}
}

// Structural guard: the connect-time question reconcile's daemon-side source
// (#1980) must stay bound to the SAME questionbridge registry the stream-approval
// bridge's surfacer records into, through the registry's non-retiring read. It
// exists because startRelayV2 has no test and cannot cheaply get one
// (TestBootstrapSnapshotUsage's doc comment records that), so without a gate that
// reads the wiring instead of constructing it, deleting the assignment or
// repointing it at a freshly-minted registry would ship a reconcile that
// enumerates an empty store — silently, on every client, caught by no test.
//
// It is a first-of-its-kind guard rather than a copy of a working one: none of the
// three sibling seams is pinned at its assignment today. Grepping the twin field
// names suggests otherwise and should not be believed — TestOutstandingQueues_*
// and TestRetainedModelLists_* exercise the adapter functions outstandingQueues
// and retainedModelLists directly, and every one of them stays green if the
// matching line is deleted from the V2SessionConfig literal.
//
// Bare presence is not enough, which is why three facts are tied together rather
// than one asserted. A gate that only checks the field appears as a key stays
// green against `OutstandingQuestions: questionbridge.New().Snapshot` — a second
// registry, always empty, nothing ever recording into it:
//
//  1. The sole OutstandingQuestions element binds a method value on a plain
//     identifier, and the method is Snapshot. A call expression as the receiver is
//     a second registry; Resolve or Lookup in place of Snapshot is a consuming read
//     that would retire a batch at reconcile time and break "answerable exactly
//     once" in the opposite direction — the batch would stop being answerable at
//     the moment it was re-sent.
//  2. questionbridge.New() is called exactly once in the file, so a second registry
//     cannot exist here at all, and the identifier it defines is that receiver.
//  3. The surfacer's question arm is fed that same identifier.
//
// Together those pin "one registry, minted once, read without retiring, shared by
// the producer and the reconcile". The extractors below are new rather than calls
// into mapLiteralKeys: that one matches a field whose value is a composite map
// literal and runs every key through a protocol.Type* check, and this assignment is
// a plain selector expression. parseGoFile and relayPath are the reusable parts.
//
// Every extractor Fatals with an "update the guard" message when the shape it
// expects is absent, following this file's convention — a moved surface must be
// loud, never vacuously green.
func TestOutstandingQuestionsWiredToSurfacerRegistry(t *testing.T) {
	t.Parallel()

	// Parsed once and shared, unlike the coverage guard's per-file helpers above:
	// all three facts live in relay.go, so three parses would buy nothing.
	file := parseGoFile(t, relayPath)

	registry, read := configSeamSelector(t, file, relayPath, questionSeamField)

	// Fact #1b (AC #2): the non-retiring current-truth read, not a consuming one.
	// Snapshot leaves the batch parked, so a batch outstanding across a reconnect is
	// re-sent and stays answerable exactly once (Registry.Resolve's one-shot consume
	// is what governs that, and this path never calls it), and a batch resolved while
	// the client was away is absent from the read rather than re-sent.
	if read != questionSeamRead {
		t.Errorf("%s is bound to %s.%s, want %s.%s — %s is the registry's non-retiring "+
			"current-truth read; a consuming read would retire the batch at reconcile time, "+
			"making a re-sent batch unanswerable rather than answerable exactly once.",
			questionSeamField, registry, read, registry, questionSeamRead, questionSeamRead)
	}

	// Fact #2 (AC #1): the producer and the reconcile share one instance. This is the
	// half bare presence cannot carry — a seam reading a registry nothing records into
	// enumerates an empty store on every connect and reconciles nothing, forever.
	//
	// Checked BEFORE the mint count below, deliberately. Any mutant that points the seam
	// at a second registry trips both, and soleRegistryMint has to Fatal (it returns a
	// name), so whichever runs first is the only one that speaks. This is the assertion
	// that names the actual failure; the call-count arithmetic below is the backstop.
	surfaced := fieldAssignIdent(t, file, relayPath, surfacerQuestionField)
	if surfaced != registry {
		t.Errorf("the surfacer's .%s arm is fed %q but %s reads %q — the reconcile would "+
			"enumerate a registry the producer never records into.",
			surfacerQuestionField, surfaced, questionSeamField, registry)
	}

	// Fact #3 (AC #1): exactly one registry is minted in this composition root, so a
	// second one cannot exist here even unwired, and the seam reads the one that does.
	minted := soleRegistryMint(t, file, relayPath, questionBridgePkg, questionBridgeMint)
	if minted != registry {
		t.Errorf("%s reads %s.%s, but the sole %s.%s() in %s defines %q — the seam is pointed "+
			"at something other than the daemon-singleton registry.",
			questionSeamField, registry, read, questionBridgePkg, questionBridgeMint, relayPath, minted)
	}
}

// Names the question-reconcile guard reads relay.go for. Kept beside the guard
// rather than in the const block above, which addresses the dispatch-coverage
// surfaces.
const (
	questionSeamField     = "OutstandingQuestions"
	questionSeamRead      = "Snapshot"
	questionBridgePkg     = "questionbridge"
	questionBridgeMint    = "New"
	surfacerQuestionField = "questions"
)

// configSeamSelector returns the receiver identifier and method name of the sole
// composite-literal element keyed fieldName, asserting the value is a method value
// on a plain identifier (`reg.Method`). Anything else — a call expression receiver,
// a closure, a nil — is Fatal: those are the shapes that would leave the seam
// pointed at a registry the producer never touches, or at no registry at all.
func configSeamSelector(t *testing.T, file *ast.File, path, fieldName string) (recv, method string) {
	t.Helper()
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			return true
		}
		found++
		sel, ok := kv.Value.(*ast.SelectorExpr)
		if !ok {
			t.Fatalf("%s in %s is bound to a %T, want a method value on the registry identifier "+
				"(reg.Method) — update the guard if the wiring shape changed deliberately.",
				fieldName, path, kv.Value)
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			t.Fatalf("%s in %s selects on a %T rather than a plain identifier — a registry "+
				"constructed inline here is not the one the surfacer records into.",
				fieldName, path, sel.X)
		}
		recv, method = ident.Name, sel.Sel.Name
		return true
	})
	if found != 1 {
		t.Fatalf("found %d %q elements in %s, want exactly 1 — the connect-time question "+
			"reconcile (#1979) has no daemon-side source, so a client that connects while "+
			"claude is waiting on a question is never sent it (#1980).", found, fieldName, path)
	}
	return recv, method
}

// soleRegistryMint asserts pkg.fn() is called exactly once in the file and that the
// call is a plain short variable declaration, returning the identifier it defines.
// Counting the calls and the declarations separately is what makes an inline
// `pkg.fn().Method` fail here too: it raises the call count without adding a
// declaration.
func soleRegistryMint(t *testing.T, file *ast.File, path, pkg, fn string) string {
	t.Helper()
	calls := 0
	var defined []string
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isPkgCall(call.Fun, pkg, fn) {
			calls++
			return true
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !isPkgCall(call.Fun, pkg, fn) {
			return true
		}
		if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
			defined = append(defined, ident.Name)
		}
		return true
	})
	if calls != 1 || len(defined) != 1 {
		t.Fatalf("found %d %s.%s() call(s) and %d short-var-decl(s) binding one in %s, want "+
			"exactly 1 of each — a second registry in this composition root is one the surfacer "+
			"never records into, so a seam reading it would reconcile an empty store forever.",
			calls, pkg, fn, len(defined), path)
	}
	return defined[0]
}

// fieldAssignIdent returns the identifier fed to the sole `<x>.field = <ident>`
// assignment in the file.
func fieldAssignIdent(t *testing.T, file *ast.File, path, field string) string {
	t.Helper()
	var names []string
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != field {
			return true
		}
		ident, ok := assign.Rhs[0].(*ast.Ident)
		if !ok {
			t.Fatalf("the .%s assignment in %s is fed a %T rather than a plain identifier — "+
				"update the guard.", field, path, assign.Rhs[0])
		}
		names = append(names, ident.Name)
		return true
	})
	if len(names) != 1 {
		t.Fatalf("found %d `.%s =` assignments in %s, want exactly 1 — did the surfacer's "+
			"question arm move or rename? Update the guard.", len(names), field, path)
	}
	return names[0]
}

// isPkgCall reports whether fun is the selector `pkg.name`.
func isPkgCall(fun ast.Expr, pkg, name string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

// toSet builds a set from a slice of names, deduping.
func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// keysOf returns the keys of a string-keyed map (values ignored).
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// notIn returns the sorted, deduped members of want that are absent from have,
// so a multi-type regression reports every offender at once.
func notIn(want []string, have map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range want {
		if !have[name] && !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
	}
	sort.Strings(out)
	return out
}

// appTypeConstNames returns every Type* constant identifier declared in the given
// file (codes.go). A parse failure or an empty result is fatal — the guard cannot
// verify coverage without the constant universe.
func appTypeConstNames(t *testing.T, path string) []string {
	t.Helper()
	file := parseGoFile(t, path)
	var names []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if strings.HasPrefix(name.Name, "Type") {
					names = append(names, name.Name)
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("found no Type* constants in %s — did codes.go move? Update the guard.", path)
	}
	return names
}

// mapLiteralKeys returns the constant names used as keys of the composite-literal
// map assigned to fieldName (Handlers) in the given file. It asserts exactly one
// such literal and that every key is a protocol.Type* selector — any other shape
// is a wiring change the guard must be taught about.
func mapLiteralKeys(t *testing.T, path, fieldName string) []string {
	t.Helper()
	file := parseGoFile(t, path)
	var keys []string
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		ident, ok := kv.Key.(*ast.Ident)
		if !ok || ident.Name != fieldName {
			return true
		}
		lit, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return true
		}
		found++
		for _, elt := range lit.Elts {
			ekv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("%s map in %s has a non key/value element (%T) — update the guard.", fieldName, path, elt)
			}
			keys = append(keys, protocolTypeSelName(t, path, fieldName, ekv.Key))
		}
		return true
	})
	if found != 1 {
		t.Fatalf("found %d %q map literals in %s, want exactly 1 — did the wiring move? Update the guard.", found, fieldName, path)
	}
	return keys
}

// switchCaseSelectors returns the constant names used as case labels of the sole
// switch statement in funcName (dispatchAppFrame). It asserts the function and
// exactly one switch are found and that every case label is a protocol.Type*
// selector; the default clause (empty label list) contributes nothing.
func switchCaseSelectors(t *testing.T, path, funcName string) []string {
	t.Helper()
	file := parseGoFile(t, path)
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == funcName {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatalf("function %q not found in %s — did it move or rename? Update the guard.", funcName, path)
	}
	var selectors []string
	switches := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		switches++
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, label := range cc.List {
				selectors = append(selectors, protocolTypeSelName(t, path, funcName, label))
			}
		}
		return true
	})
	if switches != 1 {
		t.Fatalf("found %d switch statements in %s (%s), want exactly 1 — did the interception switch change shape? Update the guard.", switches, funcName, path)
	}
	return selectors
}

// protocolTypeSelName extracts the Sel name from an expression that must be a
// protocol.Type* selector, Fatal-ing on any other shape so an unrecognized key
// or case expression is loud rather than silently dropped.
func protocolTypeSelName(t *testing.T, path, where string, expr ast.Expr) string {
	t.Helper()
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("%s in %s has a non-selector element (%T); want protocol.Type* — update the guard.", where, path, expr)
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "protocol" || !strings.HasPrefix(sel.Sel.Name, "Type") {
		t.Fatalf("%s in %s references %v, not a protocol.Type* constant — update the guard.", where, path, sel)
	}
	return sel.Sel.Name
}

// parseGoFile parses a single Go source file for structural inspection. A parse
// failure is fatal: the guard cannot verify coverage against a file it can't read.
func parseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return file
}
