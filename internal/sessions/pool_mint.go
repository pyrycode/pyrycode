package sessions

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"
)

// Create mints a fresh session, persists it, and brings it up under the
// cap-aware spawn path. Returns the new SessionID and an error.
//
// The returned id is empty only when the failure happened before (or during)
// the persist step — in that case nothing is on disk and nothing in memory
// changed. A non-empty id means the registry entry is on disk; the caller
// uses errors.Is to decide whether the failure was ErrPoolNotRunning (no
// lifecycle goroutine yet — fix and retry by calling Run + Activate) or an
// Activate error (lifecycle goroutine is running and may transition to
// active anyway — see ctx-cancellation note below).
//
// Sequence: NewID → build *Session in stateEvicted → register under p.mu and
// persist (rollback the in-memory entry on save failure) → schedule sess.Run on
// Pool.Run's errgroup via supervise → call Pool.Activate (cap-aware) to wake the
// lifecycle goroutine. Everything up to and including supervise is Pool.Mint;
// this is that plus the Activate.
//
// We persist BEFORE activating: a save failure with claude already running
// would leave an unsupervised orphan whose JSONL has no on-disk record. A
// registry-only entry that didn't activate is benign — the same shape as a
// session that ran and then idled out, recoverable on next Activate.
//
// Concurrency: safe for concurrent use. Each call serialises through Pool.mu
// briefly (registration + persist), then runs supervise/Activate off-lock
// under the cap path's existing capMu serialisation.
//
// Note: if ctx is cancelled after supervise succeeded but before Activate
// returns, the lifecycle goroutine still observes the buffered activate
// signal and may transition to active anyway. The lifecycle goroutine
// respects the pool's run-context, not the caller's. Tests should not
// assume "Activate returned ctx.Err → claude is not running."
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error) {
	return p.CreateIn(ctx, label, "")
}

// CreateIn is Create with an explicit per-session spawn working directory.
// spawnDir == "" spawns in the shared template workdir (tpl.WorkDir),
// byte-identical to Create. A non-empty spawnDir is used verbatim and is NOT
// validated, canonicalised, or trust-checked by the pool — callers supply a
// pre-resolved path (see #685). An inaccessible directory surfaces at spawn
// time via the supervisor's existing chdir-failure path, not here.
//
// Otherwise identical to Create: see its docstring for the full create
// sequence, concurrency, and error semantics. The register-and-supervise half
// lives in Mint; this is that plus the cap-aware Activate, and the split is
// what lets the relay's per-conversation mint defer the spawn to the first
// message while the control plane's `sessions new` verb keeps bringing its
// session up (#2085).
func (p *Pool) CreateIn(ctx context.Context, label, spawnDir string) (SessionID, error) {
	id, err := p.Mint(label, spawnDir)
	if err != nil {
		return id, err
	}
	if err := p.Activate(ctx, id); err != nil {
		return id, err
	}
	return id, nil
}

// Mint registers a fresh session — mint an id, build it, persist it, schedule
// its lifecycle goroutine — WITHOUT spawning claude. The returned session is in
// the evicted state with its goroutine parked on the activate signal, which is
// byte-for-byte the shape an idle-evicted session has, so the child comes up on
// the caller's first Pool.Activate through the existing lazy-respawn path. It
// adds no lifecycle path; it only stops one step short of Create's.
//
// The absent context.Context is the API signal for that, as it is on Revive:
// Mint does no blocking work and cannot spawn.
//
// It exists because create_conversation must bind a session id to a fresh
// conversation without starting a process for a discussion nobody has spoken in
// — and, the load-bearing half, so that every per-session setting chosen before
// the first message is simply what the child launches with, instead of an
// in-band correction applied to an already-running child (#2085).
//
// spawnDir is the per-session spawn working directory, used verbatim and NOT
// validated, canonicalised, or trust-checked by the pool — callers supply a
// pre-resolved, $HOME-confined realpath, exactly as CreateIn, GetOrCreateIn and
// Revive require (#685/#696). spawnDir == "" spawns in the shared template
// workdir. SECURITY: it is a phone-influenced workspace path, so it must never
// be logged (the #741 precedent for session ids and conversation ids); nothing
// here logs, and nothing added here should.
//
// A minted session carries mintSettings — the operator's configured model and
// effort, never the bypass (#1575). That is the one thing separating it from
// Revive, which inherits nothing.
//
// Returns:
//   - id, nil — registered, persisted, and scheduled
//   - "", err — the failure was at or before the persist; nothing is on disk and
//     nothing in memory changed
//   - id, ErrPoolNotRunning — the registry entry IS on disk but no lifecycle
//     goroutine was scheduled (fix and retry by calling Run + Activate)
//
// Concurrency: safe for concurrent use. Each call serialises through Pool.mu
// briefly (registration + persist), then supervises off-lock.
//
// It mints a claude session; MintAs is the same for any harness.
func (p *Pool) Mint(label, spawnDir string) (SessionID, error) {
	return p.MintAs(label, spawnDir, HarnessClaude)
}

// MintAs is Mint for a given harness (#2647), which buildSessionAs carries onto
// the session and saveLocked persists on its registry entry, so a restart revives
// it as the same agent. The pool does not validate the harness: the injected
// factory decides which harnesses have a runner, and refusing one fails the mint
// as an ordinary runner-construction error with nothing registered.
//
// Only a claude session starts from mintSettings. The operator's configured model
// and effort are claude's, so any other harness starts with none and runs at its
// own defaults.
func (p *Pool) MintAs(label, spawnDir, harness string) (SessionID, error) {
	return p.MintWith(label, spawnDir, harness, p.MintDefaults(harness))
}

// MintDefaults answers the settings MintAs starts a session of harness on: a
// claude session starts at the operator's configured model and effort (#1575),
// any other harness at its own defaults (#2647). It is the one definition of
// what a mint gives, read by MintAs and by a caller composing settings for
// MintWith (#2665), so the two cannot drift. Posture is never included — see
// mintSettings.
//
// Since #2492 mintSettings is an already-locked reader, so the RLock is taken
// here rather than inside it, and released before any build, which must stay off
// p.mu. The window that leaves — a concurrent Pool.UpdateSettings on the
// BOOTSTRAP between this read and a mint's registration — is the pre-existing,
// benign one every lock-releasing settings accessor carries: it changes what a
// new session inherits, never what an existing one holds.
func (p *Pool) MintDefaults(harness string) SessionSettings {
	if canonicalHarness(harness) != HarnessClaude {
		return SessionSettings{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mintSettings()
}

// MintWith is MintAs starting the session on settings, used verbatim (#2665):
// create_conversation's requested model and effort are the session's stored
// settings from the mint, so its first turn carries them with no second write
// that a refusal could leave half done. Callers pass no posture — a mint is
// never a bypass grant — which the one caller outside this package guarantees by
// copying only Model and Effort.
//
// This entry point does NOT go through materialise and needs none of its #2492
// machinery: id comes fresh from NewID, so it can name no dormant entry and no
// dormant write can race it.
func (p *Pool) MintWith(label, spawnDir, harness string, settings SessionSettings) (SessionID, error) {
	harness = canonicalHarness(harness)
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("sessions: create id: %w", err)
	}

	sess, err := p.buildSessionAs(id, label, spawnDir, settings, harness, "")
	if err != nil {
		return "", err
	}

	// Persist before activating: if saveLocked fails, roll the in-memory
	// registration back so a retry sees a clean slate. See Create's docstring
	// rationale ("save failure with claude running would leave an
	// unsupervised orphan").
	p.mu.Lock()
	p.sessions[id] = sess
	if err := p.saveLocked(); err != nil {
		delete(p.sessions, id)
		p.mu.Unlock()
		// Discarding the session discards its settings file too (#1518): in the
		// data dir that orphan is permanent, and id came fresh from NewID above so
		// it is never reused. Safe HERE and deliberately not in materialise's
		// rollbacks, where the id is caller-supplied and a concurrent same-id
		// builder may already own the byte-identical path.
		_ = os.Remove(sess.settingsPath)
		// The prompt file is discarded on the same terms and for the same reason,
		// with confidentiality on top: it holds the operator's text (#2150).
		_ = os.Remove(sess.systemPromptPath)
		return "", err
	}
	p.mu.Unlock()

	if err := p.supervise(sess); err != nil {
		return id, err
	}
	return id, nil
}

// buildSession constructs a per-session supervisor and *Session for a given
// (id, label). Touches no Pool state (the returned Session is in stateEvicted
// and its lifecycle goroutine has not yet been scheduled). Caller is
// responsible for registering it in p.sessions, persisting, and supervising.
//
// Used by Pool.CreateIn (UUID-minted) and Pool.GetOrCreateIn (caller-supplied
// id). Sharing this helper keeps the supervisor.Config + Session field
// shape in one place.
//
// spawnDir is the per-session spawn working directory: when non-empty it is
// used verbatim as supervisor.Config.WorkDir; when empty the child spawns in
// the shared template workdir (tpl.WorkDir), today's behaviour. The pool does
// NOT validate, canonicalise, or trust-check spawnDir — callers supply a
// pre-resolved path (see #685).
//
// settings are the per-session model / effort / YOLO applied to the spawn argv
// (#833) and stored on the returned Session. The zero value appends no flags,
// so an unconfigured spawn's argv carries none. CreateIn and GetOrCreateIn source
// them from mintSettings — the operator's configured model and effort, never the
// bypass (#1575). Pool.Revive sources them from revivedSettings — the model and
// effort the dropped session's own entry persisted, and no posture, so a
// phone-granted bypass still cannot survive a daemon restart (#1487/#2448).
//
// Via materialise (GetOrCreateIn and Revive) the value reaching here is the
// PROVISIONAL one, read before this build; materialise re-reads the source under
// its own lock and, on the rare path where the two disagree, replaces both the
// stored settings and the runner's argv before the session is published (#2492).
// This function is unchanged by that and stays off p.mu deliberately: it writes
// two files and calls the injected RunnerFactory, none of which belongs inside
// the pool's write lock.
//
// The session it builds runs claude: CreateIn is the fresh-id path. MintAs,
// which a client can ask for another agent through (#2647), and materialise,
// whose id may name a dormant entry of another harness, call buildSessionAs.
func (p *Pool) buildSession(id SessionID, label, spawnDir string, settings SessionSettings) (*Session, error) {
	return p.buildSessionAs(id, label, spawnDir, settings, HarnessClaude, "")
}

// buildSessionAs is buildSession for a given harness, which it canonicalises and
// carries onto both the RunnerConfig and the Session (#2593). A harness the
// injected factory has no runner for fails here as an ordinary runner-construction
// error, with the session's two files removed and nothing registered.
//
// threadID is the harness thread the session resumes (#2622), carried onto both
// the RunnerConfig and the Session; empty for a fresh thread and for claude.
func (p *Pool) buildSessionAs(id SessionID, label, spawnDir string, settings SessionSettings, harness, threadID string) (*Session, error) {
	harness = canonicalHarness(harness)
	tpl := p.sessionTpl
	// Normalise the posture before it reaches either the argv or the stored
	// value, so a minted session and a revived one — both two-field literals,
	// mintSettings' and revivedSettings' — hold the default mode spelled out
	// rather than an empty string. It is a normalisation, not a gate: neither caller takes a
	// mode from operator input — Pool.UpdateSettings is where an unrecognised
	// mode is rejected.
	settings = canonicalSettings(settings)
	// The per-session --settings file pre-approves the project's MCP servers so
	// claude's startup enablement modal never wedges the readiness check (#943).
	// It joins spawnBase (below) rather than claudeSettingsArgs so it survives
	// every recompose (backoff restart, #842 live restart). Removed in Pool.Remove
	// after the child is confirmed dead.
	settingsPath, err := writeMCPSettings(p.registryPath, id)
	if err != nil {
		return nil, fmt.Errorf("sessions: write mcp settings: %w", err)
	}
	// This session's appended system-prompt file: #2093's constant plus the bound
	// conversation's operator prompt, if it has one (#2150). Per-session rather
	// than daemon-scoped precisely because that text differs per conversation, and
	// composed HERE because label is the conversation id at both production sites
	// (create_conversation's mint and sessionRouter's revive), so nothing has to
	// widen a signature to carry the prompt. A label naming no conversation, or a
	// pool with no conversations registry, resolves to no bytes — never an error.
	//
	// The bytes are refreshed again in Pool.Activate before the child comes up:
	// since #2085 a conversation's session is minted at create and started on its
	// first message, so the prompt is typically set AFTER this runs.
	operatorPrompt := p.conversationPrompt(label)
	promptPath, err := writeSystemPrompt(p.registryPath, id, composeSystemPromptForOn(daemonPromptText(p.readFolders), p.DaemonInstructions(), operatorPrompt, nil, ""))
	if err != nil {
		// The wrapped error carries paths only. No error and no log line on any
		// path may carry a fragment of the operator's prompt (#2150 AC #3).
		_ = os.Remove(settingsPath)
		return nil, fmt.Errorf("sessions: write system prompt: %w", err)
	}
	// Same reasoning as Pool.New's cleanup defer (#1518): in the data dir an
	// orphan is permanent, so an error return past this point must take the files
	// with it. Only one such return exists today; the defer shape means a future
	// one is covered without anyone remembering to add a removal.
	built := false
	defer func() {
		if !built {
			_ = os.Remove(settingsPath)
			_ = os.Remove(promptPath)
		}
	}()
	// base is the settings-free argv (template, resume suffix, and the immutable
	// --settings pair). Storing base on the Session lets a live restart recompose
	// full argv from the persisted settings (#842). composeSpawnArgs returns a
	// fresh final argv, so base remains the provenance source.
	base := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))
	base = append(base, "--settings", settingsPath)
	// This session's own appended system-prompt file (#2093's flag, #2150's file).
	// On the base rather than in claudeSettingsArgs so the path survives every
	// recompose — a backoff restart, the #842 live settings-restart — each of
	// which re-execs whatever bytes the file then holds.
	base = append(base, "--append-system-prompt-file", promptPath)
	workDir := tpl.WorkDir
	if spawnDir != "" {
		workDir = spawnDir
	}
	// Claude Code's own default model only matters to a claude child; another
	// harness runs at its own defaults (#2647).
	var defaultFamily string
	if harness == HarnessClaude {
		defaultFamily = resolveDefaultFamily(p.defaultModel, workDir)
	}
	args := composeSpawnArgs(base, settings, defaultFamily)

	supCfg := RunnerConfig{
		ClaudeBin: tpl.ClaudeBin,
		WorkDir:   workDir,
		// #1108 seam: the same id already baked into ClaudeArgs as
		// "--session-id <id>" is also exposed here so the stream RunnerFactory
		// (#1109) can read it at construction.
		SessionID: string(id),
		// Same seam as Pool.New's (#2135), and p is already in hand here.
		AdoptAnnouncedReset: func(oldID, newID string) error {
			return p.AdoptAnnouncedID(SessionID(oldID), SessionID(newID))
		},
		ClaudeArgs: args,
		Harness:    harness,
		ThreadID:   threadID,
		// The runner reports a thread it started with the live id, as above.
		RecordThread: func(sessionID, threadID string) error {
			return p.recordThread(SessionID(sessionID), threadID)
		},
		// Same seed as Pool.New's, and canonicalSettings runs above this site too
		// (#2064).
		PermissionMode: settings.PermissionMode,
		// Same derivation as Pool.New's and off BASE for its reason (#2065). tpl is
		// p.sessionTpl, which is cfg.Bootstrap verbatim, so a minted session inherits
		// the same operator pass-through the bootstrap has — the provenance answer is
		// per-daemon, not per-session.
		OperatorBypass: operatorBypass(base),
		Logger:         p.log,
		BackoffInitial: tpl.BackoffInitial,
		BackoffMax:     tpl.BackoffMax,
		BackoffReset:   tpl.BackoffReset,
	}
	sup, err := p.newRunner(supCfg)
	if err != nil {
		return nil, fmt.Errorf("sessions: create runner: %w", err)
	}

	idleTimeout := tpl.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = p.idleTimeoutDefault
	}

	now := time.Now().UTC()
	sess := &Session{
		id:               id,
		sup:              sup,
		log:              p.log,
		label:            label,
		createdAt:        now,
		lastActiveAt:     now,
		bootstrap:        false,
		harness:          harness,
		threadID:         threadID,
		settings:         settings,
		spawnBase:        base,
		defaultFamily:    defaultFamily,
		settingsPath:     settingsPath,
		systemPromptPath: promptPath,
		systemPrompt:     operatorPrompt,
		pool:             p,
		idleTimeout:      idleTimeout,
		turnBusy:         p.turnBusy,
		removedCh:        make(chan struct{}),
		lcState:          stateEvicted,
		activeCh:         make(chan struct{}),
		evictedCh:        closedChan(),
		activateCh:       make(chan struct{}, 1),
		evictCh:          make(chan struct{}, 1),
	}
	built = true
	return sess, nil
}
