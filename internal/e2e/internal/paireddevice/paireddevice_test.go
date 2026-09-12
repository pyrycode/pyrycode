package paireddevice

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/keys"
	"github.com/pyrycode/pyrycode/internal/pair"
)

func TestSetup_PersistsPairedDevice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		deviceName             string
		allowRemotePermissions bool
	}{
		{
			name:       "explicit label without remote permissions",
			deviceName: "test phone",
		},
		{
			name:                   "generated label with remote permissions",
			allowRemotePermissions: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cfg := Config{
				Home:                   home,
				InstanceName:           "fixture",
				Relay:                  "wss://relay.example.test",
				DeviceName:             tt.deviceName,
				AllowRemotePermissions: tt.allowRemotePermissions,
			}

			started := time.Now().UTC()
			payload, err := Setup(cfg)
			finished := time.Now().UTC()
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}

			instanceDir := filepath.Join(home, ".pyry", cfg.InstanceName)
			storedID, err := identity.LoadOrCreate(filepath.Join(instanceDir, "server-id"))
			if err != nil {
				t.Fatalf("load identity: %v", err)
			}
			if payload.Server != storedID {
				t.Errorf("payload server = %q, want %q", payload.Server, storedID)
			}
			if payload.Relay != cfg.Relay {
				t.Errorf("payload relay = %q, want %q", payload.Relay, cfg.Relay)
			}

			staticKey, err := keys.LoadOrCreate(filepath.Join(home, ".pyry"), cfg.InstanceName)
			if err != nil {
				t.Fatalf("load static key: %v", err)
			}
			pub := staticKey.PublicKey()
			wantPub := base64.StdEncoding.EncodeToString(pub[:])
			if payload.ServerStaticPubkey != wantPub {
				t.Errorf("payload public key = %q, want %q", payload.ServerStaticPubkey, wantPub)
			}

			registry, err := devices.Load(filepath.Join(instanceDir, "devices.json"))
			if err != nil {
				t.Fatalf("load registry: %v", err)
			}
			stored := registry.List()
			if len(stored) != 1 {
				t.Fatalf("stored devices = %d, want 1", len(stored))
			}
			device := stored[0]
			wantName := tt.deviceName
			if wantName == "" {
				wantName = "device-" + devices.HashToken(payload.Token)[:8]
			}
			if device.Name != wantName {
				t.Errorf("device name = %q, want %q", device.Name, wantName)
			}
			if !devices.VerifyToken(payload.Token, device.TokenHash) {
				t.Error("payload token does not verify against stored hash")
			}
			if device.AllowRemotePermissions != tt.allowRemotePermissions {
				t.Errorf("allow remote permissions = %v, want %v", device.AllowRemotePermissions, tt.allowRemotePermissions)
			}
			if device.PairedAt.Before(started) || device.PairedAt.After(finished) {
				t.Errorf("paired at = %v, want between %v and %v", device.PairedAt, started, finished)
			}
			if got := device.RedeemBy.Sub(device.PairedAt); got != devices.RedemptionWindow {
				t.Errorf("redemption window = %v, want %v", got, devices.RedemptionWindow)
			}
			if !device.LastSeenAt.IsZero() {
				t.Errorf("last seen at = %v, want zero", device.LastSeenAt)
			}
		})
	}
}

func TestSetup_RejectsInvalidConfigBeforeWriting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "relative home",
			mutate: func(cfg *Config) {
				cfg.Home = "relative-home"
			},
		},
		{
			name: "empty relay",
			mutate: func(cfg *Config) {
				cfg.Relay = ""
			},
		},
		{
			name: "invalid instance",
			mutate: func(cfg *Config) {
				cfg.InstanceName = "../escape"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cfg := Config{
				Home:         home,
				InstanceName: "fixture",
				Relay:        "wss://relay.example.test",
			}
			tt.mutate(&cfg)

			payload, err := Setup(cfg)
			if err == nil {
				t.Fatal("Setup: nil error, want rejection")
			}
			if payload != (pair.Payload{}) {
				t.Errorf("payload = %+v, want zero value", payload)
			}
			entries, readErr := os.ReadDir(home)
			if readErr != nil {
				t.Fatalf("read isolated home: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("isolated home contains %d entries after rejection, want 0", len(entries))
			}
		})
	}
}

func TestSetup_RepeatPreservesExistingDevice(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	first, err := Setup(Config{
		Home:         home,
		InstanceName: "fixture",
		Relay:        "wss://relay-one.example.test",
		DeviceName:   "first phone",
	})
	if err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	registryPath := filepath.Join(home, ".pyry", "fixture", "devices.json")
	registry, err := devices.Load(registryPath)
	if err != nil {
		t.Fatalf("load first registry: %v", err)
	}
	firstStored := registry.List()[0]

	second, err := Setup(Config{
		Home:                   home,
		InstanceName:           "fixture",
		Relay:                  "wss://relay-two.example.test",
		DeviceName:             "second phone",
		AllowRemotePermissions: true,
	})
	if err != nil {
		t.Fatalf("second Setup: %v", err)
	}
	if second.Server != first.Server {
		t.Errorf("server changed: first %q, second %q", first.Server, second.Server)
	}
	if second.ServerStaticPubkey != first.ServerStaticPubkey {
		t.Error("static public key changed between setup calls")
	}
	if second.Token == first.Token {
		t.Error("repeat reused the first plaintext token")
	}

	registry, err = devices.Load(registryPath)
	if err != nil {
		t.Fatalf("load repeated registry: %v", err)
	}
	stored := registry.List()
	if len(stored) != 2 {
		t.Fatalf("stored devices = %d, want 2", len(stored))
	}
	firstAfter := findDevice(t, stored, first.Token)
	secondStored := findDevice(t, stored, second.Token)
	assertDeviceEqual(t, firstAfter, firstStored)
	if secondStored.Name != "second phone" {
		t.Errorf("second device name = %q, want %q", secondStored.Name, "second phone")
	}
	if !secondStored.AllowRemotePermissions {
		t.Error("second device remote permission = false, want true")
	}
}

func TestSetup_SaveFailureReturnsNoCredentialAndPreservesRegistry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cfg := Config{
		Home:         home,
		InstanceName: "fixture",
		Relay:        "wss://relay.example.test",
		DeviceName:   "failed phone",
	}
	if _, err := Setup(Config{
		Home:         home,
		InstanceName: cfg.InstanceName,
		Relay:        cfg.Relay,
		DeviceName:   "existing phone",
	}); err != nil {
		t.Fatalf("seed Setup: %v", err)
	}

	registryPath := filepath.Join(home, ".pyry", cfg.InstanceName, "devices.json")
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry before failure: %v", err)
	}

	rawToken := bytes.Repeat([]byte{0x5a}, 32)
	plainToken := hex.EncodeToString(rawToken)
	errSave := errors.New("forced registry save failure")
	payload, setupErr := setup(cfg, bytes.NewReader(rawToken), func(*devices.Registry, string) error {
		return errSave
	})
	if !errors.Is(setupErr, errSave) {
		t.Fatalf("setup error = %v, want wrapped save error", setupErr)
	}
	if payload != (pair.Payload{}) {
		t.Errorf("payload = %+v, want zero value", payload)
	}

	after, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry after failure: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("registry changed after failed save\n before: %s\n  after: %s", before, after)
	}
	registry, err := devices.Load(registryPath)
	if err != nil {
		t.Fatalf("load registry after failure: %v", err)
	}
	if got := len(registry.List()); got != 1 {
		t.Errorf("stored devices after failure = %d, want 1", got)
	}

	serverID, err := identity.LoadOrCreate(filepath.Join(home, ".pyry", cfg.InstanceName, "server-id"))
	if err != nil {
		t.Fatalf("load identity: %v", err)
	}
	staticKey, err := keys.LoadOrCreate(filepath.Join(home, ".pyry"), cfg.InstanceName)
	if err != nil {
		t.Fatalf("load static key: %v", err)
	}
	pub := staticKey.PublicKey()
	encodedPairing := pair.Encode(pair.Payload{
		Server:             serverID,
		Relay:              cfg.Relay,
		Token:              plainToken,
		ServerStaticPubkey: base64.StdEncoding.EncodeToString(pub[:]),
	})
	assertSecretAbsent(t, setupErr.Error(), plainToken, encodedPairing)

	walkErr := filepath.WalkDir(home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		assertSecretAbsent(t, string(contents), plainToken, encodedPairing)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk persisted files: %v", walkErr)
	}
}

func findDevice(t *testing.T, stored []devices.Device, plainToken string) devices.Device {
	t.Helper()
	for _, device := range stored {
		if devices.VerifyToken(plainToken, device.TokenHash) {
			return device
		}
	}
	t.Fatalf("no stored device verifies token")
	return devices.Device{}
}

func assertDeviceEqual(t *testing.T, got, want devices.Device) {
	t.Helper()
	if got.TokenHash != want.TokenHash || got.Name != want.Name ||
		!got.PairedAt.Equal(want.PairedAt) || !got.LastSeenAt.Equal(want.LastSeenAt) ||
		got.Platform != want.Platform || got.PushToken != want.PushToken ||
		got.AllowRemotePermissions != want.AllowRemotePermissions ||
		!got.RedeemBy.Equal(want.RedeemBy) {
		t.Errorf("device changed on repeat\n got: %+v\nwant: %+v", got, want)
	}
}

func assertSecretAbsent(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("text contains secret %q", secret)
		}
	}
}
