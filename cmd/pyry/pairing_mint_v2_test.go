package main

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/pair"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// --- #2127 wire-mint fixtures ---

const (
	pmTestRelayURL  = "wss://relay.invalid/v2/phone"
	pmTestGrantor   = "operator-laptop"
	pmTestLabel     = "Kitchen iPad"
	pmTestPlainTok  = "2127-grantor-plaintext-token"
	pmTestPubkeyRaw = "0123456789abcdef0123456789abcdef" // 32 bytes, base64'd by the minter
)

// pmTestServerID is minted rather than spelled as a literal: pair.Decode
// validates the id's ULID shape, so a hand-typed constant is one Crockford-alphabet
// slip away from failing every decode assertion here for a reason unrelated to the
// mint. One per process is enough — the minter treats it as opaque.
var pmTestServerID = identity.NewServerID()

// newMintFixture stands up a minter over a fresh registry containing one device,
// and returns the minter, that device, the devices.json path and the log sink.
//
// The registry is written through Save rather than hand-built in memory, because
// mintDevice re-reads it off disk inside its lock — the grantor check and the
// append both act on what is actually stored, and a purely in-memory fixture
// could not exercise either.
func newMintFixture(t *testing.T, privileged bool) (*pairingMinterV2, *devices.Device, string, *bytes.Buffer) {
	t.Helper()

	dir := t.TempDir()
	devicesPath := filepath.Join(dir, "devices.json")
	reg := &devices.Registry{}
	grantor := devices.Device{
		TokenHash:              devices.HashToken(pmTestPlainTok),
		Name:                   pmTestGrantor,
		PairedAt:               time.Now().UTC(),
		AllowRemotePermissions: privileged,
	}
	reg.Add(grantor)
	if err := reg.Save(devicesPath); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var pub [32]byte
	copy(pub[:], pmTestPubkeyRaw)

	return newPairingMinterV2(devicesPath, pmTestRelayURL, pmTestServerID, pub, logger), &grantor, devicesPath, &buf
}

// loadRegistry re-reads devices.json so an assertion is made against what is on
// disk rather than against any in-memory copy.
func loadRegistry(t *testing.T, path string) *devices.Registry {
	t.Helper()
	reg, err := devices.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

// deviceNamed returns the stored record with the given name, or nil.
func deviceNamed(reg *devices.Registry, name string) *devices.Device {
	for _, d := range reg.List() {
		if d.Name == name {
			got := d
			return &got
		}
	}
	return nil
}

// TestPairingMinterV2_PermittedMint is the happy path, and it pins the whole of
// AC-1: the reply decodes to this host's identity, and the record behind it is
// indistinguishable from one `pyry pair` wrote.
func TestPairingMinterV2_PermittedMint(t *testing.T) {
	t.Parallel()

	m, grantor, devicesPath, logBuf := newMintFixture(t, true)

	res := m.MintPairing(grantor, pmTestLabel)
	if res.Outcome != relay.PairingMintOK {
		t.Fatalf("outcome = %v, want PairingMintOK", res.Outcome)
	}

	got, err := pair.Decode(res.Pairing)
	if err != nil {
		t.Fatalf("decode the minted pairing: %v", err)
	}
	if got.Server != pmTestServerID {
		t.Errorf("server = %q, want this host's %q", got.Server, pmTestServerID)
	}
	if got.Relay != pmTestRelayURL {
		t.Errorf("relay = %q, want the daemon's own resolved URL %q", got.Relay, pmTestRelayURL)
	}
	wantPub := base64.StdEncoding.EncodeToString([]byte(pmTestPubkeyRaw))
	if got.ServerStaticPubkey != wantPub {
		t.Errorf("server_static_pubkey = %q, want %q", got.ServerStaticPubkey, wantPub)
	}

	// The record behind the token. It must be reachable by the SAME hash the
	// handshake path computes, or the pairing decodes cleanly and authenticates
	// nowhere.
	reg := loadRegistry(t, devicesPath)
	minted := deviceNamed(reg, pmTestLabel)
	if minted == nil {
		t.Fatalf("no record named %q; registry holds %d devices", pmTestLabel, len(reg.List()))
	}
	if minted.TokenHash != devices.HashToken(got.Token) {
		t.Error("the stored hash is not the hash of the token the reply carries")
	}
	if minted.AllowRemotePermissions {
		t.Error("a wire-minted device carries the remote-permissions flag; it must always be unprivileged")
	}
	if d := minted.RedeemBy.Sub(minted.PairedAt); d != devices.RedemptionWindow {
		t.Errorf("RedeemBy - PairedAt = %v, want exactly %v — both stamps come from one clock read",
			d, devices.RedemptionWindow)
	}

	// The grantor is untouched: minting must append, never rewrite.
	if g := deviceNamed(reg, pmTestGrantor); g == nil || !g.AllowRemotePermissions {
		t.Error("the minting device's own record was lost or downgraded by the mint")
	}

	assertMintLogIsClean(t, logBuf, got.Token, minted.TokenHash, res.Pairing)
	logs := logBuf.String()
	if !strings.Contains(logs, pmTestGrantor) || !strings.Contains(logs, pmTestLabel) {
		t.Errorf("the mint record names %q and %q nowhere; AC-4 asks for both", pmTestGrantor, pmTestLabel)
	}
	if n := strings.Count(logs, "pairing.mint.ok"); n != 1 {
		t.Errorf("pairing.mint.ok appears %d times, want exactly one event per successful mint", n)
	}
}

// TestPairingMinterV2_UnnamedDeviceGetsTheCLIFallback pins that the two entry
// points name an unnamed device identically. The fallback is derived from the
// minted token's own hash, so it is checkable rather than merely shaped.
func TestPairingMinterV2_UnnamedDeviceGetsTheCLIFallback(t *testing.T) {
	t.Parallel()

	m, grantor, devicesPath, _ := newMintFixture(t, true)

	res := m.MintPairing(grantor, "")
	if res.Outcome != relay.PairingMintOK {
		t.Fatalf("outcome = %v, want PairingMintOK — an unnamed device is not a refusal", res.Outcome)
	}
	got, err := pair.Decode(res.Pairing)
	if err != nil {
		t.Fatalf("decode the minted pairing: %v", err)
	}

	want := "device-" + devices.HashToken(got.Token)[:8]
	if d := deviceNamed(loadRegistry(t, devicesPath), want); d == nil {
		t.Errorf("no record named %q; the unnamed fallback must match `pyry pair`'s", want)
	}
}

// TestPairingMinterV2_UnprivilegedIsRefusedBeforeAnythingIsCreated is AC-2: the
// gate fires first, nothing is created, and the refusal is audited.
func TestPairingMinterV2_UnprivilegedIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	t.Parallel()

	m, requester, devicesPath, logBuf := newMintFixture(t, false)
	before, err := os.ReadFile(devicesPath)
	if err != nil {
		t.Fatalf("read seeded registry: %v", err)
	}

	res := m.MintPairing(requester, pmTestLabel)
	if res.Outcome != relay.PairingMintUnauthorized {
		t.Fatalf("outcome = %v, want PairingMintUnauthorized", res.Outcome)
	}
	if res.Pairing != "" {
		t.Error("a refusal carried a credential; Pairing must be empty on every non-OK outcome")
	}

	// BYTE-IDENTICAL, not merely "no new named record": the claim is that the
	// refusal creates nothing at all, which a count comparison would not catch if
	// the file were rewritten with the same contents.
	after, err := os.ReadFile(devicesPath)
	if err != nil {
		t.Fatalf("re-read registry: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the registry changed on a refused mint; nothing may be created before the gate passes")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "denied_unauthorized") {
		t.Error("the refusal was not audited denied_unauthorized")
	}
	if !strings.Contains(logs, classPairingMint) {
		t.Errorf("the audit record does not carry the %q class; it is unidentifiable among modal records", classPairingMint)
	}
	if !strings.Contains(logs, pmTestGrantor) {
		t.Error("the audit record does not name the refused device")
	}
}

// TestPairingMinterV2_NilRequesterDenies pins the fail-closed reading of a conn
// with no authenticated device. It cannot happen on an open v2 session — the
// handshake binds one — which is exactly why it must be asserted rather than
// assumed: the day it can, the answer has to already be "deny".
func TestPairingMinterV2_NilRequesterDenies(t *testing.T) {
	t.Parallel()

	m, _, devicesPath, logBuf := newMintFixture(t, true)

	res := m.MintPairing(nil, pmTestLabel)
	if res.Outcome != relay.PairingMintUnauthorized {
		t.Fatalf("outcome = %v, want PairingMintUnauthorized", res.Outcome)
	}
	if d := deviceNamed(loadRegistry(t, devicesPath), pmTestLabel); d != nil {
		t.Error("a device-less conn minted a record")
	}
	if !strings.Contains(logBuf.String(), "denied_unauthorized") {
		t.Error("the nil-device refusal was not audited")
	}
}

// TestPairingMinterV2_RevokedGrantorIsRefusedAtWriteTime is the security-review
// finding this ticket folded into the design: v2 revocation is connection-scoped,
// so the *devices.Device a live session holds is the record as of connect time.
// Both shapes of revocation are walked, because they are two different registry
// states and only one of them is "the row is gone".
func TestPairingMinterV2_RevokedGrantorIsRefusedAtWriteTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		revoke func(t *testing.T, path string)
	}{
		{
			name: "the device was removed from the registry",
			revoke: func(t *testing.T, path string) {
				t.Helper()
				reg := loadRegistry(t, path)
				if !reg.Remove(pmTestGrantor) {
					t.Fatalf("Remove(%q) found nothing to remove", pmTestGrantor)
				}
				if err := reg.Save(path); err != nil {
					t.Fatalf("save after remove: %v", err)
				}
			},
		},
		{
			name: "the device is still paired but no longer privileged",
			revoke: func(t *testing.T, path string) {
				t.Helper()
				reg := loadRegistry(t, path)
				stored := deviceNamed(reg, pmTestGrantor)
				if stored == nil {
					t.Fatalf("no record named %q to downgrade", pmTestGrantor)
				}
				if !reg.Remove(pmTestGrantor) {
					t.Fatal("could not replace the grantor's record")
				}
				stored.AllowRemotePermissions = false
				reg.Add(*stored)
				if err := reg.Save(path); err != nil {
					t.Fatalf("save after downgrade: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The in-memory device stays privileged throughout: it models the
			// stale handshake-time snapshot a live session holds, which is the
			// whole subject of this test.
			m, grantor, devicesPath, logBuf := newMintFixture(t, true)
			tc.revoke(t, devicesPath)

			res := m.MintPairing(grantor, pmTestLabel)
			if res.Outcome != relay.PairingMintUnauthorized {
				t.Fatalf("outcome = %v, want PairingMintUnauthorized — a revoked device must not mint", res.Outcome)
			}
			if res.Pairing != "" {
				t.Error("a revoked grantor was handed a credential")
			}
			if d := deviceNamed(loadRegistry(t, devicesPath), pmTestLabel); d != nil {
				t.Error("a revoked grantor's mint reached disk")
			}
			if !strings.Contains(logBuf.String(), "denied_unauthorized") {
				t.Error("the revoked-grantor refusal was not audited as a privilege denial")
			}
		})
	}
}

// TestPairingMinterV2_HostFailureIsRetryableAndLeaksNothing drives the merged
// host-failure arm by pointing the minter at a registry path that cannot be read,
// and pins that the answer is the retryable outcome carrying no credential.
func TestPairingMinterV2_HostFailureIsRetryableAndLeaksNothing(t *testing.T) {
	t.Parallel()

	m, grantor, devicesPath, logBuf := newMintFixture(t, true)
	// A directory where the registry file belongs: devices.Load opens it and
	// fails, inside the lock, after the grantor check would have passed.
	if err := os.Remove(devicesPath); err != nil {
		t.Fatalf("clear the seeded registry: %v", err)
	}
	if err := os.Mkdir(devicesPath, 0o700); err != nil {
		t.Fatalf("replace the registry with a directory: %v", err)
	}

	res := m.MintPairing(grantor, pmTestLabel)
	if res.Outcome != relay.PairingMintFailed {
		t.Fatalf("outcome = %v, want PairingMintFailed", res.Outcome)
	}
	if res.Pairing != "" {
		t.Error("a host failure carried a credential")
	}
	if !strings.Contains(logBuf.String(), "pairing.mint.failed") {
		t.Error("the host failure was not recorded for the operator")
	}
	if strings.Contains(logBuf.String(), "pairing.mint.ok") {
		t.Error("a failed mint logged a success record")
	}
}

// assertMintLogIsClean checks the never-log rule on every value a mint produces
// that must not reach a record: the plaintext token, its hash, and the encoded
// pairing — whole, and in the prefix a truncating edit would leave behind.
func assertMintLogIsClean(t *testing.T, logBuf *bytes.Buffer, token, hash, pairing string) {
	t.Helper()
	logs := logBuf.String()
	for label, secret := range map[string]string{
		"the plaintext token":  token,
		"the stored hash":      hash,
		"the encoded pairing":  pairing,
		"a pairing string cut": pairing[:16],
		"a token prefix":       token[:16],
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("%s reached the log", label)
		}
	}
}

// TestMintDevice_CLICallerIsNotGrantorChecked pins the other half of the grantor
// check: it is opt-in, and `pyry pair` opts out.
//
// It lives beside the wire tests rather than in pair_test.go because the property
// it protects is this ticket's — an operator at a shell on the host is the
// authority the check defers to, not a subject of it — and the regression it
// guards against is a later edit making grantorHash unconditional, which would
// break `pyry pair` on an empty registry.
func TestMintDevice_CLICallerIsNotGrantorChecked(t *testing.T) {
	t.Parallel()

	devicesPath := filepath.Join(t.TempDir(), "devices.json")

	// No registry at all: nothing could satisfy a grantor check, so a mint
	// succeeding here proves none was applied.
	minted, err := mintDevice(mintRequest{
		devicesPath:            devicesPath,
		lockWait:               wireMintLockWait,
		deviceName:             pmTestGrantor,
		allowRemotePermissions: true,
	})
	if err != nil {
		t.Fatalf("mintDevice with no grantorHash: %v", err)
	}

	stored := deviceNamed(loadRegistry(t, devicesPath), pmTestGrantor)
	if stored == nil {
		t.Fatalf("no record named %q", pmTestGrantor)
	}
	if stored.TokenHash != devices.HashToken(minted.token) {
		t.Error("the stored hash is not the hash of the returned token")
	}
	if !stored.AllowRemotePermissions {
		t.Error("the CLI's --allow-remote-permissions did not reach the record")
	}
}
