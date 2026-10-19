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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
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
// attachment_stored reply names attachReadChunkEnvID in its in_reply_to, and the
// message's ack names attachReadSendEnvID. They keep the values they held when a
// cursor-stamp turn still occupied 2: an envelope id is only a correlation handle,
// so the gap costs nothing and renumbering would churn the file for no assertion.
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
	runParallel(t)
	h := startAttachmentReadHarness(t)
	sequence := uint64(2)
	wire := scenariohelpers.Wire{Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}, Next: scenariohelpers.Receive(t, h.phone, h.initRecv)}
	scenariohelpers.AttachmentRead(t, wire, scenariohelpers.AttachmentReadConfig{Home: h.home, ConversationID: attachReadConvID, AttachmentID: attachReadAttachmentID, Filename: attachReadFilename, Token: mintAttachmentToken(t), Prompt: fmt.Sprintf("Read the attached file and reply with its exact contents and nothing else. run=%d", time.Now().UnixNano()), Budget: perTurnReplyBudget})
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

// nextAttachReadEnvelope reads and decrypts the next daemon→phone application
// envelope, answering ok=false once deadline passes.
//
// IT IS THE RUN'S ONLY READER, and since #2143 that holds for the whole run rather
// than only after a cursor-stamp turn. Each decrypt advances the
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
	// Parallel only through runParallel; see liveHome.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := liveHome(t) // skips cleanly when no creds
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

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBootstrapRegistry(t, home, attachReadBootstrapUUID)
	seedBoundConversation(t, home, attachReadConvID, attachReadBootstrapUUID, workdir)

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
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
