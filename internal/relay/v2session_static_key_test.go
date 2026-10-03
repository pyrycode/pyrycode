package relay

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// installKey returns a distinct install's static private key and the hex of its
// public key, as Device.StaticKey stores it.
func installKey(t *testing.T, b byte) (priv []byte, pubHex string) {
	t.Helper()
	priv = bytes.Repeat([]byte{b}, 32)
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatalf("install key: %v", err)
	}
	return priv, hex.EncodeToString(k.PublicKey().Bytes())
}

// unboundFixture seeds devices.json with one unbound record for v2TestToken.
func unboundFixture(t *testing.T, redeemBy time.Time) (*devices.Registry, string) {
	t.Helper()
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(v2TestToken),
		Name:      v2TestDevName,
		PairedAt:  time.Now().UTC().Add(-time.Minute),
		RedeemBy:  redeemBy,
	})
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed devices.json: %v", err)
	}
	return reg, path
}

// TestV2Session_StaticKey_FirstConnectionBindsAndPersists is AC-1: the first
// accepted connection binds the record to its key on disk, for a fresh
// redemption and for a record redeemed before the field existed alike.
func TestV2Session_StaticKey_FirstConnectionBindsAndPersists(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		redeemBy time.Time
	}{
		{"fresh redemption", time.Now().UTC().Add(devices.RedemptionWindow)},
		{"legacy redeemed record", time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, path := unboundFixture(t, tc.redeemBy)
			priv, pubHex := installKey(t, 1)
			out := runHelloFrom(t, V2SessionConfig{Devices: reg, DevicesPath: path}, priv, v2TestToken, "v2-test")
			assertVersionAccepted(t, out)
			if got := diskDevice(t, path).StaticKey; got != pubHex {
				t.Errorf("on-disk StaticKey = %q, want %q", got, pubHex)
			}
		})
	}
}

// TestV2Session_StaticKey_OtherInstallRefusedLikeUnknownToken is AC-2 and AC-3:
// once bound, the same install reconnects, while another install presenting the
// same valid token gets byte-for-byte the reject an unknown token gets, and the
// daemon logs one Warn naming the device and the reason, without the token, its
// hash or either key.
func TestV2Session_StaticKey_OtherInstallRefusedLikeUnknownToken(t *testing.T) {
	t.Parallel()
	reg, path := unboundFixture(t, time.Time{})
	privA, hexA := installKey(t, 1)
	privB, hexB := installKey(t, 2)
	cfg := V2SessionConfig{Devices: reg, DevicesPath: path}

	assertVersionAccepted(t, runHelloFrom(t, cfg, privA, v2TestToken, "v2-test"))
	assertVersionAccepted(t, runHelloFrom(t, cfg, privA, v2TestToken, "v2-test"))

	logger, buf := bufferLogger()
	cfg.Logger = logger
	other := runHelloFrom(t, cfg, privB, v2TestToken, "v2-test")
	unknown := runHelloFrom(t, V2SessionConfig{}, privB, "wrong-token-xxxx", "v2-test")

	if other.open {
		t.Fatal("another install's connection opened a session")
	}
	if len(other.envs) != 2 || other.closeCode != uint16(StatusUnauthorized) {
		t.Fatalf("envelopes = %d, close_code = %d; want 2 and %d", len(other.envs), other.closeCode, StatusUnauthorized)
	}
	if !reflect.DeepEqual(other.ack, unknown.ack) {
		t.Errorf("ack = %+v, want the unknown-token ack %+v", other.ack, unknown.ack)
	}
	if other.errRaw != unknown.errRaw || other.errEnv.Type != unknown.errEnv.Type ||
		!reflect.DeepEqual(other.errEnv.InReplyTo, unknown.errEnv.InReplyTo) {
		t.Errorf("error frame = %s %s, want the unknown-token frame %s %s",
			other.errEnv.Type, other.errRaw, unknown.errEnv.Type, unknown.errRaw)
	}
	if other.errBody.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("code = %q, want %q", other.errBody.Code, protocol.CodeAuthInvalidToken)
	}

	waitForLogContains(t, buf, "v2.handshake.reject.static_key_mismatch")
	logs := buf.String()
	if n := strings.Count(logs, "level=WARN"); n != 1 {
		t.Errorf("WARN lines = %d, want 1:\n%s", n, logs)
	}
	for _, want := range []string{"device_name=" + v2TestDevName, "reason=bound_to_other_key", "close_code=4401"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	for _, secret := range []string{v2TestToken, devices.HashToken(v2TestToken), hexA, hexB} {
		if strings.Contains(logs, secret) {
			t.Errorf("log carries %q:\n%s", secret, logs)
		}
	}
	if got := diskDevice(t, path).StaticKey; got != hexA {
		t.Errorf("on-disk StaticKey = %q, want the first install's %q", got, hexA)
	}
}

// TestV2Session_StaticKey_VersionRefusedBindsNothing: a connection refused at
// 4412 was never accepted, so it leaves the record unbound for the install
// that is.
func TestV2Session_StaticKey_VersionRefusedBindsNothing(t *testing.T) {
	t.Parallel()
	reg, path := unboundFixture(t, time.Time{})
	privA, _ := installKey(t, 1)
	privB, hexB := installKey(t, 2)
	cfg := V2SessionConfig{
		Devices:           reg,
		DevicesPath:       path,
		MinClientVersions: map[string]string{protocol.AppMobile: "1.4.0"},
	}

	assertVersionRejected(t, runHelloFrom(t, cfg, privA, v2TestToken, "pyrycode-mobile/1.0.0"), "1.4.0")
	if d, _ := reg.FindByTokenHash(devices.HashToken(v2TestToken)); d.StaticKey != "" {
		t.Fatalf("version-refused connection bound the record to %q", d.StaticKey)
	}
	assertVersionAccepted(t, runHelloFrom(t, cfg, privB, v2TestToken, "pyrycode-mobile/1.4.0"))
	if got := diskDevice(t, path).StaticKey; got != hexB {
		t.Errorf("on-disk StaticKey = %q, want the accepted install's %q", got, hexB)
	}
}

// TestV2Session_StaticKey_RaceAcceptsAtMostOne is AC-1's race clause: installs
// racing on one unbound record through separate sessions open exactly one.
func TestV2Session_StaticKey_RaceAcceptsAtMostOne(t *testing.T) {
	t.Parallel()
	reg, path := unboundFixture(t, time.Time{})
	var opened atomic.Int32
	t.Run("racers", func(t *testing.T) {
		for i := range 6 {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				priv, _ := installKey(t, byte(i+1))
				out := runHelloFrom(t, V2SessionConfig{Devices: reg, DevicesPath: path}, priv, v2TestToken, "v2-test")
				if out.open {
					opened.Add(1)
				} else if out.closeCode != uint16(StatusUnauthorized) {
					t.Errorf("loser close_code = %d, want %d", out.closeCode, StatusUnauthorized)
				}
			})
		}
	})
	if n := opened.Load(); n != 1 {
		t.Fatalf("opened sessions = %d, want exactly 1", n)
	}
}

// TestV2Session_StaticKey_RevokeAndRepairFreesTheDevice is AC-4: revoke drops
// the record and its binding, and the re-paired record binds a different
// install.
func TestV2Session_StaticKey_RevokeAndRepairFreesTheDevice(t *testing.T) {
	t.Parallel()
	reg, path := unboundFixture(t, time.Time{})
	privA, _ := installKey(t, 1)
	privB, hexB := installKey(t, 2)
	cfg := V2SessionConfig{Devices: reg, DevicesPath: path}
	assertVersionAccepted(t, runHelloFrom(t, cfg, privA, v2TestToken, "v2-test"))

	// What `pyry pair revoke` then `pyry pair` do to the file.
	disk, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !disk.Remove(v2TestDevName) {
		t.Fatal("revoke: no such device")
	}
	const repaired = "v2-token-repaired-cafef00d"
	disk.Add(devices.Device{TokenHash: devices.HashToken(repaired), Name: v2TestDevName, PairedAt: time.Now().UTC()})
	if err := disk.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	assertVersionAccepted(t, runHelloFrom(t, cfg, privB, repaired, "v2-test"))
	after, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := after.List(); len(got) != 1 || got[0].TokenHash != devices.HashToken(repaired) || got[0].StaticKey != hexB {
		t.Errorf("on-disk records = %+v, want only the re-paired record, bound to %q", got, hexB)
	}
}
