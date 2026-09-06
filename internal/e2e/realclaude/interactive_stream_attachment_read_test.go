//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamAttachmentRead is the #2039 deliverable (split from #1745):
// one live claude, one file uploaded as an attachment over the encrypted v2
// session, one message naming it, and an assertion that the assistant's reply
// carries a token that existed only inside that file.
//
// WHAT IT PROVES THAT #2038 CANNOT. #2038 landed composeAttachmentPrompt: a
// send_message naming attachment_ids reaches claude as the user's own text
// followed by a daemon-authored block naming each attachment's on-host path and
// directing claude to read them. Its hermetic tier proves the paths reach claude's
// stdin. It cannot prove the other half of the 2026-05-16 decision — that claude,
// given a path and told to read it, OPENS the file with its ordinary Read tool —
// because the fake claude in internal/e2e replays scripted lines, so an assertion
// against it would pass whether the path worked or not. That decision was taken
// because inlining the bytes would duplicate them into the JSONL transcript the
// on-disk copy already holds, and until this ran, the half it rests on was
// untested.
//
// # Why the token is minted at run time
//
// A token in the repo, in this source, or in the message text is a token the
// assistant could reproduce without opening anything. attachmentReadToken mints 96
// bits from crypto/rand per run, and the file under t.TempDir() is the only place
// those bytes exist. Deliberately NOT time.Now().UnixNano(), which this package
// uses for run nonces and which is a value an assistant could in principle
// produce. The test asserts its own message text does not contain the token before
// it sends, so the claim survives a later edit of the prompt rather than resting on
// review.
//
// THE TOKEN FILE LIVES UNDER t.TempDir(), NEVER UNDER THE DAEMON WORKDIR, and that
// is load-bearing rather than tidy. claude's cwd is the workdir; a token file
// sitting in it is findable by an ordinary directory listing, and this test would
// then green with the attachment path playing no part at all.
//
// # MEASURED: what claude actually did
//
// Observed 2026-09-03, the first run of this gate, on --model haiku. Claude called
// exactly one tool, Read, against the absolute attachment path the daemon composed —
// a path OUTSIDE its cwd, under the daemon's instance directory — and replied with
// the 24-character token and nothing else, 24 bytes for a 25-byte file. No permission
// modal was raised, no re-ask, no complaint about the path's location. Whole run:
// 9.0s for both turns including the daemon spawn.
//
// So the 2026-05-16 decision holds on the live rung, not just the hermetic one: hand
// claude a path and tell it to read, and it opens the file with its ordinary Read
// tool. The token came back byte-exact, so the assertion stays a strict
// strings.Contains rather than folding case.
//
// # AC 3: the mutation this run is falsifiable against
//
// composeAttachmentPrompt already returns text unchanged for an empty path list, so
// making that return unconditional is "composition disabled" exactly. Under it, this
// test FAILS — measured 2026-09-03, and the reply is the diagnostic: "I don't see a
// file path in your message. Could you please provide the path to the file you'd like
// me to read?", zero tools called. The upload chain still ran green underneath it
// (attachment_stored arrived, the file landed on the host), so the red is isolated to
// the composition. The manifest and the invocation are in
// docs/specs/architecture/2039-live-attachment-read.md § Revisions.
//
// A `go test -overlay` ALONE DOES NOT MUTATE WHAT THIS PACKAGE MEASURES, which is
// the trap to know about before repeating the run. The overlay reaches the test
// binary, but the daemon under test is a separate process that ensurePyryBuilt
// produces by shelling out to a plain `go build` with no overlay flag — so an
// overlaid run would exercise an unmutated daemon and green misleadingly. The
// mutation has to be built into the binary and handed over via PYRY_E2E_BIN, which
// ensurePyryBuilt honours. Still no worktree write.
//
// # Two live-run risks, so they are not debugged as bugs
//
//   - spawnBootstrapDaemon passes --dangerously-skip-permissions, so a file read
//     raises no permission modal and this file drives none. A modal_shown arriving
//     anyway means that premise broke, and the drain fails naming it rather than
//     parking until the deadline.
//   - The attachment lands under the daemon's INSTANCE directory
//     (<instanceDir>/conversations/<id>/attachments/<id>/), not under claude's
//     workspace, so the composed prompt hands claude an absolute path outside its
//     cwd. Measured above: the first run opened it without objection. A future model
//     that balks is what this gate would catch, and that measurement would itself be
//     a finding about the 2026-05-16 decision rather than something to work around.
//
// # Running it
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestInteractiveStreamAttachmentRead$' ./internal/e2e/realclaude/
//
// It costs two live claude turns and needs real credentials; a skip without them is
// the correct outcome and carries no signal. READ THE COUNT OF TESTS EXECUTED, NEVER
// THE EXIT CODE: with no credentials every test skips and exits 0, and when the
// package fails to build zero tests run and it still exits 0 through a shell
// wrapper. `make check` never compiles this package — `make preship` is the gate
// that does. Count the `=== RUN` lines.
//
// Placement alone wires this into make e2e-realclaude and make preship via the
// e2e_realclaude build tag; no Makefile change. This slice captures no artefact, so
// nothing needs `git add`ing beyond this file.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The run's three fixed identifiers, in the package's <ticket>0000-… convention so
// no same-package file collides on one. All three must satisfy
// conversations.ValidID — 36 characters, '4' at index 14, '8' at index 19 — which
// both attachments.EnsureDir and attachments.ResolvePath check before either id
// becomes a path component. attachReadAttachmentID is the CLIENT's own id: nothing
// daemon-side mints one, the client supplies it on every chunk, and storage keys by
// it.
const (
	attachReadBootstrapUUID = "20390000-0000-4000-8000-000000000001"
	attachReadConvID        = "20390000-0000-4000-8000-000000000002"
	attachReadAttachmentID  = "20390000-0000-4000-8000-000000000003"
)

// attachReadFilename is the client's own name for the uploaded file. Plain ASCII,
// inside attachments.SanitizeFilename's [a-zA-Z0-9_.-] allowlist, so it reaches the
// host unchanged and requireStoredAttachment can compare it exactly. It names the
// ticket and not the token: a filename that hinted at the contents would hand claude
// a second route to the answer.
const attachReadFilename = "pyrycode-2039-token.txt"

// attachReadTokenBytes is the entropy behind one run's token, in raw bytes before
// hex. 12 bytes is 96 bits — past any argument that an assistant guessed it, and
// short enough at 24 hex characters that a model echoes it back without mangling.
const attachReadTokenBytes = 12

// The two request-envelope ids the run needs to correlate replies on. The
// cursor-stamp turn takes 2, so these start at 3; the attachment_stored reply names
// attachReadChunkEnvID in its in_reply_to, and the message's ack names
// attachReadSendEnvID.
const (
	attachReadChunkEnvID uint64 = 3
	attachReadSendEnvID  uint64 = 4
)

// attachReadToolLogCap bounds how many distinct tool names the measured observation
// prints. Claude-authored, so bounded; the observation is a record of HOW claude got
// the bytes, never an assertion — a future model reading the file through some other
// tool is still a model that read the file.
const attachReadToolLogCap = 8

func TestInteractiveStreamAttachmentRead(t *testing.T) {
	h := startAttachmentReadHarness(t)
	nonce := time.Now().UnixNano()

	token := mintAttachmentToken(t)
	file, digest := writeTokenFile(t, token)

	// ── The upload: one chunk, then the reply that says the bytes are on the host ──
	//
	// NO TURN IS ROUTED FIRST since #2143. This run used to drive a whole stamp turn
	// against a real claude — a live prompt and its full drain — for no reason but to
	// stamp the follow-active cursor attachments.Intake read the destination from;
	// before any route that cursor is empty, the completing chunk answered
	// ErrNoConversation, and the dispatch arm mapped it to attachment.storage_failed:
	// a success-shaped stream that stored nothing. The chunk names its conversation
	// now, so the upload stands alone and the live budget goes to the turn under
	// test. The standing alarms — unrecognized_message and rate_limited — are still
	// carried by the drain of that turn.
	uploadSingleChunk(t, h, attachReadChunkEnvID, file, digest)
	awaitAttachmentStored(t, h, attachReadChunkEnvID, 30*time.Second)
	requireStoredAttachment(t, h.home)

	// ── The message naming it, and the assertion the whole file exists for ──
	//
	// The text has to ask for the CONTENTS: the daemon's block tells claude to read
	// the files, it does not tell claude to say what it found. Without that, a claude
	// that opened the file correctly could still reply "I've read it" and red this.
	text := fmt.Sprintf("Read the attached file and reply with its exact contents and "+
		"nothing else. run=%d", nonce)
	if strings.Contains(text, token) {
		t.Fatal("the message text carries the token, so a reply echoing it would prove nothing " +
			"about whether the file was opened; the token must exist only inside the attachment")
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   attachReadSendEnvID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: attachReadConvID,
			MessageID:      "m-attach",
			Text:           text,
			AttachmentIDs:  []string{attachReadAttachmentID},
		}),
	})

	reply, tools := drainTurnText(t, h, attachReadConvID, perTurnReplyBudget)
	if !strings.Contains(reply, token) {
		t.Fatalf("the reply does not carry the token that existed only inside the attached file, so "+
			"nothing here shows claude opened it. Reply (%d bytes): %q. Tools called: %v",
			len(reply), truncateString(reply, questionTextLogCap), tools)
	}
	t.Logf("measured: claude returned the attachment's token in a %d-byte reply, calling %v — the "+
		"composed path was opened, not guessed", len(reply), tools)
}

// --- fixture ----------------------------------------------------------------

// mintAttachmentToken returns this run's token: 24 lowercase hex characters over 96
// bits from crypto/rand.
//
// crypto/rand and NOT time.Now().UnixNano(), which is what this package reaches for
// when it wants a run nonce. The difference is load-bearing exactly once, in AC 3's
// claim: a pass must not be reachable by an assistant producing a plausible value,
// and a nanosecond timestamp is such a value. It fails the test rather than falling
// back on a weaker source — a token this run could not mint is a run that cannot
// make its claim.
func mintAttachmentToken(t *testing.T) string {
	t.Helper()
	buf := make([]byte, attachReadTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("mint attachment token: %v — without crypto/rand entropy this run cannot claim "+
			"the token was unguessable, which is the whole basis of its assertion", err)
	}
	return hex.EncodeToString(buf)
}

// writeTokenFile writes the token to a file UNDER t.TempDir() and returns the bytes
// read back plus their lowercase-hex sha256.
//
// THE DIRECTORY IS THE POINT. t.TempDir() is a sibling of the isolated HOME, so the
// file sits outside both the daemon's tree and claude's cwd. Written under the
// workdir instead, an ordinary directory listing would find it and this test would
// green with the attachment path playing no part — the one vacuity that would not
// announce itself. 0600 matches what attachments.Store writes the daemon-side copy
// at: these are the user's own private file bytes on both ends.
//
// The digest is taken over the bytes READ BACK, which are the same bytes the chunk
// carries, so the declaration and the payload share provenance and a fixture bug
// cannot make a self-consistent green. hex.EncodeToString is already lowercase, the
// canonical form docs/protocol-mobile.md § Attachments compares for exact equality.
func writeTokenFile(t *testing.T, token string) (file []byte, digest string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), attachReadFilename)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back token file: %v", err)
	}
	sum := sha256.Sum256(file)
	return file, hex.EncodeToString(sum[:])
}

// --- upload -----------------------------------------------------------------

// uploadSingleChunk seals the whole file as ONE attachment_chunk at index 0.
//
// THE CHUNK COUNT IS DERIVED FROM protocol.MaxAttachmentChunkBytes, never hardcoded
// to 1, and guarded. attachments.CheckDeclaration compares total_chunks to
// max(1, ceil(size / bound)) as an EQUALITY, so a fixture that stopped being
// single-chunk would be refused invalid_chunk for a reason no failure message here
// would name. The package overview records the same trap from the other side —
// #1898's multi-chunk fixture derives its count so a future move of the constant
// reddens loudly instead of silently degrading to one chunk. The ceiling is the
// receiver's own division-then-remainder form, never (size + bound - 1) / bound,
// which wraps.
//
// attachment_chunk NAMES its conversation (#2142), and since #2143 the daemon files
// the bytes under exactly that one — validated against its own registry before the
// id becomes a path component, never trusted as sent. That is why this caller no
// longer routes a turn first: the destination is on the frame rather than in a
// cursor only a successful send_message stamps. Naming a conversation is not
// authorization; confinement is the registry check plus attachments.EnsureDir
// refusing an escaping directory, and it did not move.
func uploadSingleChunk(t *testing.T, h *perConvHarness, envID uint64, file []byte, digest string) {
	t.Helper()
	totalChunks := len(file) / protocol.MaxAttachmentChunkBytes
	if len(file)%protocol.MaxAttachmentChunkBytes != 0 {
		totalChunks++
	}
	totalChunks = max(totalChunks, 1)
	if totalChunks != 1 {
		t.Fatalf("fixture of %d bytes cuts into %d chunks at a bound of %d; this run drives a "+
			"SINGLE-chunk transfer and its declaration would be refused invalid_chunk",
			len(file), totalChunks, protocol.MaxAttachmentChunkBytes)
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   envID,
		Type: protocol.TypeAttachmentChunk,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.AttachmentChunkPayload{
			ConversationID: attachReadConvID,
			AttachmentID:   attachReadAttachmentID,
			Index:          0,
			TotalChunks:    totalChunks,
			Filename:       attachReadFilename,
			MimeType:       "text/plain",
			Size:           int64(len(file)),
			SHA256:         digest,
			Data:           file,
		}),
	})
}

// awaitAttachmentStored drains to the attachment_stored correlated to the chunk that
// completed the transfer, and fails naming the code on any error frame.
//
// It is a local drain rather than drainForReply because drainForReply skips a
// TypeError silently and would report a bare timeout on the most diagnostic frame in
// the run. Here the refusal CODE is the whole diagnostic: storage_failed points at
// the follow-active cursor (the caller's routed turn never stamped it),
// invalid_chunk at the declaration arithmetic, integrity_failed at the digest or the
// assembled length.
func awaitAttachmentStored(t *testing.T, h *perConvHarness, envID uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, ok := nextAttachReadEnvelope(t, h, deadline)
		if !ok {
			t.Fatalf("no attachment_stored for chunk envelope %d within %s — the single chunk either "+
				"never reached the intake or did not complete the transfer", envID, timeout)
		}
		switch env.Type {
		case protocol.TypeAttachmentStored:
			if env.InReplyTo == nil || *env.InReplyTo != envID {
				continue // an earlier transfer's reply cannot exist here, but correlate anyway
			}
			var p protocol.AttachmentStoredPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode attachment_stored payload: %v", err)
			}
			if p.AttachmentID != attachReadAttachmentID {
				t.Fatalf("attachment_stored attachment_id = %q, want %q (the id this run chose)",
					p.AttachmentID, attachReadAttachmentID)
			}
			return
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("the upload was refused and its error payload did not decode: %v", err)
			}
			t.Fatalf("the upload was refused with code %q (retryable=%v); storage_failed points at the "+
				"follow-active cursor, invalid_chunk at the declaration arithmetic, integrity_failed "+
				"at the digest", ep.Code, ep.Retryable)
		}
		// Anything else is the routed turn's tail — classify after decrypt, which
		// keeps the receive nonce in lockstep, and read on.
	}
}

// requireStoredAttachment is the deterministic precondition under the stochastic
// assertion: the bytes are actually on the host before claude is asked to read them.
//
// DIFFERENT FABRIC, and it separates two reds that are otherwise identical. Without
// it, a stored file that never landed and a claude that never opened one both
// surface as "the reply does not carry the token", and only the second is the
// finding this run exists to make. #1898 owns the bytes-on-host claim in full; this
// is one directory read, not a second copy of that proof.
//
// It names the DIRECTORY and never the leaf. The leaf is attachments.SanitizeFilename's
// rendering of a client filename, which § Attachments bans logging for a privacy
// reason sanitising does not lift — the same obligation attachments.ResolvePath puts
// on its own callers. The directory is built from two canonical-shape-checked ids
// and carries no client text.
func requireStoredAttachment(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".pyry", "test", "conversations", attachReadConvID,
		"attachments", attachReadAttachmentID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("attachment directory: %v — the daemon replied attachment_stored and the host has "+
			"no such directory, so the composed prompt would name a path that opens nothing", err)
	}
	if len(entries) != 1 {
		t.Fatalf("attachment directory holds %d entries, want exactly 1 — a second entry is a leaked "+
			"temporary file from attachments.Store's rename", len(entries))
	}
	if got := entries[0].Name(); got != attachReadFilename {
		t.Fatalf("stored file name = %q, want %q — a plain-ASCII name is inside SanitizeFilename's "+
			"allowlist and passes through unchanged", got, attachReadFilename)
	}
}

// --- the turn under test ----------------------------------------------------

// drainTurnText accumulates the turn's assistant text for convID and returns it with
// the distinct tool names claude called along the way, at the terminal
// turn_state{idle}.
//
// IT ACCUMULATES RATHER THAN SIGNALS, which is the whole reason it is not
// drainForAssistantReply: that one returns on the FIRST non-empty delta and discards
// the text, so it proves liveness and could never see a token that arrives in the
// third delta. Widening it would change what fourteen callers assert.
//
// IDLE IS ONLY TERMINAL ONCE A DELTA HAS BEEN SEEN. The caller drained the
// cursor-stamp turn through its own terminal idle, so no earlier idle should be left
// on the wire — but accepting one unconditionally would turn a stale frame into an
// empty accumulation and a token failure that blamed claude for a wire bug. Gating on
// sawDelta makes the deadline message name which milestone was missed instead, the
// same split drainForCompletedTurn makes.
//
// A MODAL IS A HARD FAIL, not something to answer. spawnBootstrapDaemon passes
// --dangerously-skip-permissions, so a file read raises no permission modal; one
// arriving means that premise broke, and without an answer the turn parks until the
// daemon's approval window elapses and the diagnostic becomes a bare wall clock.
//
// The frame loop is the package's standing one: read binary→phone frames in receive
// order, decrypt EVERY noise_msg to keep the sequential receive nonce in sync, and
// skip a non-noise_msg control frame WITHOUT decrypting. A TypeError is a hard fail
// naming its code — attachment.not_found here means the resolver, the conversation
// binding or a non-canonical id, and never claude.
func drainTurnText(t *testing.T, h *perConvHarness, convID string, timeout time.Duration) (string, []string) {
	t.Helper()
	var (
		text     strings.Builder
		tools    []string
		seenTool = map[string]struct{}{}
		sawDelta bool
	)
	deadline := time.Now().Add(timeout)
	for {
		env, ok := nextAttachReadEnvelope(t, h, deadline)
		if !ok {
			if !sawDelta {
				t.Fatalf("no non-empty assistant_delta for %q within %s — the message naming the "+
					"attachment never produced a turn (it was refused before enqueue, or the drain "+
					"never delivered it)", convID, timeout)
			}
			t.Fatalf("claude replied for %q but the turn never reached terminal turn_state{idle} "+
				"within %s; %d byte(s) so far, tools %v", convID, timeout, text.Len(), tools)
		}
		switch env.Type {
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("the message was refused and its error payload did not decode: %v", err)
			}
			t.Fatalf("the message naming the attachment was refused with code %q (retryable=%v); "+
				"attachment.not_found means the id did not resolve under this conversation — the "+
				"resolver, the binding, or a non-canonical id, never claude", ep.Code, ep.Retryable)

		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode modal_shown payload: %v", err)
			}
			t.Fatalf("a modal was raised (class %q, id %q, title %q) — this harness spawns claude "+
				"with --dangerously-skip-permissions precisely so reading the attachment raises "+
				"none, and nothing here answers one, so the turn would park until the daemon's "+
				"approval window elapsed", shown.Class, shown.ModalID,
				truncateString(shown.Title, questionLabelLogCap))

		case protocol.TypeToolUse:
			var p protocol.ToolUsePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode tool_use payload: %v", err)
			}
			if p.ConversationID != convID || p.Name == "" {
				continue
			}
			if _, dup := seenTool[p.Name]; dup || len(tools) >= attachReadToolLogCap {
				continue
			}
			seenTool[p.Name] = struct{}{}
			tools = append(tools, truncateString(p.Name, questionLabelLogCap))

		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				sawDelta = true
				text.WriteString(p.Text)
			}

		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID || !sawDelta {
				continue
			}
			return text.String(), tools
		}
	}
}

// nextAttachReadEnvelope reads and decrypts the next daemon→phone application
// envelope, answering ok=false once deadline passes.
//
// IT IS THE RUN'S ONLY READER after the cursor-stamp turn. Each decrypt advances the
// receive CipherState exactly once, so every frame must be taken in arrival order; a
// second concurrent reader desynchronises the sequential receive nonce into a decrypt
// failure that reads like a daemon bug. It answers rather than Fatals on a timeout,
// unlike readInnerFrame, so each caller names the milestone that failed instead of
// reporting a bare receive timeout.
func nextAttachReadEnvelope(t *testing.T, h *perConvHarness, deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				return protocol.Envelope{}, false
			}
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) carries no envelope and does
			// not advance the receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return env, true
	}
}

// --- harness ----------------------------------------------------------------

// startAttachmentReadHarness stands up the real interactive stack for this run: the
// stream-json interactive runner, a bootstrap session seeded at a deterministic pool
// id, attachReadConvID bound to it, and a paired phone handshaken with the
// interactive capability. Skips cleanly when claude or credentials are absent.
//
// Both seeds and the config toggle land BEFORE the daemon spawns: sessions.json,
// conversations.json and <home>/.pyry/config.json are each read exactly once at
// startup, with no reload. A UUID mismatch between the two seeds drops every event
// and hangs the first drain, which is what that drain's deadline message names.
func startAttachmentReadHarness(t *testing.T) *perConvHarness {
	t.Helper()
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME: a fresh claude sessions dir,
	// and the cwd the composed prompt's absolute attachment path sits OUTSIDE of.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	writeStreamInteractiveConfig(t, home)

	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBootstrapRegistry(t, home, attachReadBootstrapUUID)
	seedBoundConversation(t, home, attachReadConvID, attachReadBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, fr.URL()+"/v2/server")
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)
	return &perConvHarness{phone: phone, initSend: initSend, initRecv: initRecv, home: home, workdir: workdir}
}
