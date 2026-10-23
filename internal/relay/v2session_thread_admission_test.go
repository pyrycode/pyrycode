package relay

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func assertThreadRefused(t *testing.T, out versionOutcome, code string, closeCode uint16) {
	t.Helper()
	if out.open || len(out.envs) != 2 || out.closeCode != closeCode {
		t.Fatalf("open=%v envelopes=%d close=%d; want false/2/%d", out.open, len(out.envs), out.closeCode, closeCode)
	}
	if !out.hasErr || out.errEnv.Type != protocol.TypeError || out.errBody.Code != code || out.errBody.Retryable {
		t.Fatalf("refusal: %#v / %#v", out.errEnv, out.errBody)
	}
	if out.errEnv.InReplyTo == nil || *out.errEnv.InReplyTo != 1 || !bytes.Contains(out.ackRaw, []byte(`"in_reply_to":1`)) {
		t.Fatal("hello ack/error correlation lost")
	}
	if bytes.Contains(out.ackRaw, []byte(`"workspace_root"`)) || out.errBody.MinClientVersion != "" {
		t.Fatal("rejected admission leaked workspace/minimum metadata")
	}
}

func TestV2Session_ThreadAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		ready func() bool
		caps  []string
		code  string
		close uint16
	}{
		{"omitted", func() bool { return true }, nil, protocol.CodeClientUpdateRequired, 4412},
		{"empty", func() bool { return true }, []string{}, protocol.CodeClientUpdateRequired, 4412},
		{"unknown", func() bool { return true }, []string{"thread_fake", "private-cap-marker"}, protocol.CodeClientUpdateRequired, 4412},
		{"nil-readiness", nil, threadCaps, protocol.CodeProtocolUnsupported, 4421},
		{"false-readiness", func() bool { return false }, threadCaps, protocol.CodeProtocolUnsupported, 4421},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deadline := time.Now().UTC().Add(devices.RedemptionWindow).Truncate(time.Second)
			reg, path := unboundFixture(t, deadline)
			before := diskDevice(t, path)
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			logger, logs := bufferLogger()
			out := runHelloFromCaps(t, V2SessionConfig{Devices: reg, DevicesPath: path, ThreadReady: tc.ready, Logger: logger}, v2TestInstallPriv, v2TestToken, "private-version-marker", tc.caps)
			assertThreadRefused(t, out, tc.code, tc.close)
			diskAfter, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(diskBefore, diskAfter) {
				t.Fatal("refusal rewrote pairing file")
			}
			mem, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
			if !ok {
				t.Fatal("pairing disappeared")
			}
			for _, d := range []devices.Device{mem, diskDevice(t, path)} {
				if d.StaticKey != before.StaticKey || !d.RedeemBy.Equal(before.RedeemBy) || d.ClientVersion != before.ClientVersion {
					t.Fatalf("refusal changed pairing admission fields: %#v", d)
				}
			}
			if mem.LastSeenAt.IsZero() || mem.LastSeenAt.Before(before.LastSeenAt) {
				t.Fatal("authentication bookkeeping lost")
			}
			for _, secret := range []string{v2TestToken, devices.HashToken(v2TestToken), v2TestInstallKey(t), "private-version-marker", "private-cap-marker"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("private hello material in logs")
				}
			}
		})
	}
}

func TestV2Session_ThreadAdmissionAuthPrecedence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"unknown", "expired", "mismatched-key"} {
		for _, ready := range []bool{false, true} {
			for _, caps := range [][]string{nil, {"thread_fake"}, {protocol.CapabilityThread}} {
				t.Run(fmt.Sprintf("%s/ready=%t/caps=%v", kind, ready, caps), func(t *testing.T) {
					reg, path := unboundFixture(t, time.Time{})
					token := v2TestToken
					if kind == "unknown" {
						token = "unknown-token"
					}
					if kind == "expired" {
						reg, path = unboundFixture(t, time.Now().Add(-time.Hour))
					}
					if kind == "mismatched-key" {
						other, _ := installKey(t, 99)
						reg.BindStaticKey(devices.HashToken(token), other)
						if err := reg.Save(path); err != nil {
							t.Fatal(err)
						}
					}
					out := runHelloFromCaps(t, V2SessionConfig{Devices: reg, DevicesPath: path, ThreadReady: func() bool { return ready }}, v2TestInstallPriv, token, "private-version-marker", caps)
					assertThreadRefused(t, out, protocol.CodeAuthInvalidToken, 4401)
					if out.errBody.Message != MsgInvalidToken {
						t.Fatal("authentication error became distinguishable")
					}
				})
			}
		}
	}
}

func TestV2Session_ThreadAdmissionVersionPrecedence(t *testing.T) {
	t.Parallel()
	for _, caps := range [][]string{nil, {protocol.CapabilityThread}} {
		out := runHelloFromCaps(t, V2SessionConfig{MinClientVersions: map[string]string{protocol.AppMobile: "1.4.0"}}, v2TestInstallPriv, v2TestToken, "pyrycode-mobile/1.0.0", caps)
		assertVersionRejected(t, out, "1.4.0")
	}
}
