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

	// pending handler — the v2 attachment chunk (#1752). Its own label rather
	// than one of the eight pushes above, because the frame is BIDIRECTIONAL:
	// upload rides it client→daemon and retrieval rides it daemon→client, so
	// "push" and the reason its neighbours give for it ("this slice declares no
	// inbound request verb") would both be false here. inboundTypes is wrong
	// too — this slice ships no dispatch, so Assertion #1 would fail it by
	// construction. Excluded under the reason that is actually true: the inbound
	// leg has no handler YET. #1744 adds the dispatchAppFrame case, at which
	// point this entry moves to inboundTypes as "switch-intercepted". TypeHello
	// above is the precedent — a borderline phone→binary type deliberately not
	// filed inbound, carrying its own label. Mandatory here from the moment the
	// constant exists: Assertion #3 reports an unclassified constant, not an
	// unemitted one.
	"TypeAttachmentChunk": "pending handler (#1744)",

	// outbound push — the v2 clarifying-question batch (#1962). Outbound-only like
	// the eight pushes above rather than bidirectional like the attachment chunk,
	// and mandatory here from the moment the constant exists rather than from the
	// moment something emits it (the producer is #1927) — Assertion #3 reports an
	// unclassified constant, not an unemitted one. It is a push and not a reply
	// because this slice declares no inbound request verb: an inbound type needs a
	// handler in Handlers or dispatchAppFrame or Assertion #1 fails, and filing a
	// handler-less verb here to dodge that would be a lie to the guard. If #1927
	// picks request/reply, the verb and its handler land together and this entry
	// becomes "reply".
	"TypeQuestionShown": "push",
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
