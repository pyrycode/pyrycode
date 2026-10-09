package main

import (
	"context"
	"errors"
	"slices"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/modelfamily"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// poolResolver adapts *sessions.Pool to control.SessionResolver. The shapes
// differ only in the return type: Pool.Lookup returns *sessions.Session,
// SessionResolver.Lookup returns control.Session (an interface satisfied
// structurally by *sessions.Session). Go's lack of covariant return types on
// interface satisfaction is the only reason this adapter exists.
type poolResolver struct{ p *sessions.Pool }

func (r poolResolver) Lookup(id sessions.SessionID) (control.Session, error) {
	return r.p.Lookup(id)
}

func (r poolResolver) ResolveID(arg string) (sessions.SessionID, error) {
	return r.p.ResolveID(arg)
}

// sessionMinter adapts *sessions.Pool to handlers.SessionCreator. It narrows the
// type (Pool returns sessions.SessionID; the handler interface speaks plain
// string, keeping internal/relay/handlers free of an internal/sessions import)
// and owns the cmd-layer validation of the phone-requested spawn workdir:
// resolveSpawnDir confines + trust-marks a set spawnDir before it reaches
// Pool.Mint, which uses the resolved realpath verbatim (#685). A rejected
// spawnDir wraps handlers.ErrSpawnDirRejected and short-circuits before any
// mint. The precedent for this type-narrowing seam is poolResolver above.
//
// It mints WITHOUT spawning (#2085). The conversation's session is still bound
// and persisted at create_conversation, exactly as before; only the child is
// deferred, to the first message on that conversation — where the drain's
// boundSession.Activate brings it up on the same lazy path an idle-evicted
// conversation already takes. That is what lets a model, an effort or a
// permission mode chosen before the first message be simply what the child
// launches with, including one cleared back to claude's own default, which
// cannot be expressed in-band at all.
//
// SECURITY: the deferral widens the validated-then-spawn window from
// milliseconds to "whenever the operator sends". The decision, recorded in this
// ticket's spec, is to ACCEPT it rather than re-validate at the spawn site.
// resolveSpawnDir returns trustMark's own realpath and that value is frozen onto
// the session at build time — no phone-influenced state is re-read between the
// check and the chdir, so the phone gains nothing from the wait. Winning the
// window means replacing an ancestor of an already-resolved realpath under the
// operator's $HOME, which needs the $HOME write access the confinement exists to
// protect and claude itself already holds. This is a widening of the residual
// TOCTOU that conversation-session-binding.md already records as accepted, not a
// new class of exposure. It differs from the #1487 revive path — which DOES
// re-run resolveSpawnDir at its own spawn site — because that path re-reads a
// raw, persisted conv.Cwd from a mutable file across a daemon restart, so its
// stored value is unvalidated bytes from a previous process lifetime.
type sessionMinter struct {
	p *sessions.Pool
	// saved is the daemon's persisted model vocabulary, the same third source
	// settingsUpdaterAdapter checks against (#2665), so a create and a later
	// set_session_settings accept exactly the same values. nil is two sources.
	saved savedModelVocabulary
}

// Create satisfies handlers.SessionCreator. The ctx is discarded because neither
// half of this can observe one: resolveSpawnDir takes no context.Context, and
// Pool.Mint is ctx-free by contract because it cannot spawn. The parameter stays
// for the seam's shape, which the handler shares with its cancellable siblings.
//
// The honest consequence is that the handler's mint budget cannot interrupt this
// — a wedged filesystem blocks in a syscall regardless of any deadline. See
// handlers.createConversationMintTimeout, which records the same thing rather
// than claiming a protection it no longer provides.
//
// It answers with resolved alongside the id so the handler records exactly the
// folder the session spawns in (#2568) — one resolver on the create path, no
// second one to drift from it.
//
// agent is the handler-validated protocol.AgentClaude or protocol.AgentCodex
// (#2647), passed to the pool as the harness: the wire's agent names are the
// harness names (sessions.HarnessClaude, harnessCodex), the equality
// handlers.AgentOf already reads the other way.
//
// settings (#2665) replace what the mint would give, field by field, and are
// checked first — before resolveSpawnDir trust-marks anything and before the
// mint — by the membership checks settingsUpdaterAdapter.UpdateSettings runs,
// against agent's own entries (ADR 039). There is no bound session yet, so the
// vocabulary is read unbound. An effort is checked against the model the
// session will start on: the requested one, else the mint's. That model and the
// minted one are the same snapshot of Pool.MintDefaults, so an operator change
// racing the create cannot validate against one model and mint on another.
//
// A requested Claude model is resolved to its family first (followFamily), so a
// pinned id is checked, stored and spawned as the family the menu offers.
func (m sessionMinter) Create(_ context.Context, label, spawnDir, agent string, settings handlers.CreateSettings) (string, string, error) {
	// Only model and effort are ever copied, so no create can mint a posture.
	base := m.p.MintDefaults(agent)
	start := sessions.SessionSettings{Model: base.Model, Effort: base.Effort}
	if settings.Model != nil {
		start.Model = followFamily(agent, *settings.Model)
	}
	if settings.Effort != nil {
		start.Effort = *settings.Effort
	}
	checkModel := settings.Model != nil && *settings.Model != ""
	checkEffort := settings.Effort != nil && *settings.Effort != ""
	if checkModel || checkEffort {
		list, have := agentModelVocabulary(m.p, m.saved, agent, "")
		if checkModel {
			if err := validateModelVocabulary(list, have, start.Model); err != nil {
				return "", "", err
			}
		}
		if checkEffort {
			if err := validateEffortVocabulary(agent, list, have, start.Model, start.Effort); err != nil {
				return "", "", err
			}
		}
	}

	resolved, err := resolveSpawnDir(spawnDir)
	if err != nil {
		return "", "", err
	}
	id, err := m.p.MintWith(label, resolved, agent, start)
	return string(id), resolved, err
}

// sessionHarness answers the agent a pool session runs, live or dormant, for the
// list_conversations reply (#2643). A session the pool does not hold is a miss,
// which the handler reads as claude.
func sessionHarness(pool *sessions.Pool) handlers.SessionHarnessFunc {
	return func(sessionID string) (string, bool) {
		harness, err := pool.HarnessFor(sessions.SessionID(sessionID))
		return harness, err == nil
	}
}

// settingsUpdaterAdapter adapts *sessions.Pool to relay.SettingsUpdater (#845,
// model-vocabulary validation #2281).
// It narrows the type (relay speaks relay.SettingsUpdate / relay.ErrSessionUnknown
// so internal/relay imports neither internal/sessions nor cmd/pyry) and owns the
// sessions.ErrSessionNotFound → relay.ErrSessionUnknown mapping and the retained
// model-vocabulary decision — the
// project convention that sentinel-to-wire mapping lives at the consumer call
// site, not in the primitive. The four presence pointers pass straight through:
// relay.SettingsUpdate mirrors sessions.SettingsUpdate 1:1, so a nil field still
// means "leave unchanged" and a nil YOLO can never enable bypass. The precedent
// for this type-narrowing seam is sessionMinter / poolResolver above.
//
// A non-empty model is checked before Pool.UpdateSettings against the entries of
// the session's own agent (#2629): Claude's through the same
// retainedModelVocabulary used for client publication, Codex's families from the
// store. A non-empty effort is checked against the levels the model the session
// will run advertises. This adapter is the one layer that can see both cmd-side
// retention and the sessions primitive without inverting either package
// dependency. Empty retains its restart-to-default meaning and skips membership.
//
// The mirror is maintained BY HAND, so a field added on one side and forgotten
// here compiles and ships as a silent no-op. That is what
// TestSettingsUpdaterAdapter_CarriesPermissionMode exists to catch, and why it
// asserts on a REJECTED mode: the pool validates a posture only when the update
// names one, so a dropped field returns nil rather than an error.
type settingsUpdaterAdapter struct {
	p *sessions.Pool
	// saved is the daemon's persisted model vocabulary (#2450), the third source
	// retainedModelVocabulary reads when neither hold holds anything. It is carried
	// here rather than re-derived so this gate and the two client-facing seams in
	// relayWiring answer from the SAME three sources — a membership check that saw
	// fewer sources than the menu the client was offered would refuse a model that
	// menu had just advertised. nil is a daemon built without a store and is two
	// sources, not an error.
	saved savedModelVocabulary
}

func (a settingsUpdaterAdapter) UpdateSettings(id string, u relay.SettingsUpdate) error {
	sessionID := sessions.SessionID(id)
	checkModel := u.Model != nil && *u.Model != ""
	checkEffort := u.Effort != nil && *u.Effort != ""
	if checkModel || checkEffort {
		// Check membership only for a session the daemon actually has a record of.
		// Apart from preserving session.not_found precedence, this prevents an
		// unknown id from probing whether a vocabulary is complete or which agent
		// runs it — which is why HarnessFor must stay AHEAD of the vocabulary read
		// below rather than being folded into the write.
		// Pool.Lookup("") deliberately resolves the bootstrap session for legacy
		// internal callers, while neither pool write accepts anything but an exact
		// map key. Preserve the update seam's unknown-session behavior before
		// consulting the vocabulary.
		if sessionID == "" {
			return relay.ErrSessionUnknown
		}
		// The session's agent, live or dormant (#2629): a model and an effort are
		// checked against that agent's entries, since a capability belongs to the
		// agent and the model together.
		harness, err := a.p.HarnessFor(sessionID)
		if err != nil {
			return relay.ErrSessionUnknown
		}
		list, have := agentModelVocabulary(a.p, a.saved, harness, id)
		// Validate the published row while storage and execution follow its family.
		// u is this call's copy, so the caller's frame is untouched.
		if checkModel {
			offered := offeredModel(harness, list, *u.Model)
			u.Model = &offered
		}
		if checkModel {
			if err := validateModelVocabulary(list, have, *u.Model); err != nil {
				return err
			}
		}
		if checkEffort {
			// The model the session will run after this update: the frame's own
			// when it names one (an empty one included, which has no entry), else
			// the stored one.
			var model string
			if u.Model != nil {
				model = *u.Model
			} else if model, err = a.storedModel(sessionID); err != nil {
				return err
			}
			if err := validateEffortVocabulary(harness, list, have, model, *u.Effort); err != nil {
				return err
			}
		}
		if checkModel {
			family := followFamily(harness, *u.Model)
			u.Model = &family
		}
	}

	update := sessions.SettingsUpdate{
		Model:          u.Model,
		Effort:         u.Effort,
		YOLO:           u.YOLO,
		PermissionMode: u.PermissionMode,
	}

	// TWO WRITES, LIVE FIRST, EACH WITH ITS OWN MISS (#2463) — resolveBoundRunSettings'
	// composition for the read half (#2449), applied to the write. A session the pool
	// holds is written through Pool.UpdateSettings; one the daemon has only a persisted
	// record of is merged into that entry by Pool.UpdateDormantSettings, which is what
	// the first message will then revive it under. Only an id in neither half is
	// unknown. Before this the dormant case fell through to the refusal, and since
	// Pool.New materialises just the bootstrap, that was EVERY conversation after a
	// daemon restart — a model or effort picked before the channel's first message
	// simply did not land.
	//
	// The order is not interchangeable, for the reason the read's twin records: live
	// first means a session the pool holds is never written into a stale persisted
	// entry, and that is a guarantee of this function rather than merely of the pool's
	// bookkeeping (Pool.materialise retires the dormant entry it takes over, so the
	// halves partition — but a composition that asked in the other order would depend
	// on that staying true forever).
	//
	// ONLY ErrSessionNotFound falls through. Every other error from the live write —
	// an unsupported mode, a contradicting posture pair, a failed save — is that
	// session's answer and is returned as it is today; retrying such a frame against
	// the dormant half would be asking a second writer to re-judge a verdict already
	// reached.
	//
	// A revive can land between the two writes and retire the entry. p.dormant only
	// ever shrinks, so the id can only move that way and the dormant write finds a
	// clean miss rather than a torn entry; session.not_found is then the correct
	// answer, since the settings reached nothing. No retry — the operator's next pick
	// goes through the live half.
	if err := a.p.UpdateSettings(sessionID, update); !errors.Is(err, sessions.ErrSessionNotFound) {
		return err
	}
	err := a.p.UpdateDormantSettings(sessionID, update)
	// Both sentinels become session.not_found, which is a deliberate decision and not
	// a lost distinction: a dormant session cannot store a posture (a revive does not
	// restore one, #1487), and the user-visible outcome is identical either way — the
	// client's menu snaps back. A distinguishable code is wire vocabulary plus client
	// work, and no client has been observed mis-reading this one. The two stay
	// separate sentinels INSIDE internal/sessions for the reason
	// ErrDormantPostureUnsupported records; it is this seam that collapses them.
	if errors.Is(err, sessions.ErrSessionNotFound) || errors.Is(err, sessions.ErrDormantPostureUnsupported) {
		return relay.ErrSessionUnknown
	}
	return err
}

// storedModel is the model id is stored with, live or dormant, mapping a miss in
// both halves to relay.ErrSessionUnknown. Live first, the write's order: an id
// that a revive moves between the two reads misses both, and session.not_found
// is then the write path's answer for the same race too.
func (a settingsUpdaterAdapter) storedModel(id sessions.SessionID) (string, error) {
	if s, err := a.p.SettingsFor(id); err == nil {
		return s.Model, nil
	}
	s, err := a.p.DormantSettingsFor(id)
	if err != nil {
		return "", relay.ErrSessionUnknown
	}
	return s.Model, nil
}

// followFamily resolves a Claude model to its family alias, so a session follows
// the latest model of a family whichever spelling the client sent: claude-opus-5
// and claude-opus-4-7 both become opus. Menu identity is resolved separately by
// offeredModel; stored and executed Claude settings use the family spelling.
// Any other agent's model passes through unchanged (#2647). An empty agent is
// claude, as it is to the pool.
func followFamily(agent, model string) string {
	if agent != "" && agent != sessions.HarnessClaude {
		return model
	}
	return modelfamily.Alias(model)
}

// validateModelVocabulary classifies one non-empty client model against the same
// retained menu used for publication. A complete untruncated row set can prove
// absence; a missing list, a dropped row, or a cut Value cannot. Exact match is
// checked first because a present full row proves availability even when a
// different row was lost or truncated.
//
// Only ModelOption.Value participates. Neither the requested value nor any menu
// value is included in the returned sentinels, so callers can log the outcome
// without disclosing model vocabulary.
func validateModelVocabulary(list turnevent.ModelList, have bool, model string) error {
	if model == "" {
		return nil
	}
	if !have || len(list.Models) == 0 {
		return relay.ErrModelVocabularyUnavailable
	}
	for _, option := range list.Models {
		if option.Value == model && !slices.Contains(option.TruncatedFields, "value") {
			return nil
		}
	}
	if list.DroppedModels > 0 {
		return relay.ErrModelVocabularyUnavailable
	}
	for _, option := range list.Models {
		if slices.Contains(option.TruncatedFields, "value") {
			return relay.ErrModelVocabularyUnavailable
		}
	}
	return relay.ErrModelNotOffered
}

// fallbackEffortLevels is the set a Claude model with no entry accepts (#2629):
// the five levels relay's closed set held before, so a session whose model is
// unlisted, empty or not yet reported keeps what it had. A fresh slice per call,
// so a caller holding the capability list cannot widen the check.
func fallbackEffortLevels() []string {
	return []string{"low", "medium", "high", "xhigh", "max"}
}

// codexCommonEffortLevels is the set a Codex model with no entry accepts
// (#2666): the levels every held family advertises, in the first family's order,
// none when no family is held. Claude's five would let max reach turn/start on a
// model that refuses it. A cut level list can only shrink the intersection, so
// truncation never widens it.
func codexCommonEffortLevels(families []turnevent.ModelOption) []string {
	if len(families) == 0 {
		return nil
	}
	var common []string
	for _, level := range families[0].EffortLevels {
		if !slices.ContainsFunc(families[1:], func(f turnevent.ModelOption) bool { return !slices.Contains(f.EffortLevels, level) }) {
			common = append(common, level)
		}
	}
	return common
}

// effortLevelsFor answers the levels a non-empty effort is accepted from for
// model on harness (#2629, #2646): the EffortLevels of the entry list advertises
// for it, none when it advertises none, else that agent's fallback set. An entry
// represents the same family/variant with an uncut Value. A cut value is not
// the model's name, and cut levels cannot prove a level absent, so either falls
// through to the fallback set
// rather than refusing on partial evidence. It is both the check and the reported
// list, which is what keeps a session's effort_levels from drifting from what is
// accepted.
func effortLevelsFor(harness string, list turnevent.ModelList, have bool, model string) []string {
	if have && model != "" {
		model = offeredModel(harness, list, model)
		for _, option := range list.Models {
			if option.Value != model || slices.Contains(option.TruncatedFields, "value") || slices.Contains(option.TruncatedFields, "effort_levels") {
				continue
			}
			return option.EffortLevels
		}
	}
	if harness == harnessCodex {
		if !have {
			return nil
		}
		return codexCommonEffortLevels(list.Models)
	}
	return fallbackEffortLevels()
}

// validateEffortVocabulary classifies one non-empty client effort against the
// levels effortLevelsFor answers for model. Neither the effort nor any level is in
// the returned sentinel.
func validateEffortVocabulary(harness string, list turnevent.ModelList, have bool, model, effort string) error {
	if effort == "" || slices.Contains(effortLevelsFor(harness, list, have, model), effort) {
		return nil
	}
	return relay.ErrEffortNotOffered
}

// Capabilities answers the agent-and-model half of sessionID's capability list
// for relay.V2SessionConfig.CapabilitiesFor (#2646), from the SAME reads
// UpdateSettings checks against: the session's agent from HarnessFor, that
// agent's entries from agentModelVocabulary, and the effort set from
// effortLevelsFor for model, the session's stored model. Models are the Values
// validateModelVocabulary's exact-match loop accepts (a row whose value was cut is
// not listed), empty but non-nil when the vocabulary is unavailable. false for an
// id with no record or an agent this daemon does not know.
//
// Interrupt and MidTurnInput report what the daemon does today. Both agents
// interrupt (sessions.Runner.Interrupt, codexRunner.Interrupt). Only Claude takes
// input mid-turn: send_queued_now writes a queued message into its running turn
// (newSendNowDeliver, #2729) and the operator's push lands where claude read it
// (sendNowPlacement, #2730). newSendNowDeliver refuses Codex, whose WriteUserTurn
// starts a new turn.
//
// SlashCommands, MCPServers and ContextUsageDetail (#2670) are true only for
// Claude: codexRunner implements none of slashCommandLister, mcpStatusQuerier
// or contextUsageQuerier, so those resolvers refuse every Codex session.
// TestSettingsUpdaterAdapter_CapabilityFlagsMatchResolvers pins each flag to
// its resolver's assertion.
func (a settingsUpdaterAdapter) Capabilities(sessionID, model string) (relay.AgentCapabilities, bool) {
	if sessionID == "" {
		return relay.AgentCapabilities{}, false
	}
	harness, err := a.p.HarnessFor(sessions.SessionID(sessionID))
	if err != nil || (harness != sessions.HarnessClaude && harness != harnessCodex) {
		return relay.AgentCapabilities{}, false
	}
	list, have := agentModelVocabulary(a.p, a.saved, harness, sessionID)
	models := []string{}
	if have {
		for _, option := range list.Models {
			if !slices.Contains(option.TruncatedFields, "value") {
				models = append(models, option.Value)
			}
		}
	}
	claude := harness == sessions.HarnessClaude
	return relay.AgentCapabilities{
		Interrupt:          true,
		MidTurnInput:       claude,
		SlashCommands:      claude,
		MCPServers:         claude,
		ContextUsageDetail: claude,
		EffortLevels:       slices.Clone(effortLevelsFor(harness, list, have, model)),
		Models:             models,
	}, true
}
