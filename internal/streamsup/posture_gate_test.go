package streamsup

import (
	"context"
	"encoding/json"
	"errors"
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

// armedIDForTest reads the gate's armed id under its own mutex, so a test can play
// claude's ack without the race detector seeing an unsynchronised field read. It
// lives in this file rather than beside the type: production has no reader for it —
// the id flows from the arm site into the written line and is never fetched back.
func (g *PostureGate) armedIDForTest() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armedID
}
