package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// openVersionConn drives one real IK handshake for v2TestToken whose hello
// reports clientVersion, and returns once the conn is open. The version write
// sits ahead of the V2StateOpen transition, so waitConnOpen is the
// happens-before edge the callers' disk assertions rely on.
func openVersionConn(t *testing.T, mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, respPub []byte, connID, clientVersion string) {
	t.Helper()
	initPriv, _ := genV2Keypair(t)
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator(%s): %v", connID, err)
	}
	initMsg, err := initiator.WriteInit(buildHelloIdentityEarlyData(t, v2TestToken, v2TestDevName, clientVersion))
	if err != nil {
		t.Fatalf("WriteInit(%s): %v", connID, err)
	}
	frames <- wrapInnerFrame(t, connID, protocol.TypeNoiseInit, initMsg)
	waitConnOpen(t, mgr, connID)
}

// TestV2Session_ClientVersion_PersistedAndReplaced is AC-1: an accepted hello's
// client_version reaches the devices file — read by a fresh Load, the view
// `pyry pair list` has — and a later hello with a different version replaces it.
func TestV2Session_ClientVersion_PersistedAndReplaced(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Time{})
	mgr, frames, _, respPub := startRedemptionManager(t, reg, path)

	openVersionConn(t, mgr, frames, respPub, "c-ver-1", "pyrycode-android/1.4.0")
	if got := diskDevice(t, path).ClientVersion; got != "pyrycode-android/1.4.0" {
		t.Fatalf("after the first hello, on-disk ClientVersion = %q, want %q", got, "pyrycode-android/1.4.0")
	}

	openVersionConn(t, mgr, frames, respPub, "c-ver-2", "pyrycode-android/1.5.0")
	if got := diskDevice(t, path).ClientVersion; got != "pyrycode-android/1.5.0" {
		t.Fatalf("after the second hello, on-disk ClientVersion = %q, want %q", got, "pyrycode-android/1.5.0")
	}
}

// TestV2Session_ClientVersion_UnchangedTakesNoLock pins the Technical Notes'
// write-only-on-change rule: a hello reporting the version already stored does
// no file lock and no Save. The sidecar's absence proves the region was never
// entered; the untouched mtime proves no Save ran.
func TestV2Session_ClientVersion_UnchangedTakesNoLock(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Time{})
	if !reg.SetClientVersion(devices.HashToken(v2TestToken), "1.4.0") {
		t.Fatal("seed SetClientVersion reported no change")
	}
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	before := backdate(t, path)

	mgr, frames, _, respPub := startRedemptionManager(t, reg, path)
	openVersionConn(t, mgr, frames, respPub, "c-ver-same", "1.4.0")

	assertNotRewritten(t, path, before)
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(%s.lock) = %v, want ErrNotExist — the lock was acquired for an unchanged version", path, err)
	}
}

// TestV2Session_ClientVersion_InadmissibleStoredEmpty is AC-2 through the real
// handshake: a reported version admitClient's gate refuses is stored as empty,
// replacing a previously stored valid one, and the key leaves the file.
func TestV2Session_ClientVersion_InadmissibleStoredEmpty(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Time{})
	mgr, frames, _, respPub := startRedemptionManager(t, reg, path)

	openVersionConn(t, mgr, frames, respPub, "c-ver-ok", "1.4.0")
	if got := diskDevice(t, path).ClientVersion; got != "1.4.0" {
		t.Fatalf("seeding hello: on-disk ClientVersion = %q, want %q", got, "1.4.0")
	}

	openVersionConn(t, mgr, frames, respPub, "c-ver-bad", `1.4.0" injected`)
	if got := diskDevice(t, path).ClientVersion; got != "" {
		t.Fatalf("after an inadmissible version, on-disk ClientVersion = %q, want empty", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read devices.json: %v", err)
	}
	if strings.Contains(string(raw), `"client_version"`) {
		t.Fatalf("devices.json still carries a client_version key:\n%s", raw)
	}
}

// TestV2Session_ClientVersion_HandlerSeesCurrentHello is #2704's stale-snapshot
// case: on the first connection after an upgrade the device record still holds
// the previous release's ClientVersion, and a handler reading c.Auth() — as
// send_message does to store the version on each message — must see the version
// THIS hello reported, admitted, not the record's old one.
func TestV2Session_ClientVersion_HandlerSeesCurrentHello(t *testing.T) {
	t.Parallel()
	const stored = "pyrycode-android/1.4.0"
	tests := []struct {
		name     string
		reported string
		want     string
	}{
		{"upgraded version", "pyrycode-android/1.5.0", "pyrycode-android/1.5.0"},
		{"inadmissible version reads empty", `1.5.0" injected`, ""},
		{"no version reads empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			if !reg.SetClientVersion(devices.HashToken(v2TestToken), stored) {
				t.Fatal("seed SetClientVersion reported no change")
			}

			seen := make(chan string, 1)
			handlers := map[string]dispatch.Handler{
				protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
					if auth := c.Auth(); auth != nil {
						seen <- auth.ClientVersion
					}
					return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(`{}`))
				},
			}
			frames := make(chan protocol.RoutingEnvelope, 2)
			rec := &v2Recorder{}
			_, stop := startManager(t, V2SessionConfig{
				Frames:     frames,
				Outbound:   rec.outbound,
				StaticPriv: respPriv,
				Devices:    reg,
				ServerID:   v2TestServerID,
				Logger:     silentLogger(),
				Handlers:   handlers,
			})
			t.Cleanup(stop)

			initiator, err := noise.NewInitiator(initPriv, respPub)
			if err != nil {
				t.Fatalf("NewInitiator: %v", err)
			}
			initMsg, err := initiator.WriteInit(buildHelloIdentityEarlyData(t, v2TestToken, v2TestDevName, tt.reported))
			if err != nil {
				t.Fatalf("WriteInit: %v", err)
			}
			frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)
			envs := waitForEnvelopes(t, rec, 1)
			_, initSend, _, err := initiator.ReadResp(decodeRespFrame(t, envs[0]))
			if err != nil {
				t.Fatalf("ReadResp: %v", err)
			}

			frames <- sealAppFrame(t, initSend, protocol.Envelope{
				ID:      7,
				Type:    protocol.TypeListConversations,
				TS:      time.Now().UTC(),
				Payload: json.RawMessage(`{}`),
			})
			select {
			case got := <-seen:
				if got != tt.want {
					t.Errorf("c.Auth().ClientVersion = %q, want %q (stored record held %q)", got, tt.want, stored)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("handler never ran")
			}
		})
	}
}
