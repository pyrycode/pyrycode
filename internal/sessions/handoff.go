package sessions

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// handoffNotesDir is the handoff-note directory's name under the daemon data
// dir. A sibling of session-settings/ and session-prompts/ and a tenant of
// NEITHER, for two different reasons: writeMCPSettings' doc promises a *.json
// glob of the settings directory counts sessions exactly, and both
// sessionPromptsDirFor callers remove the prompts directory wholesale. A note
// placed in either would break a promise or be deleted.
const handoffNotesDir = "handoff-notes"

// MaxHandoffNoteBytes bounds one handoff note on disk. UTF-8 BYTES, NOT RUNES,
// matching every bound in internal/protocol.
//
// An over-bound note is TRUNCATED, not refused — the opposite of
// admissibleClientField, and deliberately so. A refused client name costs a
// section of a system prompt; a refused handoff note costs the successor session
// everything its predecessor knew, and a wrap-up reply that ran long is exactly
// the case where the note matters most. The head of a long note is still a
// usable handoff.
//
// Exported because #2455 composes a prompt around a bounded value and should be
// able to name the bound rather than re-derive it.
const MaxHandoffNoteBytes = 16 << 10

// ErrHandoffNotesDisabled is returned by Pool.WriteHandoffNote when persistence
// is disabled — the test-only mode Pool.dataDir reports as "". A sentinel so a
// caller can recognise the condition: a failed handoff note must never fail the
// reset it was written for, so #2455 is expected to log and continue rather than
// propagate.
var ErrHandoffNotesDisabled = errors.New("sessions: handoff notes need a data dir; persistence is disabled")

// handoffNotePathFor resolves the absolute path of a conversation's handoff
// note, or "" when persistence is disabled. It is the single derivation all
// three methods funnel through, which is what makes the id gate below apply to
// reads as well as to the write.
//
// registryPath == "" returns "" BEFORE the id is validated, matching
// systemPromptPathFor's ordering; each caller then gives the disabled case its
// own answer (see the three methods). The os.TempDir answer writeMCPSettings and
// writeSystemPromptFile give for that branch DOES NOT TRANSFER HERE: a note's
// whole purpose is to outlive the session that wrote it and survive a daemon
// restart, and a file an age-based OS reaper may delete cannot do either. There
// is no honest temp-file fallback, so there is none.
//
// id is gated on conversations.ValidID, for systemPromptPathFor's reason: since
// it names a file, an id carrying a separator or a ".." segment would place the
// write outside the data dir. This package does not mint conversation ids — it
// is handed them — so the gate is load-bearing rather than decorative, and a
// malformed one is a hard error rather than a silent fallback.
//
// It does NOT create the directory, which is where it departs from
// systemPromptPathFor: Pool.HandoffNotePath answers from it and must stay
// side-effect free. The write creates the directory itself.
func handoffNotePathFor(registryPath string, id conversations.ConversationID) (string, error) {
	if registryPath == "" {
		return "", nil
	}
	if !conversations.ValidID(string(id)) {
		// %q, never %s: the id is caller-supplied and reaches an error string, so
		// quoting is what keeps a control character or a newline in a malformed one
		// from forging a log line.
		return "", fmt.Errorf("sessions: handoff note for conversation %q: not a canonical conversation id", id)
	}
	dataDir, err := filepath.Abs(filepath.Dir(registryPath))
	if err != nil {
		return "", fmt.Errorf("sessions: resolve handoff note dir: %w", err)
	}
	return filepath.Join(dataDir, handoffNotesDir, string(id)+".txt"), nil
}

// truncateHandoffNote bounds text at MaxHandoffNoteBytes WITHOUT splitting a
// rune: the cut is walked back to the nearest rune start, so a multi-byte rune
// straddling the budget is dropped whole rather than left as a lone prefix byte.
//
// The walk-back is bounded at utf8.UTFMax-1 bytes, which is every distance a
// rune start can be from the cut in well-formed UTF-8. Finding none within it
// means the input is not valid UTF-8 at that point — the text is claude-authored
// and crosses the subprocess boundary, so that is possible — and the hard cut is
// taken, because there is no rune boundary to honour and a bounded loop beats
// scanning back through an arbitrarily long run of continuation bytes.
//
// The result is always a PREFIX of the input. Nothing is rewritten, escaped or
// repaired; see Pool.HandoffNote on what that leaves the caller holding.
func truncateHandoffNote(text string) string {
	if len(text) <= MaxHandoffNoteBytes {
		return text
	}
	for cut := MaxHandoffNoteBytes; cut > MaxHandoffNoteBytes-utf8.UTFMax && cut > 0; cut-- {
		if utf8.RuneStart(text[cut]) {
			return text[:cut]
		}
	}
	return text[:MaxHandoffNoteBytes]
}

// writeHandoffNoteFile writes text to final, atomically, creating the note
// directory at 0700 on demand — cold start needs that, because the data dir
// itself is created only by saveRegistryLocked, which has not necessarily run.
//
// Scratch file in the target directory, fsync, rename, at os.CreateTemp's 0600
// preserved by the rename: writeMCPSettings' recipe and its reasons. A rename
// hands a reader either the complete old note or the complete new one rather
// than a truncated prefix, and it replaces a symlink at the destination instead
// of writing through it. The 0600 here is confidentiality rather than integrity:
// a note is a fragment of the operator's own conversation.
//
// final is never "" — every caller short-circuits the persistence-disabled case
// before reaching this — so there is no os.TempDir branch to mirror.
func writeHandoffNoteFile(final, text string) error {
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sessions: mkdir handoff notes dir: %w", err)
	}
	// Dotted .tmp suffix so a scratch file left by a SIGKILL inside the write
	// window is never mistaken for a note. Mirrors writeSystemPromptFile's
	// ".system-prompt-*.txt.tmp".
	f, err := os.CreateTemp(dir, ".handoff-*.txt.tmp")
	if err != nil {
		return fmt.Errorf("sessions: create handoff note tempfile: %w", err)
	}
	tmpName := f.Name()

	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sessions: write handoff note: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sessions: fsync handoff note: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("sessions: close handoff note: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("sessions: rename handoff note: %w", err)
	}
	return nil
}

// WriteHandoffNote stores text as the handoff note of the conversation id names
// and returns the absolute path written, REPLACING any previous note for that
// conversation. text over MaxHandoffNoteBytes is truncated, never refused.
//
// The note is keyed by CONVERSATION, not by session, because it must outlive the
// session that produced it: a reset replaces the session and the note is for its
// successor. That is also why nothing removes it — not Pool.Remove, not the
// `/clear` rotation, not Pool.Run's shutdown defer, and not Pool.New's startup
// purge, which clears session-prompts wholesale and would take the note with it
// if the note lived there. Reaping a note whose conversation was deleted is
// deliberately unbuilt; a note is retained until something is written to do it.
//
// Persistence disabled returns ErrHandoffNotesDisabled. There is no honest
// success to report there and a silent no-op would claim one; see
// handoffNotePathFor on why the temp-file fallback its neighbours take does not
// transfer.
//
// The store LOGS NOTHING — it holds no logger and takes none, so no fragment of
// a claude-authored note can reach a log line by construction rather than by
// discipline. Errors name paths and wrap OS errors; none carries note bytes.
//
// Concurrency: takes no lock. It reads p.registryPath and no other pool state,
// the way Pool.dataDir does. Two concurrent writes for one conversation race on
// the rename and the loser is the note, which is writeMCPSettings' accepted
// position — a reader still sees one complete note, never a blend.
func (p *Pool) WriteHandoffNote(id conversations.ConversationID, text string) (string, error) {
	final, err := handoffNotePathFor(p.registryPath, id)
	if err != nil {
		return "", err
	}
	if final == "" {
		return "", ErrHandoffNotesDisabled
	}
	if err := writeHandoffNoteFile(final, truncateHandoffNote(text)); err != nil {
		return "", err
	}
	return final, nil
}

// HandoffNote returns the handoff note of the conversation id names, or "" when
// there is none.
//
// It is TOTAL over absence, conversationPrompt's posture: the first reset of any
// conversation has no predecessor, and that is an ordinary state rather than an
// error. An absent note and an empty one are therefore indistinguishable here,
// which is correct for the reading consumer — both compose the same wrap-up
// prompt — and Pool.HandoffNotePath is where the existence question is asked.
// Persistence disabled likewise returns "": no store, so no note. A read that
// fails for any OTHER reason IS an error; a caller asking for the note is
// entitled to know the disk refused.
//
// THE RETURNED TEXT IS UNTRUSTED. It is claude-authored, it crossed the
// subprocess boundary, and this store judged its SIZE and its MODE and nothing
// else — no UTF-8 repair, no control-character filter, no trimming. That is
// deliberate: what may appear in a composed prompt belongs to the composing
// site, and validating here as well would put a second opinion in a second
// place (composeSystemPrompt's argument for operator bytes). The obligation is
// therefore the caller's, and it is not theoretical: a composing site must place
// these bytes the way clientSection places a client's, and admissibleClientField
// is the precedent for what that costs. The string type carries none of this,
// which is why the doc does.
//
// The read is bounded at MaxHandoffNoteBytes+1 and passed through the same
// truncation as the write, so a file larger than the cap — one this store did
// not write — yields a bounded, rune-safe answer instead of an allocation sized
// by corruption or by whoever planted it.
//
// Concurrency: takes no lock; see Pool.WriteHandoffNote.
func (p *Pool) HandoffNote(id conversations.ConversationID) (string, error) {
	path, err := handoffNotePathFor(p.registryPath, id)
	if err != nil || path == "" {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("sessions: open handoff note: %w", err)
	}
	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(io.LimitReader(f, MaxHandoffNoteBytes+1))
	if err != nil {
		return "", fmt.Errorf("sessions: read handoff note: %w", err)
	}
	return truncateHandoffNote(string(raw)), nil
}

// HandoffNotePath reports where the conversation's handoff note would be and
// whether one is there, WITHOUT reading the text. It is #2468's method: the
// pointer line it composes names the path and is omitted entirely when there is
// no note, so it needs both answers and neither requires the bytes.
//
// The path is returned even when nothing is there, because it is derivable
// either way and an absent note is not an error — that is what lets the caller
// branch on exists rather than on err. Persistence disabled is the one case that
// yields no path, since without a data dir there is nowhere a note could be.
//
// It answers os.LSTAT and reports anything that is not a regular file as ABSENT.
// That is the correct predicate for the question rather than an added defence: a
// symlink is not a note this store wrote, and os.Stat would follow one — a link
// planted at this path would otherwise report a note and hand the caller a path
// it then names to claude, which has file-read tools. What actually bounds that
// is the 0700 note directory, since planting the link requires the daemon's own
// uid; using Lstat costs one call and removes the need to rely on it alone. The
// read path deliberately does not grow O_NOFOLLOW, which is syscall-level and
// platform-dependent for a boundary the directory mode already holds.
//
// The answer is a point-in-time one, and nothing in the design removes a note,
// so a caller may name the path it got. A future reaper would invalidate that.
//
// Concurrency: takes no lock; see Pool.WriteHandoffNote.
func (p *Pool) HandoffNotePath(id conversations.ConversationID) (string, bool, error) {
	path, err := handoffNotePathFor(p.registryPath, id)
	if err != nil || path == "" {
		return "", false, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return path, false, nil
		}
		return path, false, fmt.Errorf("sessions: stat handoff note: %w", err)
	}
	return path, fi.Mode().IsRegular(), nil
}
