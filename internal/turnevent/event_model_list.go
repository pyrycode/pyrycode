package turnevent

// ModelOption is one entry of a ModelList, not an Event. Its claude-authored
// fields are exactly protocol.ModelOption's, in that type's declaration order.
//
// The capture's other per-entry keys are not on the producer's decode target:
// description, supportsFastMode, supportsAdaptiveThinking and supportsEffort have
// no consumer, and protocol.ModelOption carries none of them. supportsEffort
// would add nothing, since a nil EffortLevels already says what supportsEffort
// false or absent would say.
type ModelOption struct {
	// ResolvedModel is what Value resolves to now: the concrete identifier, and
	// the field to use for a dated one. Verbatim per ModelAnnounced.Model's rule.
	// It need not be dated or appear in any published list: the capture's
	// claude-sonnet-5, claude-opus-5 and claude-fable-5 are undated.
	ResolvedModel string
	// Value is the argument passed to select this model: an alias (sonnet), a
	// bracketed variant (claude-fable-5[1m]) or default, not a dated identifier.
	// A consumer cannot derive a family by splitting it on "-" or assume it
	// round-trips; see protocol.ModelOption.Value for the inbound gap and why
	// internal/relay's validModel is not widened to close it.
	Value string
	// DisplayName is claude's human label for the entry ("Default
	// (recommended)", "Haiku 4.5"): prose to render as inert text, never a key to
	// match on. Bounded and not sanitized, so the client's render boundary owes
	// the sanitization, as protocol.ModelOption's SECURITY paragraph states for
	// all three strings.
	DisplayName string
	// EffortLevels are the reasoning-effort levels claude published for this
	// model, so a client's effort control offers exactly what claude accepts.
	// Verbatim per ModelAnnounced.Model's rule, in claude's own order (low,
	// medium, high, xhigh, max in the capture, which is not sorted). Bounded and
	// valid UTF-8 only, under DisplayName's security reading.
	//
	// Bounded at construction in both dimensions: each level by streamsup's
	// maxModelEffortLevel, the count by maxModelEffortLevelCount, since the
	// array length is claude's to choose.
	//
	// An absent key, a JSON null and a published [] are one reading, nil;
	// streamsup's emitModelList normalises the zero-length list at construction.
	// The daemon has no fallback effort vocabulary, so every reading gives the
	// same instruction: there is no menu to offer. The distinction would not
	// survive the wire either (protocol.ModelOption.MarshalJSON sends [] for
	// nil), and slices.Equal(nil, []string{}) is true, so the usual assertions
	// could not see it. Only Go's spelling of zero is normalised; elements and
	// order stay claude's. Full argument:
	// streamsup-package-the-per-entry-byte-budget-s-third-dimension.md.
	//
	// When TruncatedFields names "effort_levels", a level was cut, the count cap
	// shortened the list, or both. The list is then not claude's published menu
	// and must not be offered as one; the true level count is not recoverable
	// from this event.
	//
	// internal/relay's validEffort is not applied to this list and must not be
	// widened or narrowed to match it. It bounds a phone-supplied value on an
	// inbound path: applying it here would drop a level claude adds, and widening
	// it to claude's list would let the subprocess extend what an untrusted
	// inbound frame may set.
	EffortLevels []string
	// SupportsAutoMode is claude's answer to whether it accepts auto permission
	// mode for this model, so a client's permission-mode menu can grey the option
	// out. Verbatim, with no daemon policy folded in.
	//
	// An absent key, a JSON null and false are one reading, false. That is the
	// safe direction for a grant: "claude refused" and "claude said nothing" both
	// grey the option out, and granting on silence would be the unsafe collapse.
	// claude has never been seen to send false, and
	// protocol.ModelOption.SupportsAutoMode makes the same collapse, so a *bool
	// would carry nothing to the wire.
	//
	// It is a report to a client's menu, never an authorization input: nothing in
	// the daemon may branch on it to decide what to send claude, since that would
	// be a subprocess authorizing itself. A bool is never cut, so it never appears
	// in TruncatedFields.
	SupportsAutoMode bool
	// TruncatedFields names this entry's cut fields ("resolved_model", "value",
	// "display_name", "effort_levels") per
	// BackgroundTaskStarted.TruncatedFields. "effort_levels" reports either of
	// that list's two cuts, at most once.
	TruncatedFields []string
}

// ModelList is claude's inventory of selectable models: the models array of the
// initialize reply, which the daemon solicits once per child with streamsup's
// WriteInitialize and claude answers on a control_response line. It answers
// what can be run, before the first turn, with each alias's current
// resolution.
//
// It is an Event rather than parser-held state so it reaches the wire on the
// same path as every other interactive payload: turnbridge.MapEvent maps it
// onto protocol.ModelListPayload, which takes its ConversationID at mapping
// time.
//
// Delivery: interactiveTurnEmitterV2.Handle emits it on the interactive lane,
// best-effort, since turnMarkFor answers turnMarkNone and the fan-in may drop it
// under load. cmd/pyry's sessionModelHold retains the value for the session's
// life, and the relay's connect-time reconcile (retainedModelLists) sends it to
// a client that connects later.
//
// The producer emits nothing for an ambiguous line rather than a list it did
// not observe: a missing menu beats a wrong one. It opens and closes no turn and
// is not per turn: one initialize exchange per child produces one.
type ModelList struct {
	// Models is the inventory in claude's order, reduced to one row per family:
	// the producer drops each pinned row whose family row is present and keeps
	// only the newest pinned row of a family that has none
	// (internal/modelfamily.Reduce, applied before the entry cap), because a pyry
	// session follows a family's latest model. No ranking is invented and no
	// surviving row is changed. Never empty: the producer does not emit an empty
	// array, and the reduction never empties a list.
	//
	// Three dimensions, all bounded at construction. Per-entry text by
	// streamsup's maxModelResolved, maxModelValue, maxModelDisplayName and
	// maxModelEffortLevel, reported in ModelOption.TruncatedFields. The entry
	// count by maxModelListEntries, truncated from the tail and reported as
	// DroppedModels. The per-entry level count by maxModelEffortLevelCount,
	// reported as "effort_levels" on that entry.
	Models []ModelOption
	// DroppedModels counts entries past the producer's cap that this event does
	// not carry; 0 when none. len(Models) + DroppedModels is the reduced list's
	// size; rows the family reduction removed are not counted, since a rule
	// removed them rather than the size cap. It reports here rather than in a
	// top-level TruncatedFields for BackgroundTaskRoster.DroppedTasks' reason, and
	// it is daemon-derived.
	DroppedModels int
}
