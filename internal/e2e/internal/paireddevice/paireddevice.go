// Package paireddevice seeds a valid phone credential in an isolated test home
// before an e2e daemon starts. Its internal/e2e/internal placement keeps this
// offline setup path unavailable to production packages and external callers.
package paireddevice

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/keys"
	"github.com/pyrycode/pyrycode/internal/pair"
)

// Config describes one pre-daemon phone credential. Home must be an absolute
// isolated user home, InstanceName must satisfy keys.LoadOrCreate's daemon-name
// contract, and Relay must be non-empty. DeviceName may be empty to request the
// standard device-<hash-prefix> fallback.
type Config struct {
	Home                   string
	InstanceName           string
	Relay                  string
	DeviceName             string
	AllowRemotePermissions bool
}

// Setup persists the instance identity and static key plus one new device, then
// returns the pairing payload for that device. Sequential calls for the same
// instance reuse its identity and key and append independently valid devices.
// Every error returns a zero-value payload; plaintext tokens are never persisted.
func Setup(cfg Config) (pair.Payload, error) {
	return setup(cfg, rand.Reader, (*devices.Registry).Save)
}

func setup(cfg Config, random io.Reader, save func(*devices.Registry, string) error) (pair.Payload, error) {
	if !filepath.IsAbs(cfg.Home) {
		return pair.Payload{}, fmt.Errorf("paired device: home must be absolute")
	}
	if cfg.Relay == "" {
		return pair.Payload{}, fmt.Errorf("paired device: relay must not be empty")
	}

	baseDir := filepath.Join(cfg.Home, ".pyry")
	staticKey, err := keys.LoadOrCreate(baseDir, cfg.InstanceName)
	if err != nil {
		return pair.Payload{}, fmt.Errorf("paired device: static key: %w", err)
	}

	instanceDir := filepath.Join(baseDir, cfg.InstanceName)
	serverID, err := identity.LoadOrCreate(filepath.Join(instanceDir, "server-id"))
	if err != nil {
		return pair.Payload{}, fmt.Errorf("paired device: server identity: %w", err)
	}

	var rawToken [32]byte
	if _, err := io.ReadFull(random, rawToken[:]); err != nil {
		return pair.Payload{}, fmt.Errorf("paired device: random token: %w", err)
	}
	plainToken := hex.EncodeToString(rawToken[:])
	tokenHash := devices.HashToken(plainToken)
	deviceName := cfg.DeviceName
	if deviceName == "" {
		deviceName = "device-" + tokenHash[:8]
	}

	pairedAt := time.Now().UTC()
	registryPath := filepath.Join(instanceDir, "devices.json")
	registry, err := devices.Load(registryPath)
	if err != nil {
		return pair.Payload{}, fmt.Errorf("paired device: load registry: %w", err)
	}
	registry.Add(devices.Device{
		TokenHash:              tokenHash,
		Name:                   deviceName,
		PairedAt:               pairedAt,
		RedeemBy:               pairedAt.Add(devices.RedemptionWindow),
		AllowRemotePermissions: cfg.AllowRemotePermissions,
	})
	if err := save(registry, registryPath); err != nil {
		return pair.Payload{}, fmt.Errorf("paired device: save registry: %w", err)
	}

	publicKey := staticKey.PublicKey()
	return pair.Payload{
		Server:             serverID,
		Relay:              cfg.Relay,
		Token:              plainToken,
		ServerStaticPubkey: base64.StdEncoding.EncodeToString(publicKey[:]),
	}, nil
}
