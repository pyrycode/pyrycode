package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// DefaultName is the instance name used when -pyry-name is unset and the
// PYRY_NAME environment variable is empty. It produces socket
// ~/.pyry/pyry.sock — the right thing for a single-pyry-per-user setup.
const DefaultName = "pyry"

// defaultName returns the instance name to use when -pyry-name was not given
// on the command line. The PYRY_NAME environment variable wins over
// DefaultName, so shell aliasing (`alias pyry-elli='PYRY_NAME=elli pyry'`)
// works for both supervisor mode and the control verbs.
func defaultName() string {
	if n := os.Getenv("PYRY_NAME"); n != "" {
		return n
	}
	return DefaultName
}

// resolveSocketPath returns the socket path to use given the parsed flags.
// If -pyry-socket was set explicitly, it wins. Otherwise the path is
// derived from the (sanitized) instance name as ~/.pyry/<name>.sock. Falls
// back to a CWD-relative path if $HOME can't be resolved.
func resolveSocketPath(socketFlag, name string) string {
	if socketFlag != "" {
		return socketFlag
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return sanitizeName(name) + ".sock"
	}
	return filepath.Join(home, ".pyry", sanitizeName(name)+".sock")
}

// resolveRegistryPath returns ~/.pyry/<sanitized-name>/sessions.json. Falls
// back to a CWD-relative path if $HOME can't be resolved (matches
// resolveSocketPath's contract).
func resolveRegistryPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "sessions.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "sessions.json")
}

// resolveInstanceDirPath returns ~/.pyry/<sanitized-name> — the per-instance
// directory the two registry paths above are built under, and the anchor
// attachments.EnsureDir files uploads beneath as
// conversations/<conversation-id>/attachments/<attachment-id> (#1897). Falls back
// to a CWD-relative path if $HOME can't be resolved (matches
// resolveRegistryPath's contract).
//
// The name is the operator's own -name flag, routed through the same sanitizeName
// as its siblings; nothing remote reaches this path. Containment for everything
// beneath it is EnsureDir's, which resolves this directory as its anchor and
// compares the destination against it.
func resolveInstanceDirPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return sanitizeName(name)
	}
	return filepath.Join(home, ".pyry", sanitizeName(name))
}

// resolveConversationsRegistryPath returns
// ~/.pyry/<sanitized-name>/conversations.json. Falls back to a CWD-relative
// path if $HOME can't be resolved (matches resolveRegistryPath's contract).
func resolveConversationsRegistryPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "conversations.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "conversations.json")
}

// resolveModelVocabularyPath returns ~/.pyry/<sanitized-name>/model_list.json —
// the daemon-wide model vocabulary this instance last saw (#2450), a sibling of
// the two registry files above. Falls back to a CWD-relative path if $HOME can't
// be resolved (matches resolveRegistryPath's contract).
//
// ONE FILE PER DAEMON INSTANCE is the grain because the vocabulary is a property
// of the machine and account rather than of a conversation — the same fact that
// licenses #2124's cross-conversation read — so it is keyed exactly as the
// instance's other daemon-wide state is and carries no conversation id.
//
// The name is the operator's own -name flag through the same sanitizeName as its
// siblings; nothing remote reaches this path.
func resolveModelVocabularyPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "model_list.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "model_list.json")
}

// resolveClaudeSessionsDir returns the directory where claude writes
// <uuid>.jsonl files for the given workdir. An empty workdir is resolved to
// the process cwd (matching claude's behaviour). Returns "" when the path
// cannot be resolved — startup proceeds without on-disk reconciliation.
func resolveClaudeSessionsDir(workdir string) string {
	if workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		workdir = cwd
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return ""
	}
	return sessions.DefaultClaudeSessionsDir(abs)
}

// resolveRecordingsDir returns the fixed directory the debug bundle reads .cast
// recordings from: ~/.local/share/pyry-recordings, a non-synced, non-backed-up
// sibling of ~/.local/share/pyry-artifacts/. Nothing writes there any more: the
// recorder went with the terminal runner in #1348, and checkDebugCapture rejects
// the flag that switched it on. The read survives so a recording made before the
// upgrade still ships in a bundle. Returns "" when $HOME cannot be resolved, the
// same degrade-to-empty contract as resolveClaudeSessionsDir. The directory is a
// compile-time-fixed location under $HOME; no untrusted input influences it.
func resolveRecordingsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "pyry-recordings")
}

// resolveDefaultCwd returns the absolute working directory recorded on a
// conversation created (via the create_conversation handler) with a null cwd.
// It mirrors the bootstrap session's WorkDir resolution: the absolute form of
// workdir, or the process cwd when workdir is empty, so a created conversation's
// recorded cwd matches where the bootstrap session actually runs. Falls back to
// the raw value when the path cannot be made absolute (Getwd/Abs failure) so the
// call site always receives a usable string.
func resolveDefaultCwd(workdir string) string {
	if workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		workdir = cwd
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return workdir
	}
	return abs
}

// sanitizeName keeps a-z, A-Z, 0-9, _, ., - and replaces anything else with
// _. Empty input becomes "_", and a result of "." or ".." gains a trailing _
// ("._", ".._") — the same shape the transform already gives "./x" and
// "../x", where the separator becomes _. Defends the on-disk socket filename
// and the per-instance state directory against path-traversal and other
// filesystem-unsafe input (e.g. PYRY_NAME from a careless shell setup).
//
// Postcondition: the result is non-empty, holds no path separator, and is
// never "." or "..", so filepath.Join(dir, sanitizeName(name), file) always
// yields dir/<one-component>/file. Pinned by TestSanitizeName and by
// assertInsideInstanceDir's callers. A "." or ".." reaching filepath.Join as
// a live component put per-instance state in ~/.pyry or in $HOME itself; the
// keystore's own validator rejects both rather than transforming them, and
// deliberately stays separate — see internal/keys.validDaemonName.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	// Checked on the built result rather than the input, so the
	// postcondition keeps holding if the allowlist above ever changes.
	switch out := b.String(); out {
	case ".", "..":
		return out + "_"
	default:
		return out
	}
}

// confineWorkdirToHome resolves workdir to its canonical realpath and verifies
// it lies within the operator's home directory, returning the realpath on
// success. The supervised claude's workdir is auto-trusted in ~/.claude.json so
// claude never wedges on the workspace-trust modal; the $HOME bound keeps that
// machine-level auto-accept from extending to system paths or other users'
// spaces. An empty workdir resolves to the process working directory, matching
// how the supervisor launches claude when -pyry-workdir is unset.
//
// Both sides are canonicalised with EvalSymlinks (macOS /var→/private/var,
// case-folding) before the containment test, so a symlinked home does not yield
// a false reject and a sibling like /home/userfoo is not treated as inside
// /home/user (the #118/#221 two-path-sources gotcha). The rejection error names
// the offending path and the $HOME boundary only — never any file contents.
func confineWorkdirToHome(workdir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", home, err)
	}
	absWork, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	workReal, err := filepath.EvalSymlinks(absWork)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	if !withinDir(homeReal, workReal) {
		return "", fmt.Errorf("workdir %q resolves outside the home directory %q: the supervised claude's workdir must be within $HOME", workReal, homeReal)
	}
	return workReal, nil
}

// withinDir reports whether path is dir itself or lies beneath it, using a
// boundary-aware comparison (filepath.Rel) so /home/userfoo is not treated as
// inside /home/user. Both arguments must be cleaned, canonical absolute paths.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// expandTilde performs minimal, leading-only home expansion on a phone-supplied
// path: a bare "~" and a "~/"-prefix expand to the daemon's $HOME; everything
// else (an absolute path, a relative path, or a "~user" form) is returned
// verbatim. A "~user" is intentionally NOT resolved to another user's home — it
// passes through as a literal segment and fails the later confinement/existence
// check as a deterministic reject. No $VAR / arbitrary env expansion (out of
// scope, larger attack surface). The phone cannot know the daemon's absolute
// home, so it sends the default scratch Cwd as "~/.pyrycode/scratch" meaning
// "the daemon's home"; this anchors that at the real $HOME before path
// resolution, never under the process working directory (#696).
func expandTilde(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// confineWorkdirToHomeCreating is the create-aware variant of
// confineWorkdirToHome: same return contract (the canonical realpath on
// success, confined to $HOME), but it tolerates a not-yet-existing leaf/parents
// by canonicalising the longest existing ancestor and creating the rest only
// after the $HOME check passes (#696, the default-scratch case #685 previously
// rejected). When the whole path already exists this reduces exactly to
// confineWorkdirToHome (rest == "" → no MkdirAll, identical realpath).
//
// Order is security-load-bearing. A naive "containment-check the filepath.Abs
// (un-symlink-resolved) path, then MkdirAll" lets a symlinked ancestor (e.g.
// ~/link -> /tmp/evil) pass a textual check and create outside $HOME. So the
// candidate is built from the symlink-RESOLVED longest existing ancestor: the
// ancestor walk probes with os.Lstat (not os.Stat) so a symlink counts as
// existing and is resolved by EvalSymlinks, never stepped over; containment
// check #1 runs on that resolved candidate BEFORE any directory is created; and
// after creation the full path is re-EvalSymlinks'd and re-confined (check #2),
// catching a path that became escaping during creation and yielding the realpath
// to return. The residual confine→chdir TOCTOU window is the same one #685
// accepts; MkdirAll does not widen it.
func confineWorkdirToHomeCreating(workdir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", home, err)
	}
	absWork, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}

	// Split absWork into the longest leading ancestor that exists on disk
	// (probed with Lstat, so a symlink is "existing" and gets resolved, not
	// stepped over) and the not-yet-existing suffix `rest`.
	existing := absWork
	var rest string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break // reached the filesystem root; guard against looping
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}

	existingReal, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	candidate := existingReal
	if rest != "" {
		candidate = filepath.Join(existingReal, rest)
	}

	// Containment check #1 (pre-creation): the candidate is built from the
	// symlink-resolved existing ancestor, so a symlinked ancestor escaping
	// $HOME is rejected here, before any directory is created. The error names
	// the resolved path and the $HOME boundary only — never file contents.
	if !withinDir(homeReal, candidate) {
		return "", fmt.Errorf("workdir %q resolves outside the home directory %q: the supervised claude's workdir must be within $HOME", candidate, homeReal)
	}

	if rest != "" {
		if err := os.MkdirAll(candidate, 0o700); err != nil {
			return "", fmt.Errorf("create workdir %q: %w", candidate, err)
		}
	}

	// Containment check #2 (post-creation re-confine): the path now exists, so
	// EvalSymlinks fully canonicalises it; re-check the $HOME bound to catch a
	// path that became escaping during creation and to yield the realpath.
	final, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	if !withinDir(homeReal, final) {
		return "", fmt.Errorf("workdir %q resolves outside the home directory %q: the supervised claude's workdir must be within $HOME", final, homeReal)
	}
	return final, nil
}

// resolveSpawnDir validates a phone-requested per-conversation spawn workdir for
// create_conversation, mirroring the daemon bootstrap's confine→trust sequence
// (runSupervisor below). It returns the directory to hand sessions.Pool.CreateIn:
//
//	requested == "" → ("", nil): the pool spawns in the shared trusted template
//	                  workdir (today's behaviour for a conversation with no Cwd);
//	                  trustMark is NOT called.
//	requested set   → expandTilde (leading "~"/"~/" → $HOME) then
//	                  confineWorkdirToHomeCreating (canonicalise, confine to
//	                  $HOME, create the dir if missing) then trustMark the
//	                  realpath; returns trustMark's realpath so claude's cwd and
//	                  the trust-marked path are byte-identical (AC#4).
//
// Order is load-bearing: confine gates the $HOME bound BEFORE trust — trustMark
// has no $HOME bound, so trust-marking first could auto-trust a path outside
// $HOME. The phone-default scratch Cwd ("~/.pyrycode/scratch") must resolve under
// $HOME and be created before spawn (#696): expandTilde anchors a leading "~" at
// the daemon's $HOME, and confineWorkdirToHomeCreating creates the dir only after
// the $HOME check passes (a symlinked-ancestor escape is rejected and never
// created). A requested dir that escapes $HOME (including via a symlink resolving
// outside $HOME) or is unresolvable fails confinement and is returned wrapping
// handlers.ErrSpawnDirRejected — a deterministic, non-retryable rejection (the
// confine detail is wrapped via %v for logs; errors.Is matches the sentinel). A
// trustMark failure (a transient ~/.claude.json write error) is returned plain so
// the handler classifies it retryable.
func resolveSpawnDir(requested string) (string, error) {
	if requested == "" {
		return "", nil
	}
	expanded, err := expandTilde(requested)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrSpawnDirRejected, err)
	}
	realpath, err := confineWorkdirToHomeCreating(expanded)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrSpawnDirRejected, err)
	}
	trusted, err := trustMark(realpath)
	if err != nil {
		return "", fmt.Errorf("mark conversation workdir trusted in ~/.claude.json: %w", err)
	}
	return trusted, nil
}
