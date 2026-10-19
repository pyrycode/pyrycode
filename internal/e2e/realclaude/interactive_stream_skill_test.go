//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamSkillInvocationIsSilent is #2087's live proof: invoking a
// skill leaves no trace of the skill's body in the operator's chat.
//
// WHAT WENT WRONG. Claude's harness injects a skill's whole instruction file onto
// the output stream as a `user` line carrying a `text` block. streamsup.emitUser
// surfaced every user/text block but the one known harness nudge, so each skill
// invocation put an "Unrecognized message" row into the chat carrying the entire
// body, cut at the daemon's payload cap. Observed twice in one session on
// 2026-09-04 on Opus 5, at 18681 and 87244 chars.
//
// WHY THE EXISTING SENTINELS MISSED IT, and why this file exists rather than a
// widened sibling. TestInteractiveStreamNoUnrecognizedOnToolTurn drives the widest
// line inventory a single turn can produce — and skill invocation is not in it,
// for the same reason the 2026-07-27 census that seeded streamsup.ignoredLineTypes
// missed it: that measurement drove turns that CALLED TOOLS, and a skill is not a
// tool call. Every zero-unrecognized drain in this package is a sentinel for the
// shapes its own turn produces, and no turn here produced this one.
//
// THE NON-VACUITY PROBLEM, and how it is solved. A turn in which claude never
// invoked the skill also produces zero unrecognized_message frames, so the drain's
// silence alone would read as a pass on a run that proved nothing. NEITHER
// available witness closes that on its own, and this test asserts BOTH:
//
//   - The daemon's own Debug record for the drop proves a harness-authored
//     user/text block reached streamsup.emitUser and was suppressed. It does NOT
//     prove the block was a skill body. The drop site is shared by both triggers,
//     and — the part that is easy to get wrong — the harness NUDGE is itself
//     stamped with the flag on this surface (dropped_lines_v2.1.220.json's one
//     `user` record is the nudge, and it carries `"isSynthetic": true`). So a
//     nudge fires the FLAG arm here, not the constant arm, and no content-free
//     `trigger` attribute at the drop site could ever tell the two apart. That is
//     why the discriminator is not logged: it could not work, quite apart from the
//     standing rule against a third attribute.
//   - skillReplyToken in the reply proves claude obtained content that exists
//     ONLY inside the skill file. It does NOT prove the harness injected it: the
//     prompt withholds the skill's path, but a determined search could still find
//     and Read the file, which delivers the body as a tool_result and exercises
//     none of this.
//
// TOGETHER they are the proof, and the drain's silence is the third leg. The
// skill provably loaded; a harness-authored user/text block provably arrived and
// was dropped; and zero unrecognized_message frames left the daemon. Had the
// skill's own line NOT been suppressed, its body would have surfaced on that
// third leg and the drain would have failed — which is precisely what this turn
// did before the fix. Forging the set would take a no-visible-output response to
// summon a nudge AND a visible reply carrying the token, which are contradictory.
//
// A third candidate was rejected outright: a file the skill tells claude to write.
// Same weakness as the token alone, plus it would require the test-authored skill
// to carry tool directives, and this file writes an instruction document that a
// live claude executes with --dangerously-skip-permissions. The body is
// deliberately inert, and the assertion that would have needed a side effect is
// the one that would have needed a permissive skill.
//
// The daemon is spawned with spawnBootstrapDaemonVerbose because at LevelInfo the
// drop record is not emitted at all and that leg of the witness would be absent on
// a healthy run.
//
// The drop-record leg goes red the day claude stops stamping the flag on a skill
// line, which is the one hop in streamsup.harnessNoOutputNudge's #2087 provenance
// that no committed capture pins. Red does NOT mean the daemon broke: it means the
// inference expired and the prefix fallback that docblock names becomes a real
// ticket. The token leg is what tells that apart from claude simply ignoring the
// skill — read which of the two failed.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state. The Go names are distinct from every
// sibling's — same package, so the files must not redeclare — but the VALUES are
// deliberately not: they are the same UUIDs streamBootstrapUUID/streamConvID use,
// which interactive_per_conversation_liveness_test.go already reuses too. Each
// test seeds its own temp HOME, so the values share nothing at runtime.
const (
	skillBootstrapUUID = "88888888-8888-4888-8888-888888888888"
	skillConvID        = "66666666-6666-4666-8666-666666666666"
)

// skillName is both the skill's declared name and its directory name, which
// claude requires to agree.
const skillName = "pyry-probe"

// skillBodyMarker is a distinctive filler string that exists ONLY inside the
// skill body. It is the needle for the content-free assertion; distinctive rather
// than realistic so a hit cannot be a coincidence in unrelated log prose, and the
// body below tells claude never to repeat it so a hit cannot be claude's own
// words either.
const skillBodyMarker = "pyry-2087-skill-body-marker-do-not-repeat"

// skillReplyToken is the word the skill body tells claude to reply with, and the
// second leg of the non-vacuity witness. It exists ONLY inside the skill file: the
// prompt never names it, and it is deliberately not a word a model would reach for
// unprompted, so a reply carrying it is a reply from a claude that read the body.
//
// Distinct from skillBodyMarker on purpose, and the two pull in opposite
// directions: the marker must never appear anywhere (it is the content-free
// needle), and this token must appear in the reply. One string could not be both.
const skillReplyToken = "pyry-2087-ack"

// skillDropMsg is the Debug message streamsup's drop site emits, as a LITERAL —
// the same rule streamsup's own harnessNudgeDropMsg states. A witness built from
// the production constant would follow a rewording of it green, and this message
// is the entire witness.
const skillDropMsg = "streamsup: dropping known harness user block"

// writeProbeSkill writes an INERT skill into the test's own temp HOME.
//
// Test-authored on purpose: lifting one from the operator's machine would make the
// assertion depend on a file this repository does not control, and would put that
// file's contents through a live model. Inert on purpose: a live claude runs this
// document as instructions under --dangerously-skip-permissions, so it names no
// tool, no path and no command. Everything it asks for is prose.
//
// 0o700 on the directories and 0o600 on the file, matching the mode
// WithWorktreeAuthenticated already uses for the credential file it seeds beside
// them.
func writeProbeSkill(t *testing.T, home string) { scenario.WriteProbeSkill(t, home) }

func TestInteractiveStreamSkillInvocationIsSilent(t *testing.T) {
	runParallel(t)
	// Parallel only through runParallel; see liveHome.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := liveHome(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// The stream-json interactive runner is the surface under test; the PTY path
	// has no such parser and no such diagnostic.
	writeStreamInteractiveConfig(t, home)
	writeProbeSkill(t, home)

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

	seedBootstrapRegistry(t, home, skillBootstrapUUID)
	seedBoundConversation(t, home, skillConvID, skillBootstrapUUID, workdir)

	// Verbose, so the daemon logs at slog.LevelDebug and the drop record — this
	// test's entire non-vacuity witness — is actually in the haystack. Its only
	// delta from the shared spawner is -pyry-verbose; its model alias is already
	// haiku, so no fourth near-copy of the spawner is needed.
	d := spawnBootstrapDaemonVerbose(t, home, workdir, claudeBin, relayURL)
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

	// The prompt names the skill and asks for nothing else, so the shortest path to
	// complying is to invoke it. The per-run nonce defeats accidental caching. It
	// deliberately does NOT name the skill's path: telling claude where the file is
	// invites a Read, which delivers the body as a tool_result and exercises none
	// of the arm under test.
	nonce := time.Now().UnixNano()
	sequence := uint64(1)
	w := scenario.Wire{Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, phone, initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}, Next: scenario.Receive(t, phone, initRecv)}
	scenario.SkillSilence(t, w, skillConvID, fmt.Sprintf("Use the %s skill, then do exactly what it says. run=%d", skillName, nonce), 180*time.Second, d.stderr.String)
}
