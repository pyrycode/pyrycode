package streamsup

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// #2064 — the spawn-time permission-mode write, the ack correlation, and the turn
// gate they arm together.

// --- the gate itself ---------------------------------------------------------

// TestPostureGate_ArmReleaseAndMismatch walks every transition of the one-string
// state machine, and the middle two rows are the substance: a release carrying the
// WRONG id must leave a gate closed, and a re-arm must retire the previous id.
//
// The re-arm row is the sole red for AC 4's "a replacement child is never released
// by its predecessor's ack" — under a design that kept a bool beside the id, or that
// released on any success, it is the only row that fires.
func TestPostureGate_ArmReleaseAndMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// steps runs the transitions under test; want is ready() afterwards.
		steps func(g *PostureGate)
		want  bool
	}{
		{
			name:  "the zero value is open, so a runner that never arms behaves as before",
			steps: func(*PostureGate) {},
			want:  true,
		},
		{
			name:  "arm closes",
			steps: func(g *PostureGate) { g.arm("7") },
			want:  false,
		},
		{
			name:  "a matching release opens",
			steps: func(g *PostureGate) { g.arm("7"); g.release("7") },
			want:  true,
		},
		{
			name:  "a mismatched release leaves it closed",
			steps: func(g *PostureGate) { g.arm("7"); g.release("8") },
			want:  false,
		},
		{
			name:  "an empty request_id cannot open an armed gate",
			steps: func(g *PostureGate) { g.arm("7"); g.release("") },
			want:  false,
		},
		{
			name:  "a re-arm retires the previous id, so its late ack is refused",
			steps: func(g *PostureGate) { g.arm("7"); g.arm("9"); g.release("7") },
			want:  false,
		},
		{
			name:  "the re-armed gate still opens on its own id",
			steps: func(g *PostureGate) { g.arm("7"); g.arm("9"); g.release("7"); g.release("9") },
			want:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := &PostureGate{}
			tc.steps(g)
			if got := g.ready(); got != tc.want {
				t.Errorf("ready() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPostureGate_NilReceiverIsOpen pins the nil-receiver contract sessionModelHold
// set the precedent for: a runner constructed with no gate must not branch on nil at
// the turn path, so the nil gate has to answer OPEN and swallow its mutators.
func TestPostureGate_NilReceiverIsOpen(t *testing.T) {
	t.Parallel()
	var g *PostureGate
	g.arm("1")
	g.release("1")
	if !g.ready() {
		t.Error("a nil gate reported closed; it must be open so no call site needs a nil branch")
	}
}

// TestPostureGate_NakIsRecordedAndRetargetRecovers walks the transitions a NAK adds.
// The two guard rows are the substance: refuse must NOT mark a gate nothing is waiting
// on (an open gate's id is "", which is exactly what a NAK carrying no request_id
// decodes to), and retarget must NOT close one (an in-band posture change happens
// mid-session, and closing on it would drop deliverSettingsInBand's own follow-on
// sends).
//
// The recovery rows are the MUST FIX itself: before them the gate's only opener was a
// spawn, so a NAK'd child stayed healthy and refused every turn forever.
func TestPostureGate_NakIsRecordedAndRetargetRecovers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		steps       func(g *PostureGate)
		wantOpen    bool
		wantRefused bool
	}{
		{
			name:        "a NAK for the armed id is recorded and keeps the gate closed",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("7") },
			wantOpen:    false,
			wantRefused: true,
		},
		{
			name:        "a NAK for a sibling request is not this gate's verdict",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("8") },
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "a NAK carrying no request_id cannot mark an OPEN gate refused",
			steps:       func(g *PostureGate) { g.refuse("") },
			wantOpen:    true,
			wantRefused: false,
		},
		{
			name:        "the next spawn clears the verdict with its own arm",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("7"); g.arm("9") },
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "an in-band write retargets a refused gate and clears the verdict",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("7"); g.retarget("9") },
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "the retargeted gate opens on the NEW id — the recovery",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("7"); g.retarget("9"); g.release("9") },
			wantOpen:    true,
			wantRefused: false,
		},
		{
			name:        "the NAK'd id cannot open the retargeted gate",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("7"); g.retarget("9"); g.release("7") },
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "retarget never closes an open gate",
			steps:       func(g *PostureGate) { g.arm("7"); g.release("7"); g.retarget("9") },
			wantOpen:    true,
			wantRefused: false,
		},
		{
			name:        "retarget on a never-armed gate is a no-op",
			steps:       func(g *PostureGate) { g.retarget("9") },
			wantOpen:    true,
			wantRefused: false,
		},
		{
			name:        "a pending gate retargets too: the later write is the posture in force",
			steps:       func(g *PostureGate) { g.arm("7"); g.retarget("9"); g.release("9") },
			wantOpen:    true,
			wantRefused: false,
		},
		{
			name:        "a success for the armed id clears a verdict a mismatched NAK never set",
			steps:       func(g *PostureGate) { g.arm("7"); g.refuse("7"); g.release("7") },
			wantOpen:    true,
			wantRefused: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := &PostureGate{}
			tc.steps(g)
			if got := g.ready(); got != tc.wantOpen {
				t.Errorf("ready() = %v, want %v", got, tc.wantOpen)
			}
			if got := g.refusedByClaude(); got != tc.wantRefused {
				t.Errorf("refusedByClaude() = %v, want %v", got, tc.wantRefused)
			}
		})
	}
}

// TestPostureGate_NilReceiverSwallowsTheNakPath extends the nil contract to the two
// methods this slice adds, for the reason the original states: no call site may need a
// nil branch.
func TestPostureGate_NilReceiverSwallowsTheNakPath(t *testing.T) {
	t.Parallel()
	var g *PostureGate
	g.refuse("1")
	g.retarget("2")
	if !g.ready() {
		t.Error("a nil gate reported closed")
	}
	if g.refusedByClaude() {
		t.Error("a nil gate reported a refusal; it has no request to refuse")
	}
}

// --- the ack read ------------------------------------------------------------

// capturedModeAck is claude 2.1.239's own set_permission_mode ack, copied verbatim
// from internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json.
// Read as a literal rather than from the file: this package must not reach into the
// realclaude fixtures, and the shape assertion wants the bytes in front of the reader.
const capturedModeAck = `{"type":"control_response","response":{"subtype":"success","request_id":"set-permission-mode-reescalate","response":{"mode":"default"}}}`

// TestParser_NoteControlAck_ReleasesOnlyItsOwnAck is AC 2. Every row but the first
// is a way the gate must NOT open, and the initialize row is the one the ticket calls
// out as real rather than hypothetical: RequestInitializeOnSpawn writes its ask at the
// same spawn off the same counter, so its ack reaches this same function.
func TestParser_NoteControlAck_ReleasesOnlyItsOwnAck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		armedID     string
		line        string
		wantRelease bool
	}{
		{
			name:        "the captured ack releases the gate armed on its id",
			armedID:     "set-permission-mode-reescalate",
			line:        capturedModeAck,
			wantRelease: true,
		},
		{
			name:        "a minted-id ack in the daemon's own shape releases",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"success","request_id":"3","response":{"mode":"plan"}}}`,
			wantRelease: true,
		},
		{
			name:        "the sibling initialize ack does not release",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"success","request_id":"4","response":{"models":[{"model":"m"}]}}}`,
			wantRelease: false,
		},
		{
			name:        "a non-success subtype does not release",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"error","request_id":"3","error":"nope"}}`,
			wantRelease: false,
		},
		{
			name:        "an absent subtype does not release",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"request_id":"3"}}`,
			wantRelease: false,
		},
		{
			name:        "a numeric request_id does not release",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"success","request_id":3}}`,
			wantRelease: false,
		},
		{
			name:        "an undecodable shape does not release",
			armedID:     "3",
			line:        `{"type":"control_response","response":[1,2,3]}`,
			wantRelease: false,
		},
		{
			name:        "an ack carrying no request_id does not release",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"success"}}`,
			wantRelease: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewParser(func(turnevent.Event) {}, nil)
			g := p.PostureGate()
			g.arm(tc.armedID)
			// Through Write, not by calling noteControlAck directly: the property
			// under test includes that consumeLine's control_response arm reaches it.
			if _, err := p.Write([]byte(tc.line + "\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := g.ready(); got != tc.wantRelease {
				t.Errorf("gate open = %v after %s, want %v", got, tc.line, tc.wantRelease)
			}
		})
	}
}

// TestParser_NoteControlAck_LeavesModelListRungsAlone is the non-disturbance claim
// made testable, and it is why the ack decodes into its OWN target rather than a
// request_id field added to controlResponseLine. Under that rejected design the
// numeric-request_id row fails the whole-line decode, so a payload that reaches the
// model-list rung today would newly land on the undecodable one and emit nothing.
func TestParser_NoteControlAck_LeavesModelListRungsAlone(t *testing.T) {
	t.Parallel()
	const modelsWithNumericID = `{"type":"control_response","response":{"subtype":"success","request_id":7,"response":{"models":[{"model":"claude-x","displayName":"X"}]}}}`
	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, nil)
	p.PostureGate().arm("7")
	if _, err := p.Write([]byte(modelsWithNumericID + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("emitted %d event(s), want 1 ModelList — a request_id field on controlResponseLine would have pushed this line onto the undecodable rung: %#v", len(got), got)
	}
	if p.PostureGate().ready() {
		t.Error("a numeric request_id released the gate; correlation must fail closed")
	}
}

// --- the spawn-time write ----------------------------------------------------

// permissionModeAsks returns the mode of every set_permission_mode control_request
// the echo_lines child echoed back, in arrival order, paired with its request id.
// The twin of initializeAsks, and it reads the same faithful stdin transcript.
func permissionModeAsks(output string) (modes, ids []string) {
	for _, line := range findEchoedLines(output) {
		var cr decodedControlRequest
		if err := json.Unmarshal([]byte(line), &cr); err != nil {
			continue
		}
		if cr.Request.Subtype == "set_permission_mode" {
			modes = append(modes, cr.Request.Mode)
			ids = append(ids, cr.RequestID)
		}
	}
	return modes, ids
}

// TestRunner_SpawnPermissionMode_WrittenOncePerChild is AC 1. The bypass row is the
// one that has to hold as hard as the positive ones: that session is sent nothing AND
// its gate is never armed, so its turns flow exactly as they do today. A gate no
// write can ever release is a bricked session, not a fail-closed one.
//
// The two turns are the barrier and the cardinality oracle both, for
// TestRunner_RequestInitializeOnSpawn_OncePerChild's reason verbatim: "once per
// spawn" and "once per turn" are indistinguishable in a single-turn test.
func TestRunner_SpawnPermissionMode_WrittenOncePerChild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      string
		wantModes []string
	}{
		{name: "default is written like any other mode", mode: "default", wantModes: []string{"default"}},
		{name: "a non-default in-band mode is written", mode: "plan", wantModes: []string{"plan"}},
		{name: "bypassPermissions is sent nothing", mode: "bypassPermissions", wantModes: nil},
		{name: "the zero value is sent nothing", mode: "", wantModes: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, stderr := &safeBuffer{}, &safeBuffer{}
			cfg := helperRunCfg(t, "echo_lines", out, stderr)
			cfg.SpawnPermissionMode = tc.mode
			gate := &PostureGate{}
			cfg.PostureGate = gate
			spawned := make(chan struct{}, 1)
			cfg.onSpawn = func(int) {
				select {
				case spawned <- struct{}{}:
				default:
				}
			}
			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()

			select {
			case <-spawned:
			case <-time.After(5 * time.Second):
				t.Fatal("child never spawned")
			}
			waitForContains(t, out, "READY", 3*time.Second)

			// The gate is armed for every row that writes, and nothing here plays
			// claude, so it has to be opened by hand before a turn can pass.
			if len(tc.wantModes) > 0 {
				if gate.ready() {
					t.Fatal("the gate was open before any ack; a spawn that writes must arm it")
				}
				gate.release(gate.armedIDForTest())
			} else if !gate.ready() {
				t.Fatal("the gate was armed for a spawn that writes nothing — that session can never be released")
			}

			const lastMarker = "spawn-mode-turn-2"
			for _, marker := range []string{"spawn-mode-turn-1", lastMarker} {
				if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
					t.Fatalf("WriteUserTurn(%q) on a live released child: %v", marker, err)
				}
			}
			waitForContains(t, out, lastMarker, 3*time.Second)

			modes, ids := permissionModeAsks(out.String())
			if len(modes) != len(tc.wantModes) {
				t.Fatalf("child was sent %d set_permission_mode line(s) across two turns %v, want %v:\n%s",
					len(modes), modes, tc.wantModes, out.String())
			}
			for i, want := range tc.wantModes {
				if modes[i] != want {
					t.Errorf("write %d carried mode %q, want %q", i, modes[i], want)
				}
				if ids[i] == "" {
					t.Errorf("write %d carried an empty request_id, want a locally-minted id", i)
				}
			}
		})
	}
}

// TestRunner_SpawnPermissionMode_DistinctFromInitializeID pins that the two spawn-time
// writes draw distinct ids off the one counter. It is what makes AC 2's "its ack must
// not release this gate" a statement about real bytes rather than a hypothetical: a
// design that reused one id per spawn would let the initialize ack open the gate.
func TestRunner_SpawnPermissionMode_DistinctFromInitializeID(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	gate := &PostureGate{}
	cfg.PostureGate = gate
	cfg.RequestInitializeOnSpawn = true
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)
	gate.release(gate.armedIDForTest())
	const marker = "distinct-id-barrier"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
		t.Fatalf("WriteUserTurn: %v", err)
	}
	waitForContains(t, out, marker, 3*time.Second)

	_, modeIDs := permissionModeAsks(out.String())
	initIDs := initializeAsks(out.String())
	if len(modeIDs) != 1 || len(initIDs) != 1 {
		t.Fatalf("want one write of each subtype, got %d posture and %d initialize:\n%s",
			len(modeIDs), len(initIDs), out.String())
	}
	if modeIDs[0] == initIDs[0] {
		t.Errorf("both spawn-time writes carried request_id %q; the initialize ack would release the posture gate", modeIDs[0])
	}
}

// TestRunner_WriteUserTurn_HeldUntilAck is AC 3's hermetic half. ZERO BYTES is the
// assertion that matters: a gate that refused with the right error while still
// writing the envelope would satisfy an error-only check and deliver the turn anyway.
func TestRunner_WriteUserTurn_HeldUntilAck(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	gate := &PostureGate{}
	cfg.PostureGate = gate
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	const heldMarker = "held-before-ack"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(heldMarker)); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn while the posture is unconfirmed = %v, want ErrNoLiveChild (the retryable classification a rotation already returns)", err)
	}

	const flowMarker = "flows-after-ack"
	gate.release(gate.armedIDForTest())
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(flowMarker)); err != nil {
		t.Fatalf("WriteUserTurn after the ack landed: %v", err)
	}
	waitForContains(t, out, flowMarker, 3*time.Second)

	// The barrier above proves every byte written to this child has come back, so a
	// held marker absent HERE was never written rather than merely not yet echoed.
	if strings.Contains(out.String(), heldMarker) {
		t.Errorf("the refused turn reached the child anyway:\n%s", out.String())
	}
}

// TestNew_SpawnPermissionModeRequiresGate pins the composition failure loud. A mode
// with no gate would write the line and gate nothing — the unenforced-write shape
// #2065 cannot tolerate — so it is a startup error, not a degraded mode.
func TestNew_SpawnPermissionModeRequiresGate(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted SpawnPermissionMode with no PostureGate; that composition writes a posture nothing enforces")
	}
}

// TestRunner_SetSpawnPermissionMode_TakesEffectOnTheNextSpawn is the staleness fix.
// Pool.UpdateSettings rebuilds no runner, so without this the second child would
// assert the posture the FIRST one was constructed with — which can loosen a posture
// the operator has tightened.
func TestRunner_SetSpawnPermissionMode_TakesEffectOnTheNextSpawn(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	gate := &PostureGate{}
	cfg.PostureGate = gate
	spawned := make(chan struct{}, 2)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("first child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	// The call must not write into the LIVE child: it installs the next spawn's
	// posture, which is what separates it from SetPermissionMode.
	r.SetSpawnPermissionMode("plan")
	gate.release(gate.armedIDForTest())
	const barrier = "set-spawn-mode-barrier"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(barrier)); err != nil {
		t.Fatalf("WriteUserTurn: %v", err)
	}
	waitForContains(t, out, barrier, 3*time.Second)
	if modes, _ := permissionModeAsks(out.String()); len(modes) != 1 || modes[0] != "default" {
		t.Fatalf("the live child saw %v, want exactly [default] — SetSpawnPermissionMode must not write in-band", modes)
	}

	// echo_lines never self-exits, so the second spawn is attributable to Restart.
	r.Restart(cfg.Args)
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)
	gate.release(gate.armedIDForTest())
	const barrier2 = "set-spawn-mode-barrier-2"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(barrier2)); err != nil {
		t.Fatalf("WriteUserTurn after respawn: %v", err)
	}
	waitForContains(t, out, barrier2, 3*time.Second)

	modes, _ := permissionModeAsks(out.String())
	if len(modes) != 2 || modes[1] != "plan" {
		t.Fatalf("across two children the child saw %v, want the second to be plan", modes)
	}
}

// TestRunner_SpawnPermissionMode_ResidualArmDoesNotBrickABypassRespawn is AC 1's
// bypass clause held across a RESPAWN, which is the only place it can actually fail.
// The gate outlives the child — Restart reuses this Runner and this PostureGate — so a
// spawn that writes nothing has to install the OPEN state rather than leave whatever
// its predecessor armed.
//
// The sequence is the operator one, not a synthetic one: a default child comes up and
// never acks (it crashed, claude NAK'd, or the write hit EPIPE — AC 4 keeps the gate
// closed for all three), then the posture is escalated to bypassPermissions and a later
// spawn asserts it. #2066 changed which spawn that is, and not the sequence: the
// escalation used to be undeliverable in band and so took Pool.UpdateSettings' RESTART
// branch; it now goes in band and installs the escalation through
// SetSpawnPermissionMode, so the spawn that inherits the residual arm is the next
// CRASH-respawn (or a restart driven by a Model or Effort cleared to ""). Either way a
// bypass spawn meets a gate its predecessor armed, which is the only thing this test
// judges. With the
// arm nested inside the admissibility check the replacement child inherited id "1",
// which nothing can ever ack, and every turn on that session refused forever — under
// the RETRYABLE classification, so callers retry against a gate with no opener.
//
// Not releasing the first gate is the whole point of the test.
// TestRunner_SetSpawnPermissionMode_TakesEffectOnTheNextSpawn releases before each
// respawn and TestRunner_SpawnPermissionMode_WrittenOncePerChild only ever judges a
// FRESH gate, where open is the zero value and the bypass assertion is vacuous.
func TestRunner_SpawnPermissionMode_ResidualArmDoesNotBrickABypassRespawn(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	gate := &PostureGate{}
	cfg.PostureGate = gate
	spawned := make(chan struct{}, 2)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("first child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	// Non-vacuity: the residual arm has to be real, or the assertion below passes
	// against a gate that was open the whole time.
	residual := gate.armedIDForTest()
	if residual == "" {
		t.Fatal("the default spawn left the gate open; there is no residual arm for the respawn to inherit")
	}

	// The escalation the SPAWN path refuses — permissionModeSpawnWritable, which
	// subtracts it back out of the writer's allow-list (#2066). echo_lines never
	// self-exits, so the second spawn is attributable to Restart.
	r.SetSpawnPermissionMode("bypassPermissions")
	r.Restart(cfg.Args)
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	if got := gate.armedIDForTest(); got != "" {
		t.Fatalf("after a bypass respawn the gate is armed on %q (the predecessor armed %q); "+
			"a spawn that writes nothing must install the OPEN state, or the session is bricked", got, residual)
	}

	// The error alone is not the oracle — the bytes have to reach the child.
	const marker = "residual-arm-turn"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
		t.Fatalf("WriteUserTurn to a bypass child: %v; AC 1 says its turns flow exactly as they do today", err)
	}
	waitForContains(t, out, marker, 3*time.Second)

	// And the refusal is still by non-membership: the bypass spawn wrote nothing, so
	// the only ask across both children is the first one's.
	if modes, _ := permissionModeAsks(out.String()); len(modes) != 1 || modes[0] != "default" {
		t.Fatalf("across both children the child saw %v, want exactly [default] — bypass is sent nothing", modes)
	}
}

// TestRunner_SpawnPermissionMode_ProvenanceDecidesTheWrite is #2065 AC 4, one row
// per row of that ticket's fail-safe-2 table. It pins that the spawn-time posture
// write is decided by the PROVENANCE of the escalation, not by its presence in the
// argv.
//
// # Why presence stopped being an answer
//
// #2064 keyed this on the argv, and that was right at the time: the escalation
// reached a spawn's argv from exactly two places, and only one of them was
// modelled by the stored posture. #2065 makes sessions.claudeSettingsArgs append
// the flag to EVERY argv, so a predicate reading the argv answers "operator
// bypass" for every session, no child is ever sent its stored posture, and every
// child stays in the bypass it launched with — the exact inverse of that ticket,
// shipped green and silent. The signal that survives is
// sessions.RunnerConfig.OperatorBypass, derived from the settings-free spawnBase
// and carried here as Config.OperatorBypass.
//
// # Measured provenance, not reasoned
//
// #2064's first shape keyed the write on the stored posture alone and recorded an
// argv interlock as deliberately rejected; the live-claude gate then reddened four
// TestInteractiveStream* specs that pass on main. Each spawns through
// spawnBootstrapDaemon's pass-through bypass with a stored posture of default, and
// the write silently revoked the escalation the operator had asked for — real
// claude answered with permission_denied on Bash and on Write, and refused to Read
// an attached file. Neither this package nor the fake-daemon suite could see it,
// because both drive the stored posture and neither spawns through the
// pass-through.
//
// # Which row kills which tree
//
//   - "daemon-composed" is the row that reddens on any tree keying on the bare
//     presence of the flag. Its argv CARRIES the escalation — as every #2065 argv
//     does — and the mode is written anyway.
//   - "operator pass-through" is the row that reddens on any tree that deletes the
//     interlock and trusts the stored posture alone. Same argv, same stored mode
//     as the row above; only the provenance bit differs, so the pair cannot both
//     pass on either mistake.
//   - "the escalation is sent nothing" is the non-membership row, unchanged from
//     #2064 and here so a rewrite cannot quietly drop it.
//
// # Measured 2026-09-03, not predicted
//
// Both mutants applied through `go test -overlay`, so no mutated source was ever
// written into the worktree. Each reddened exactly the row named for it and no
// other:
//
//	M1  !slices.Contains(args, bypassPermissionsFlag)  — #2064's argv read
//	    RED: "a daemon-composed bypass argv is still written its stored mode"
//	    ("the gate was open before any ack; a spawn that writes must arm it")
//	M2  the second clause deleted entirely
//	    RED: "the operator's pass-through bypass suppresses the write"
//	    ("the gate is armed on \"1\" after a spawn that writes nothing")
func TestRunner_SpawnPermissionMode_ProvenanceDecidesTheWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		mode           string
		args           []string
		operatorBypass bool
		wantModes      []string
	}{
		{
			// Fail-safe-2 table, row 2: the daemon composed this bypass itself, so
			// the stored posture decides and the child is walked back to it.
			name:           "a daemon-composed bypass argv is still written its stored mode",
			mode:           "default",
			args:           []string{bypassPermissionsFlag},
			operatorBypass: false,
			wantModes:      []string{"default"},
		},
		{
			// Row 3: byte-identical argv and stored mode to the row above. Only the
			// provenance differs, and it is the whole decision.
			name:           "the operator's pass-through bypass suppresses the write",
			mode:           "default",
			args:           []string{bypassPermissionsFlag},
			operatorBypass: true,
			wantModes:      nil,
		},
		{
			// Row 1: refused by non-membership before provenance is consulted at all.
			name:           "the escalation is sent nothing whatever the provenance",
			mode:           "bypassPermissions",
			args:           []string{bypassPermissionsFlag},
			operatorBypass: false,
			wantModes:      nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, stderr := &safeBuffer{}, &safeBuffer{}
			cfg := helperRunCfg(t, "echo_lines", out, stderr)
			cfg.SpawnPermissionMode = tc.mode
			cfg.Args = tc.args
			cfg.OperatorBypass = tc.operatorBypass
			gate := &PostureGate{}
			cfg.PostureGate = gate
			spawned := make(chan struct{}, 1)
			cfg.onSpawn = func(int) {
				select {
				case spawned <- struct{}{}:
				default:
				}
			}
			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()

			select {
			case <-spawned:
			case <-time.After(5 * time.Second):
				t.Fatal("child never spawned")
			}
			waitForContains(t, out, "READY", 3*time.Second)

			if len(tc.wantModes) == 0 {
				// Nothing was written, so nothing can ever ack: the gate must be OPEN
				// or this session refuses every turn for the rest of its life.
				if !gate.ready() {
					t.Fatalf("the gate is armed on %q after a spawn that writes nothing; "+
						"a bypass child has no ack coming and its turns must flow as they do today",
						gate.armedIDForTest())
				}
			} else {
				if gate.ready() {
					t.Fatal("the gate was open before any ack; a spawn that writes must arm it")
				}
				gate.release(gate.armedIDForTest())
			}

			// The error is not the oracle on either row — the bytes have to reach the child.
			const marker = "argv-bypass-turn"
			if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
				t.Fatalf("WriteUserTurn: %v", err)
			}
			waitForContains(t, out, marker, 3*time.Second)

			modes, _ := permissionModeAsks(out.String())
			if len(modes) != len(tc.wantModes) {
				t.Fatalf("child was sent %d set_permission_mode line(s) %v, want %v:\n%s",
					len(modes), modes, tc.wantModes, out.String())
			}
			for i, want := range tc.wantModes {
				if modes[i] != want {
					t.Errorf("write %d carried mode %q, want %q", i, modes[i], want)
				}
			}
		})
	}
}

// --- claude's refusal, and the way out of it ---------------------------------

// TestParser_NoteControlAck_NakMarksTheArmedRequestRefused is the read half of the NAK
// policy: consumeLine's arm has to reach refuse, and refuse has to be as selective as
// release. The last row is the guard — a NAK carrying no request_id decodes to "", which
// is an OPEN gate's own id, so an unguarded refuse would report a refusal on a session
// with nothing outstanding.
func TestParser_NoteControlAck_NakMarksTheArmedRequestRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		armedID     string // "" arms nothing, leaving the gate open
		line        string
		wantOpen    bool
		wantRefused bool
	}{
		{
			name:        "a NAK for the armed id is recorded",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"error","request_id":"3","error":"unsupported mode"}}`,
			wantOpen:    false,
			wantRefused: true,
		},
		{
			name:        "the sibling initialize NAK is not this request's verdict",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"error","request_id":"4","error":"nope"}}`,
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "an absent subtype is not an answer to anything",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"request_id":"3"}}`,
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "a numeric request_id cannot be correlated in either direction",
			armedID:     "3",
			line:        `{"type":"control_response","response":{"subtype":"error","request_id":3}}`,
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "an undecodable line records nothing",
			armedID:     "3",
			line:        `{"type":"control_response","response":[1,2,3]}`,
			wantOpen:    false,
			wantRefused: false,
		},
		{
			name:        "a NAK with no request_id cannot mark an OPEN gate refused",
			armedID:     "",
			line:        `{"type":"control_response","response":{"subtype":"error"}}`,
			wantOpen:    true,
			wantRefused: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewParser(func(turnevent.Event) {}, nil)
			g := p.PostureGate()
			g.arm(tc.armedID)
			if _, err := p.Write([]byte(tc.line + "\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := g.ready(); got != tc.wantOpen {
				t.Errorf("gate open = %v after %s, want %v", got, tc.line, tc.wantOpen)
			}
			if got := g.refusedByClaude(); got != tc.wantRefused {
				t.Errorf("refusedByClaude() = %v after %s, want %v", got, tc.line, tc.wantRefused)
			}
		})
	}
}

// TestRunner_NakThenInBandModeChange_RecoversTheSession drives the whole operator
// sequence the second rework cycle blocked on: claude ANSWERS the spawn-time write with
// a refusal, so the child stays healthy and nothing else ever spawns. Before the fix the
// gate's only opener was a spawn and its only closer a NAK it discarded, so every turn
// on that session refused forever under the RETRYABLE classification — and the
// documented remedy could not reach it, because a corrective mode change is
// in-band-deliverable and so restarts nothing.
//
// The parser is driven with claude's line directly, as the ack table above does: the
// gate travels the production binding (minted by the parser, armed by the runner,
// released through consumeLine), but echo_lines cannot mint a control_response of its
// own.
func TestRunner_NakThenInBandModeChange_RecoversTheSession(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	p := NewParser(func(turnevent.Event) {}, nil)
	gate := p.PostureGate()
	cfg.PostureGate = gate
	rec := &logRecorder{}
	cfg.Logger = slog.New(rec)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	spawnID := gate.armedIDForTest()
	if spawnID == "" {
		t.Fatal("the default spawn left the gate open; there is no request for claude to refuse")
	}

	// claude declines the posture. Documented behaviour, not a hypothetical: it refuses
	// auto per MODEL (turnevent.ModelOption.SupportsAutoMode), and nothing re-checks
	// that pairing when either the mode or the model changes.
	nak := `{"type":"control_response","response":{"subtype":"error","request_id":"` + spawnID + `","error":"unsupported"}}`
	if _, err := p.Write([]byte(nak + "\n")); err != nil {
		t.Fatalf("Write(nak): %v", err)
	}
	if gate.ready() {
		t.Fatal("a NAK opened the gate; a refused posture must not admit turns — under #2065 that child is still in bypass")
	}
	if !gate.refusedByClaude() {
		t.Fatal("the NAK was dropped; the daemon holds a definitive answer and must be able to act on it")
	}

	const heldMarker = "nakked-turn"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(heldMarker)); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn against a NAK'd posture = %v, want ErrNoLiveChild", err)
	}

	// The operator's only evidence, and it has to name the cause: "not yet confirmed"
	// describes a round trip that has already finished.
	const refusedMsg = "streamsup: turn refused; claude refused this session's permission posture — change the posture or restart the session"
	refusals := rec.withMessage(refusedMsg)
	if len(refusals) != 1 {
		t.Fatalf("captured %d refusal record(s), want exactly 1: %+v", len(refusals), rec.all())
	}
	if refusals[0].level != slog.LevelWarn {
		t.Errorf("the refusal was recorded at %v, want Warn — a Debug record anywhere in the daemon's stderr defeats #1330's e2e instrument guard, and Info is the PENDING state's level", refusals[0].level)
	}
	if len(refusals[0].attrs) != 1 || refusals[0].attrs["session"] == "" {
		t.Errorf("the refusal record carried %v; the session id is the only field it may carry — never the mode, the request id, or claude's own text", refusals[0].attrs)
	}
	if pending := rec.withMessage("streamsup: turn refused; permission posture not yet confirmed by claude"); len(pending) != 0 {
		t.Errorf("the answered state was reported as pending %d time(s); the two states need different operator actions", len(pending))
	}

	// The remedy: an in-band posture change, which never restarts the child.
	if err := r.SetPermissionMode("plan"); err != nil {
		t.Fatalf("SetPermissionMode on a live child: %v", err)
	}
	recoveryID := gate.armedIDForTest()
	if recoveryID == "" || recoveryID == spawnID {
		t.Fatalf("after the corrective write the gate is armed on %q (spawn armed %q); it must track the write whose ack can actually arrive", recoveryID, spawnID)
	}
	if gate.refusedByClaude() {
		t.Error("the retargeted gate still carries the previous request's verdict")
	}

	ack := `{"type":"control_response","response":{"subtype":"success","request_id":"` + recoveryID + `","response":{"mode":"plan"}}}`
	if _, err := p.Write([]byte(ack + "\n")); err != nil {
		t.Fatalf("Write(ack): %v", err)
	}
	if !gate.ready() {
		t.Fatal("the ack for the corrective write did not open the gate; the session is still unrecoverable")
	}

	const flowMarker = "recovered-turn"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(flowMarker)); err != nil {
		t.Fatalf("WriteUserTurn after the recovery ack: %v", err)
	}
	waitForContains(t, out, flowMarker, 3*time.Second)

	// The barrier above proves every byte written to this child has come back, so the
	// held marker's absence here is "never written" rather than "not yet echoed".
	if strings.Contains(out.String(), heldMarker) {
		t.Errorf("the turn refused during the NAK reached the child anyway:\n%s", out.String())
	}
}

// TestRunner_SetPermissionMode_LeavesAnOpenGateOpen is the other half of the retarget
// rule, and it guards a regression the recovery would otherwise introduce.
// Pool.deliverSettingsInBand writes the posture and then sends /model and /effort
// through WriteUserTurn in the SAME call, so a retarget that closed a confirmed gate
// would drop those sends while UpdateSettings still reported success.
func TestRunner_SetPermissionMode_LeavesAnOpenGateOpen(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	cfg.SpawnPermissionMode = "default"
	gate := &PostureGate{}
	cfg.PostureGate = gate
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)
	gate.release(gate.armedIDForTest())

	// deliverSettingsInBand's own order: the posture write, then the follow-on send with
	// no wait for an ack in between.
	if err := r.SetPermissionMode("plan"); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}
	const marker = "in-band-follow-on-send"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
		t.Fatalf("the send following an in-band posture change was refused: %v; an open gate must stay open", err)
	}
	waitForContains(t, out, marker, 3*time.Second)
}

// TestRunner_SetPermissionMode_FailedWriteDoesNotRetarget pins the reject row. A line
// that never reached the child will never be acked, so pointing the gate at its id
// would trade a pending id that might still be acked for one that cannot be. The runner
// is deliberately never Run: Stdin() is nil, which is the no-live-child refusal.
func TestRunner_SetPermissionMode_FailedWriteDoesNotRetarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    string
		wantErr error
	}{
		{name: "no live child", mode: "plan", wantErr: ErrNoLiveChild},
		// A near miss of the escalation, which #2066 admitted to the allow-list. The
		// row exists for the vocabulary-refusal arm of "a failed write does not
		// retarget", so it needs a mode the list still refuses — and the escalation
		// itself is now covered by the live-child arms above it.
		{name: "a mode the allow-list refuses", mode: "Bypasspermissions", wantErr: ErrUnsupportedPermissionMode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
			cfg.SpawnPermissionMode = "default"
			gate := &PostureGate{}
			cfg.PostureGate = gate
			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gate.arm("1")
			if err := r.SetPermissionMode(tc.mode); !errors.Is(err, tc.wantErr) {
				t.Fatalf("SetPermissionMode(%q) = %v, want %v", tc.mode, err, tc.wantErr)
			}
			if got := gate.armedIDForTest(); got != "1" {
				t.Errorf("the gate is armed on %q, want the pending spawn id \"1\" — a write that failed cannot be acked", got)
			}
		})
	}
}

// armedIDForTest reads the gate's armed id under its own mutex, so a test can play
// claude's ack without the race detector seeing an unsynchronised field read. It
// lives in this file rather than beside the type: production has no reader for it —
// the id flows from the arm site into the written line and is never fetched back.
func (g *PostureGate) armedIDForTest() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armedID
}
