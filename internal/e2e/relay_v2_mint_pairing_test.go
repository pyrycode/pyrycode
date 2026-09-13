//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/pair"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_MintPairing is the first end-to-end run of the pairing-mint verb
// (#2127) — a real daemon minting a real credential for a real second device, on
// the ask of a paired one. Every piece under it is unit-tested inside its own
// package: the wire shapes (#2126), the handler and its rejects, the cmd/pyry
// gate and its audit record; nothing until now has run the whole chain, and in
// particular nothing has proved that what comes back can actually pair anything.
//
// FOUR CLAIMS, ONE RUN:
//
//   - THE MINT WORKS. A privileged device's mint_pairing is answered with a
//     pairing that decodes to this host's server id and static pubkey, and A
//     SECOND PHONE DIALS WITH THE TOKEN INSIDE IT AND COMPLETES THE HANDSHAKE.
//     That last step is the whole point: a decodable string proves encoding, and
//     only a completed handshake proves the registry record behind it.
//   - IT IS CLI-EQUIVALENT. `pyry pair list` shows the minted device and `pyry
//     pair revoke` removes it, with no new verb — so the record is the CLI's
//     shape, not a parallel one.
//   - THE WRITE IS LOCK-SAFE. A `pyry pair` run is interleaved between daemon
//     start and the wire mint, and its record survives the mint.
//   - AN UNPRIVILEGED MINT IS REFUSED AND CREATES NOTHING.
//
// THE RELAY URL IS DELIBERATELY NOT COMPARED TO THE CLI'S. The daemon is started
// with -pyry-relay pointing at this test's fakerelay while the CLI resolves its
// own from config.json, so the two legitimately differ — and the reply must carry
// THE DAEMON'S, which is the relay the new device actually has to reach. Asserting
// equality with the CLI's would pin the wrong one of the two.
func TestRelayV2_MintPairing(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		// The three device labels this run creates or names.
		grantorName = "operator-laptop"   // paired with --allow-remote-permissions
		watcherName = "watcher-phone"     // paired WITHOUT it
		cliName     = "cli-during-daemon" // minted at the CLI while the daemon runs
		mintedName  = "kitchen-ipad-2127" // minted over the wire
		mintReqID   = uint64(21270)
		refusedID   = uint64(21271)
		replyWait   = 15 * time.Second
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	daemonRelayURL := fr.URL() + "/v2/server"

	// The minting device: privileged, which is the only way a device ever becomes
	// one — a shell on the host.
	grantor, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  daemonRelayURL,
		DeviceName:             grantorName,
		AllowRemotePermissions: true,
	})
	if err != nil {
		t.Fatalf("setup grantor: %v", err)
	}

	// A second paired device WITHOUT the flag, for the refusal arm. Pairing it now
	// rather than later keeps its setup write ahead of the daemon.
	watcher, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        daemonRelayURL,
		DeviceName:   watcherName,
	})
	if err != nil {
		t.Fatalf("setup watcher: %v", err)
	}

	grantorPub, err := base64.StdEncoding.DecodeString(grantor.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, daemonRelayURL)
	t.Cleanup(func() { h.Stop(t) })
	exposePairSocket(t, home, "test", h.SocketPath)

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// ── The lock claim, first half ──
	// A CLI mint landing AFTER the daemon is up and holding its own in-memory
	// registry. Before #1531 this and the wire mint below would each have mutated
	// a snapshot the other did not see; the assertion that both records survive is
	// at the end of the run, after the wire mint has had its chance to erase this.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name="+cliName)
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair (during daemon) exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}

	// ── The mint ──
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, grantor.Token, "grantor")
	if err != nil {
		t.Fatalf("grantor dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, grantorPub, grantor.Token)

	sendMintPairingE2E(t, phone, send, mintReqID, mintedName)
	minted := awaitPairingMinted(t, phone, recv, mintReqID, replyWait)

	got, err := pair.Decode(minted.Pairing)
	if err != nil {
		t.Fatalf("decode the minted pairing: %v", err)
	}
	if string(got.Server) != serverID {
		t.Errorf("minted server = %q, want this host's %q", got.Server, serverID)
	}
	if got.ServerStaticPubkey != grantor.ServerStaticPubkey {
		t.Error("minted server_static_pubkey differs from the shared setup fixture's key")
	}
	if got.Relay != daemonRelayURL {
		t.Errorf("minted relay = %q, want the daemon's own leg %q", got.Relay, daemonRelayURL)
	}
	if got.Token == grantor.Token || got.Token == watcher.Token {
		t.Error("the mint handed back an existing device's token instead of a fresh one")
	}

	// ── THE CLAIM THAT MATTERS: the pairing actually pairs ──
	// A brand-new phone, dialling with nothing but what came off the wire, must
	// complete the Noise_IK handshake and reach the open state. Anything short of
	// this — a decodable string, a row in `pair list` — is consistent with a
	// registry record the auth path cannot find.
	mintedDialCtx, mintedCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer mintedCancel()
	mintedPub, err := base64.StdEncoding.DecodeString(got.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode the minted pairing's pubkey: %v", err)
	}
	mintedPhone, err := fakephone.Dial(mintedDialCtx, fr.URL(), string(got.Server), got.Token, "minted")
	if err != nil {
		t.Fatalf("the minted device could not dial: %v", err)
	}
	t.Cleanup(func() { _ = mintedPhone.Close() })
	driveHandshakeToOpenDaemonInteractive(t, mintedPhone, mintedPub, got.Token)

	// ── The refusal ──
	watcherCtx, watcherCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer watcherCancel()
	watcherPhone, err := fakephone.Dial(watcherCtx, fr.URL(), serverID, watcher.Token, "watcher")
	if err != nil {
		t.Fatalf("watcher dial: %v", err)
	}
	t.Cleanup(func() { _ = watcherPhone.Close() })
	wSend, wRecv := driveHandshakeToOpenDaemonInteractive(t, watcherPhone, grantorPub, watcher.Token)

	sendMintPairingE2E(t, watcherPhone, wSend, refusedID, "should-never-exist-2127")
	ep := awaitMintReject(t, watcherPhone, wRecv, refusedID, replyWait)
	if ep.Code != protocol.CodePairingNotPermitted {
		t.Errorf("unprivileged mint reject code = %q, want %q", ep.Code, protocol.CodePairingNotPermitted)
	}
	if ep.Retryable {
		t.Error("pairing.not_permitted answered retryable; only a shell on the host changes a device's privilege")
	}

	// ── What the registry holds, read through the CLI ──
	list := RunBareIn(t, home, "pair", "list", "-pyry-name=test")
	if list.ExitCode != 0 {
		t.Fatalf("pyry pair list exit=%d\nstderr:\n%s", list.ExitCode, list.Stderr)
	}
	listing := string(list.Stdout)
	for _, want := range []string{grantorName, watcherName, cliName, mintedName} {
		if !strings.Contains(listing, want) {
			t.Errorf("`pyry pair list` does not show %q:\n%s", want, listing)
		}
	}
	// The lock claim, second half: the CLI's record was written between daemon
	// start and the wire mint, and the wire mint did not erase it.
	if !strings.Contains(listing, cliName) {
		t.Error("the wire mint erased a `pyry pair` record written while the daemon ran")
	}
	if strings.Contains(listing, "should-never-exist-2127") {
		t.Error("a refused mint created a record; the gate must fire before anything is created")
	}

	// No new CLI verb: the existing revoke removes a wire-minted device.
	rev := RunBareIn(t, home, "pair", "revoke", "-pyry-name=test", mintedName)
	if rev.ExitCode != 0 {
		t.Fatalf("pyry pair revoke %q exit=%d\nstderr:\n%s", mintedName, rev.ExitCode, rev.Stderr)
	}
	after := RunBareIn(t, home, "pair", "list", "-pyry-name=test")
	if strings.Contains(string(after.Stdout), mintedName) {
		t.Errorf("`pyry pair revoke` left %q in the registry:\n%s", mintedName, after.Stdout)
	}

	// The credential reached no daemon log on any arm. The daemon runs with
	// -pyry-verbose, so this reads the whole debug-level stream.
	if logs := h.Stderr.String(); strings.Contains(logs, got.Token) || strings.Contains(logs, minted.Pairing) {
		t.Error("the minted credential reached the daemon's log")
	}
}

// sendMintPairingE2E seals one mint_pairing. The envelope ID is load-bearing: the
// reply and every reject correlate on it through in_reply_to, and the payload
// carries no request-id key.
func sendMintPairingE2E(t *testing.T, phone *fakephone.Client, send *noise.CipherState, envID uint64, deviceName string) {
	t.Helper()
	sendSealedEnvelope(t, phone, send, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeMintPairing,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.MintPairingPayload{DeviceName: deviceName}),
	})
}

// awaitPairingMinted reads until the pairing_minted correlated to inReplyTo
// arrives, failing on an error frame answering the same request and reading past
// anything else — the bootstrap session's own pushes share this conn.
//
// It reuses nextAttachmentEnvelope, whose name is the retrieval leg's but whose
// job is generic: decrypt the next envelope in arrival order, which is what keeps
// the receive nonce in lockstep. Classifying AFTER the decrypt is the load-bearing
// half; skipping a frame would desynchronise every later one.
func awaitPairingMinted(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, inReplyTo uint64, within time.Duration) protocol.PairingMintedPayload {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("no pairing_minted arrived for request %d; a mint must be answered, not dropped", inReplyTo)
		}
		if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
			continue
		}
		switch env.Type {
		case protocol.TypePairingMinted:
			var p protocol.PairingMintedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode pairing_minted payload: %v", err)
			}
			return p
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("request %d refused, and its error payload did not decode: %v", inReplyTo, err)
			}
			t.Fatalf("request %d refused with code %q (retryable=%v); the asking device is privileged",
				inReplyTo, ep.Code, ep.Retryable)
		}
	}
}

// awaitMintReject is awaitPairingMinted's mirror: it fails if a PAIRING answers
// the request, so no row can pass while the daemon also minted a credential.
func awaitMintReject(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, inReplyTo uint64, within time.Duration) protocol.ErrorPayload {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("no error frame arrived for request %d; a refused mint must be answered, not dropped", inReplyTo)
		}
		if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
			continue
		}
		switch env.Type {
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			return ep
		case protocol.TypePairingMinted:
			t.Fatalf("a pairing was minted for request %d, which must be refused", inReplyTo)
		}
	}
}
