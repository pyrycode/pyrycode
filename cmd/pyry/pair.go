package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/pair"
)

// resolveDevicesPath returns ~/.pyry/<sanitized-name>/devices.json. Falls
// back to a CWD-relative path if $HOME can't be resolved (matches
// resolveRegistryPath's contract). Sanitization defends against
// PYRY_NAME=../../etc and similar path-traversal input.
func resolveDevicesPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "devices.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "devices.json")
}

// resolveServerIDPath returns ~/.pyry/<sanitized-name>/server-id. Falls
// back to a CWD-relative path if $HOME can't be resolved.
func resolveServerIDPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "server-id")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "server-id")
}

// resolveConfigPath returns ~/.pyry/config.json (per-user, not per-instance).
// Falls back to a CWD-relative path if $HOME can't be resolved.
func resolveConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "config.json"
	}
	return filepath.Join(home, ".pyry", "config.json")
}

// pairArgs is the parsed shape of `pyry pair`'s flag set.
type pairArgs struct {
	instanceName string // -pyry-name
	deviceName   string // --name
	relay        string // --relay
}

// parsePairArgs parses the flag set for `pyry pair`. Returns the parsed
// values and any error. Unknown flags or unexpected positionals produce
// errors propagated to the caller; runPair maps these to exit 2.
func parsePairArgs(args []string) (pairArgs, error) {
	fs := flag.NewFlagSet("pyry pair", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	instance := fs.String("pyry-name", defaultName(), "instance name (state dir: ~/.pyry/<name>/)")
	deviceName := fs.String("name", "", "device label persisted in the registry (default: device-<short>)")
	relay := fs.String("relay", "", "relay URL override (default: ~/.pyry/config.json or built-in default)")
	if err := fs.Parse(args); err != nil {
		return pairArgs{}, err
	}
	if fs.NArg() > 0 {
		return pairArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return pairArgs{
		instanceName: *instance,
		deviceName:   *deviceName,
		relay:        *relay,
	}, nil
}

// resolveRelay returns the first non-empty value among:
//  1. flagValue (from --relay)
//  2. cfg.RelayURL (from ~/.pyry/config.json, with defaults overlaid)
//  3. config.DefaultConfig().RelayURL
//
// Returns "" only if all three are empty (only reachable if the built-in
// default is empty *and* the on-disk file is absent/unset *and* the flag
// is unset). The third leg is normally redundant — config.Load already
// overlays DefaultConfig — but the AC names it explicitly.
func resolveRelay(flagValue string, cfg config.Config) string {
	if flagValue != "" {
		return flagValue
	}
	if cfg.RelayURL != "" {
		return cfg.RelayURL
	}
	return config.DefaultConfig().RelayURL
}

// runPair implements `pyry pair`: load config + registry + server-id,
// mint a 256-bit token, persist a Device entry (hashed), and render the
// pairing payload (QR + paste fallback) to stdout.
//
// Returns nil on success. Returns a wrapped error for exit-1 conditions
// (I/O errors, render write errors). Calls os.Exit(2) directly for
// exit-2 conditions (flag parse error, empty resolved relay) so the
// `pyry: ` prefix that main's top-level error printer adds doesn't
// appear on usage-style failures.
func runPair(args []string) error {
	parsed, err := parsePairArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry pair:", err)
		fmt.Fprintln(os.Stderr, "usage: pyry pair [-pyry-name=<instance>] [--name <label>] [--relay <url>]")
		os.Exit(2)
	}

	cfg, err := config.Load(resolveConfigPath())
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}

	relay := resolveRelay(parsed.relay, cfg)
	if relay == "" {
		fmt.Fprintln(os.Stderr, "pyry pair: relay URL is empty (set --relay or relay_url in ~/.pyry/config.json)")
		os.Exit(2)
	}

	devicesPath := resolveDevicesPath(parsed.instanceName)
	registry, err := devices.Load(devicesPath)
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}

	serverID, err := identity.LoadOrCreate(resolveServerIDPath(parsed.instanceName))
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Errorf("pair: read random: %w", err)
	}
	plain := hex.EncodeToString(raw[:])
	hash := devices.HashToken(plain)

	deviceName := parsed.deviceName
	if deviceName == "" {
		deviceName = "device-" + hash[:8]
	}

	registry.Add(devices.Device{
		TokenHash: hash,
		Name:      deviceName,
		PairedAt:  time.Now().UTC(),
	})
	if err := registry.Save(devicesPath); err != nil {
		return fmt.Errorf("pair: %w", err)
	}

	payload := pair.Payload{
		Server: serverID,
		Relay:  relay,
		Token:  plain,
	}
	if err := pair.Render(payload, os.Stdout); err != nil {
		return fmt.Errorf("pair: render: %w", err)
	}
	return nil
}
