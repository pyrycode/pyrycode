package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/debugbundle"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/pair"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
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

func deviceWithTokenHash(reg *devices.Registry, hash string) *devices.Device {
	for _, d := range reg.List() {
		if d.TokenHash == hash {
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

type localPairingTestResolver struct{}

func (localPairingTestResolver) Lookup(_ sessions.SessionID) (control.Session, error) {
	return nil, errors.New("not used")
}

func (localPairingTestResolver) ResolveID(_ string) (sessions.SessionID, error) {
	return "", errors.New("not used")
}

func startLocalPairingControlServer(
	t *testing.T,
	provider func(deviceLabel string, allowRemotePermissions bool) (string, error),
) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyry-local-pairing-")
	if err != nil {
		t.Fatalf("create short control tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socketPath := filepath.Join(dir, "p.sock")
	srv := control.NewServer(socketPath, localPairingTestResolver{}, nil, nil, nil, nil)
	if provider != nil {
		srv.SetPairingProvider(provider)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("listen on control socket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("control Serve returned: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("control Serve did not return after cancel")
		}
	})
	return socketPath
}

func assertDiagnosticBundleClean(t *testing.T, logs string, secrets ...string) {
	t.Helper()
	archive, _, err := debugbundle.Assemble(t.TempDir(), []string{logs})
	if err != nil {
		t.Fatalf("assemble diagnostic bundle: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("open diagnostic bundle gzip: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read diagnostic bundle member: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read diagnostic bundle member body: %v", err)
		}
		for _, secret := range secrets {
			if secret != "" && bytes.Contains(body, []byte(secret)) {
				t.Error("diagnostic bundle contains protected credential material")
			}
		}
	}
}

func TestLocalPairingProvider_BindsEachControlSocketToItsDaemonState(t *testing.T) {
	t.Parallel()

	type daemonFixture struct {
		serverID              identity.ServerID
		relayURL              string
		pub                   [32]byte
		devicesPath           string
		label                 string
		allowRemotePermission bool
		logs                  bytes.Buffer
		pairing               string
	}

	dir := t.TempDir()
	fixtures := []*daemonFixture{
		{
			serverID:              identity.NewServerID(),
			relayURL:              "wss://relay-one.invalid/live",
			pub:                   [32]byte{1, 2, 3, 4},
			devicesPath:           filepath.Join(dir, "one", "devices.json"),
			label:                 "desk-one",
			allowRemotePermission: false,
		},
		{
			serverID:              identity.NewServerID(),
			relayURL:              "wss://relay-two.invalid/live",
			pub:                   [32]byte{9, 8, 7, 6},
			devicesPath:           filepath.Join(dir, "two", "devices.json"),
			label:                 "",
			allowRemotePermission: true,
		},
	}

	for _, fixture := range fixtures {
		logger := slog.New(slog.NewTextHandler(&fixture.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		minter := newPairingMinterV2(
			fixture.devicesPath,
			fixture.relayURL,
			fixture.serverID,
			fixture.pub,
			logger,
		)
		socketPath := startLocalPairingControlServer(t, minter.MintLocalPairing)
		pairing, err := control.MintPairing(
			context.Background(),
			socketPath,
			fixture.label,
			fixture.allowRemotePermission,
		)
		if err != nil {
			t.Fatalf("mint pairing through %s: %v", fixture.relayURL, err)
		}
		fixture.pairing = pairing
	}

	for i, fixture := range fixtures {
		payload, err := pair.Decode(fixture.pairing)
		if err != nil {
			t.Fatalf("decode daemon %d pairing: %v", i, err)
		}
		if payload.Server != fixture.serverID {
			t.Errorf("daemon %d server = %q, want %q", i, payload.Server, fixture.serverID)
		}
		if payload.Relay != fixture.relayURL {
			t.Errorf("daemon %d relay = %q, want %q", i, payload.Relay, fixture.relayURL)
		}
		wantPub := base64.StdEncoding.EncodeToString(fixture.pub[:])
		if payload.ServerStaticPubkey != wantPub {
			t.Errorf("daemon %d static key = %q, want %q", i, payload.ServerStaticPubkey, wantPub)
		}

		wantHash := devices.HashToken(payload.Token)
		wantLabel := fixture.label
		if wantLabel == "" {
			wantLabel = "device-" + wantHash[:8]
		}
		for registryIndex, registryFixture := range fixtures {
			registry := loadRegistry(t, registryFixture.devicesPath)
			if registryIndex == i {
				stored := deviceNamed(registry, wantLabel)
				if stored == nil {
					t.Errorf("daemon %d registry has no device named %q", i, wantLabel)
					continue
				}
				if stored.TokenHash != wantHash {
					t.Errorf("daemon %d stored token hash does not match its returned token", i)
				}
				if stored.AllowRemotePermissions != fixture.allowRemotePermission {
					t.Errorf("daemon %d permission = %v, want %v", i, stored.AllowRemotePermissions, fixture.allowRemotePermission)
				}
				if d := stored.RedeemBy.Sub(stored.PairedAt); d != devices.RedemptionWindow {
					t.Errorf("daemon %d redemption window = %v, want %v", i, d, devices.RedemptionWindow)
				}
				continue
			}
			if deviceWithTokenHash(registry, wantHash) != nil {
				t.Errorf("daemon %d token hash was written to daemon %d registry", i, registryIndex)
			}
		}

		assertMintLogIsClean(t, &fixture.logs, payload.Token, wantHash, fixture.pairing)
		if strings.Contains(fixture.logs.String(), wantLabel) {
			t.Errorf("daemon %d local mint log contains the device label", i)
		}
		if n := strings.Count(fixture.logs.String(), "pairing.local_mint.ok"); n != 1 {
			t.Errorf("daemon %d local success event count = %d, want 1", i, n)
		}
		assertDiagnosticBundleClean(t, fixture.logs.String(), payload.Token, wantHash, fixture.pairing, wantLabel)
	}
}

func TestStartRelay_DisabledLeavesLocalPairingProviderUnconfigured(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cleanup, _, _, _, provider, err := startRelay(context.Background(), logger, relayWiring{})
	if err != nil {
		t.Fatalf("startRelay with no URL: %v", err)
	}
	defer cleanup()
	if provider != nil {
		t.Fatal("relay-disabled daemon returned a local pairing provider")
	}

	socketPath := startLocalPairingControlServer(t, provider)
	got, err := control.MintPairing(context.Background(), socketPath, "offline-device", true)
	if err == nil || err.Error() != "pairing.mint: provider not configured" {
		t.Fatalf("MintPairing error = %v, want fixed not-configured error", err)
	}
	if got != "" {
		t.Errorf("pairing = %q, want empty with relay disabled", got)
	}
}

func assertLocalPairingControlFailure(
	t *testing.T,
	minter *pairingMinterV2,
	devicesPath string,
	logs *bytes.Buffer,
	secrets ...string,
) {
	t.Helper()
	const label = "local-failure-device-sentinel"
	socketPath := startLocalPairingControlServer(t, minter.MintLocalPairing)
	got, err := control.MintPairing(context.Background(), socketPath, label, true)
	if err == nil || err.Error() != "pairing.mint: operation failed" {
		t.Fatalf("MintPairing error = %v, want fixed operation error", err)
	}
	if got != "" {
		t.Errorf("pairing = %q, want empty on provider failure", got)
	}
	if strings.Contains(err.Error(), devicesPath) {
		t.Errorf("client error exposes registry path %q", devicesPath)
	}
	if strings.Contains(logs.String(), label) {
		t.Error("local failure log contains the supplied device label")
	}
	if !strings.Contains(logs.String(), "pairing.local_mint.failed") {
		t.Error("local failure produced no credential-free daemon diagnostic")
	}
	hash := devices.HashToken(pmTestPlainTok)
	secrets = append(secrets, pmTestPlainTok, hash, label)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(logs.String(), secret) {
			t.Error("local failure log contains protected credential material")
		}
	}
	assertDiagnosticBundleClean(t, logs.String(), secrets...)
}

func TestLocalPairingProvider_FailuresReturnNoCredential(t *testing.T) {
	t.Run("lock timeout", func(t *testing.T) {
		m, _, devicesPath, logs := newMintFixture(t, true)
		assertUnchanged := freezeRegistry(t, devicesPath)
		release := holdPairLock(t, devicesPath)

		assertLocalPairingControlFailure(t, m, devicesPath, logs)
		release()
		assertUnchanged()
	})

	t.Run("registry load", func(t *testing.T) {
		m, _, devicesPath, logs := newMintFixture(t, true)
		const malformedCredential = "registry-load-token-hash-sentinel"
		malformed := []byte(`{"devices":[{"token_hash":"` + malformedCredential + `"}`)
		if err := os.WriteFile(devicesPath, malformed, 0o600); err != nil {
			t.Fatalf("write malformed registry: %v", err)
		}

		assertLocalPairingControlFailure(t, m, devicesPath, logs, malformedCredential)
		got, err := os.ReadFile(devicesPath)
		if err != nil {
			t.Fatalf("read malformed registry after failed mint: %v", err)
		}
		if !bytes.Equal(got, malformed) {
			t.Error("failed registry load changed the malformed registry")
		}
	})

	t.Run("registry save", func(t *testing.T) {
		m, _, devicesPath, logs := newMintFixture(t, true)
		assertUnchanged := freezeRegistry(t, devicesPath)
		if err := os.WriteFile(devicesPath+".lock", nil, 0o600); err != nil {
			t.Fatalf("seed lock sidecar: %v", err)
		}
		dir := filepath.Dir(devicesPath)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("make registry directory read-only: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		assertLocalPairingControlFailure(t, m, devicesPath, logs)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("restore registry directory mode: %v", err)
		}
		assertUnchanged()
	})
}
