package attachments

import (
	"errors"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Intake drives one decoded attachment chunk from admission to stored bytes: the
// package's composite entry point, and the one thing the wire layer calls. Every
// primitive it sequences is exported beside it, but the ORDER is not obvious and
// two steps of it are decisions rather than sequencing taste — the fork and the
// three-way answer — so this type owns them in one place rather than leaving them
// to each caller. The destination conversation is NOT one of those decisions: it
// arrives on Receive already decided and already validated (#2143).
//
// ONE PER DAEMON. maxInFlightUploads is a daemon-wide entry-count ceiling, so a
// second Intake beside the first would be a second budget of the same size and
// the bound would no longer mean what it says. That is also why the Registry is
// OWNED rather than injected: there is exactly one, and publishing a way to hand
// one in would publish a way to have two.
//
// It maps nothing to the wire. Turning the sentinels it hands back into
// attachment.* codes and declaring the seam interface this type satisfies are
// both #1897's, which is also this package's first production caller. Validating
// the destination conversation before it reaches Receive is #2143's, and it is
// the CALLER's obligation rather than this type's — see Receive.
//
// It holds no lock of its own and spawns no goroutine, so it has no shutdown
// path; the Registry's mutex stays a leaf beneath it (see Receive).
type Intake struct {
	// reg holds the uploads in flight. Owned, never shared: see the type doc.
	reg *Registry

	// instanceDir is the host directory attachments are filed beneath, captured
	// once. EnsureDir resolves it per completing chunk, which is where every
	// containment guarantee lives; nothing here builds a path.
	instanceDir string
}

// NewIntake builds the driver over instanceDir.
//
// IT TAKES NO CONVERSATION, and that is the whole of #2143 at this constructor.
// Until then it held a resolver over the daemon's follow-active cursor and the
// destination was whatever that cursor said when a transfer completed. The shape
// was published as a security property — attachment_chunk carried no
// conversation_id, so a client could not steer bytes into another conversation's
// directory — and what the omission actually bought was the absence of a field
// rather than isolation between conversations. It cost two misfiles: an
// attachment added before a conversation's first message could never be stored at
// all, because only a successful send_message route stamps that cursor; and a
// client that sent in A, sent in B and returned to A filed its bytes under B.
//
// The destination is a Receive PARAMETER now, validated by the caller against the
// daemon's own registry — the same confinement the retrieval leg already used,
// and the reason a paired client naming a conversation gains nothing by it.
func NewIntake(instanceDir string) *Intake {
	return &Intake{
		reg:         NewRegistry(),
		instanceDir: instanceDir,
	}
}

// Receive routes one decoded chunk of one conn's upload and answers ONE OF THREE
// THINGS:
//
//   - ("", false, nil) — the chunk was accepted and the transfer wants more. This
//     is what MOST chunks of a healthy upload get, and it is NOT a refusal.
//   - (attachment_id, true, nil) — the completing chunk's verified bytes are on
//     the host, filed under the conversation the caller named.
//   - ("", false, err) — refused. err is some layer's own sentinel.
//
// So err != nil is the caller's whole refusal test and stored separates the other
// two: a caller never tests for a particular sentinel to decide what to emit.
// That shape exists because Deliver reports "accepted, not yet complete" as
// ErrIncomplete IN AN ERROR POSITION, and it is the one sentinel in this package
// that neither latches nor discards. A caller reading every non-nil error as a
// refusal answers the phone a reject code for every chunk of a perfectly healthy
// upload, so ErrIncomplete is the ONE error this method interprets rather than
// passes on.
//
// THE FORK. A chunk whose pair the registry does not hold goes through Admit —
// where the declaration cross-check and both resource bounds live — and one whose
// pair is already in flight routes to that transfer. The look-up is Lookup, which
// is a pure read that stamps nothing and reaps nothing, and admission is
// therefore the ONLY way an entry is ever created on this path: there is no route
// into the registry that skips CheckDeclaration, CheckDeclaredSize or
// maxInFlightUploads. First means FIRST TO ARRIVE and never index == 0 —
// docs/protocol-mobile.md § Attachments publishes that chunks may arrive in any
// order, deliberately weaker than debug_bundle_chunk's strict seq, so a fork
// gated on the index refuses a conforming client whose opening chunk carries
// another one.
//
// The alternative fork — call Deliver and admit on ErrUnknownUpload — is rejected
// on purpose: it reinterprets a refusal sentinel as a routing decision, and this
// method reinterprets exactly one error and it is not that one.
//
// ADMIT'S RETURNED ACCUMULATOR IS DISCARDED and Deliver is called with the SAME
// CHUNK, looking the pair back up. Feeding the returned accumulator directly
// bypasses every release Deliver owns and re-opens the lockout for single-chunk
// transfers, which is the sequencing constraint the package overview states.
//
// NOTHING HERE WRAPS, ANNOTATES OR REINTERPRETS A REFUSAL. Every error out of
// this method is another function's verbatim, so
// errors.Is reaches ErrInvalidDeclaration, ErrUploadTooLarge, ErrTooManyUploads,
// the six accumulator sentinels, ErrInvalidID, ErrNotContained and ErrWriteFailed
// unchanged, and the numbers each one wrapped survive with them.
//
// ErrUnknownUpload is not reachable on a single-feeder path — a chunk either
// finds its transfer or has just admitted one — but it is not impossible either,
// and when it happens it travels out verbatim like the rest. Two third parties
// can empty the pair between the look-up and the delivery: ReleaseConn on the
// teardown goroutine, and uploadIdleTimeout's lazy reap firing inside Deliver's
// own look-up. Both mean the transfer is genuinely gone. A chunk arriving for an
// already-reaped pair takes the other branch instead and is admitted as a fresh
// transfer, which is the honest reading of "no transfer in flight": the reap
// already returned the slot, so nothing leaks, and the restarted transfer
// completes only if the client re-sends every chunk.
//
// THE DESTINATION IS conversationID, AND ITS VALIDATION IS A PRECONDITION, AND IT
// IS THE CALLER'S — the contract V2SessionConfig.HistoryPage states in the same
// words for the same reason. conversationID MUST already have passed the daemon's
// KnownConversation membership check, because it becomes a path component below
// this method; handleAttachmentChunk is the caller that discharges it, on EVERY
// chunk rather than only the completing one, so a transfer naming an unusable
// conversation dies on its first frame instead of after every byte has crossed
// the wire. Nothing is re-validated here.
//
// EnsureDir's conversations.ValidID check is the remaining fail-closed backstop
// and NOT the gate: it refuses the empty id and any non-canonical one with
// ErrInvalidID before it touches the filesystem, so a caller that skips the
// membership check still cannot escape the instance directory — but it CAN file
// bytes under a canonical id the daemon does not host, which is why the
// precondition is a contract and not merely advice. That check was belt-only
// while the id came from the daemon's own cursor; from #2143 the value is
// client-authored and the check is load-bearing.
//
// conversationID is a STRING and not a conversations.ConversationID, which is the
// seam's typing rather than an oversight: internal/relay does not import
// internal/conversations — only internal/relay/handlers does — and
// KnownConversation is primitive-typed on purpose to keep it that way. Converting
// to the typed id is this package's side of the seam, at the EnsureDir call below.
//
// It is read on the COMPLETING CHUNK ONLY, because Registry has nowhere to stash
// a per-transfer conversation id and adding one is a different slice. One
// consequence follows and is accepted rather than mitigated: a phone that
// switches conversations mid-upload files its attachment under the NEW one — now
// a registry-validated conversation rather than whatever a cursor happened to
// say, so it is a misfile a client can inflict on itself and never an escape.
// #2146 refuses the switch by recording the destination at admission.
//
// A refusal on the completion half arrives AFTER the transfer is over: Deliver
// released the entry and the bytes assembled cleanly, so no slot leaks and the
// client must re-upload. Two conns uploading under one attachment_id into one
// conversation resolve to the SAME directory and stored name and are
// last-writer-wins, atomically — which corrects the clause in Store's doc
// assuming the dispatch site can only ever reach it with distinct dirs. It is not
// a privilege boundary: both conns are already-paired devices of one account, the
// id is client-chosen and published as not-a-capability, and separating them
// would need a per-conn path component retrieval does not address by.
//
// THE ANSWER NAMES THE ATTACHMENT AND NOTHING ELSE. It is the client's own id,
// echoed, which is the whole of AttachmentStoredPayload's single field. Store's
// returned path is DISCARDED deliberately: it embeds the sanitised filename, and
// that payload's doc block names reaching for it as the natural wrong move. The
// success answer carries no bytes, no filename and no digest, and it cannot — it
// is a string and a bool.
//
// THERE IS NO FORMAT STRING IN THIS BODY, which is how the never-log rule
// protocol.AttachmentChunkPayload's SECURITY block states stays structural at the
// second function in this package holding all four of the strings it bans;
// Deliver is the first and makes the same argument. LOGGING OBLIGATION ON THE
// CONSUMER, inherited from Store and repeated here because this is where it
// crosses out of the package: Store's rename leg wraps an *os.LinkError whose
// Error() prints its destination and therefore the sanitised filename, so #1897
// must map it to attachment.storage_failed's STATIC wire message and, if it logs,
// log the sentinel and the ids rather than the error text. EnsureDir's refusals
// likewise carry host paths and the daemon's own conversation id, which are
// operator-log material the same static mapping keeps off the wire.
//
// PRECONDITION: exactly ONE GOROUTINE AT A TIME may call this for a given conn.
// It is Admit's and Deliver's precondition, inherited rather than re-derived,
// because both hand an *Accumulator back off-lock and Accumulator carries no
// mutex by design. #1897 discharges it — relay spawns exactly one appFrameWorker
// per session — and ReleaseConn is the documented exception to it. Distinct conns
// are safe concurrently: the conn is in the registry key, and EnsureDir and Store
// are both safe for concurrent use on the distinct destinations that follows.
//
// It TAKES NO LOCK. The Registry takes its own for each call, and none is held
// across EnsureDir or Store, so the mutex stays a leaf at the first call site to
// put filesystem I/O on the same path as it.
func (i *Intake) Receive(connID, conversationID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error) {
	if _, held := i.reg.Lookup(connID, chunk.AttachmentID); !held {
		// The declaration comes off THIS chunk, which every chunk of one
		// transfer repeats. Nothing reads chunk.Index here.
		if _, err := i.reg.Admit(connID, chunk.AttachmentID, chunk.TotalChunks, chunk.Size, chunk.SHA256); err != nil {
			return "", false, err
		}
	}

	// The same chunk, looked back up: never the accumulator Admit just returned.
	data, err := i.reg.Deliver(connID, chunk)
	if errors.Is(err, ErrIncomplete) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	// The caller's already-validated destination, typed on this side of the seam.
	dir, err := EnsureDir(i.instanceDir, conversations.ConversationID(conversationID), chunk.AttachmentID)
	if err != nil {
		return "", false, err
	}
	// The path Store returns is deliberately dropped; see the doc block.
	if _, err := Store(dir, chunk.Filename, data); err != nil {
		return "", false, err
	}
	return chunk.AttachmentID, true, nil
}

// ReleaseConn drops every upload still in flight for one conn, so a phone that
// goes away holds none of the daemon-wide capacity. It is the other half of the
// seam #1897 names, and a no-op for a conn holding nothing.
//
// IT CARRIES NO SINGLE-FEEDER PRECONDITION, unlike Receive. ReleaseConn on the
// Registry is remove-only and touches no accumulator, so it is meant to run on
// the teardown goroutine rather than on the one feeding that conn's frames — a
// teardown that had to join the frame worker first would need a lifecycle this
// package does not own.
//
// A teardown landing between Receive's Deliver and its Store still files those
// bytes: they were authenticated and integrity-checked before the conn went away,
// and cancelling mid-store would need a context this seam does not have.
func (i *Intake) ReleaseConn(connID string) {
	i.reg.ReleaseConn(connID)
}
