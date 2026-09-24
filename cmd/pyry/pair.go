package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/pair"
)

// pairVerbList is the displayed verb list in `pyry pair` usage errors.
// Update in lockstep with the switch in runPair when new sub-verbs land.
const pairVerbList = "list, revoke, preflight"

// pairLockWait bounds how long `pyry pair revoke` blocks waiting for the
// cross-process devices lock. WithLock's doc points operator-invoked one-shots
// at DefaultLockWait.
//
// A var rather than a const purely so the timeout-path test can shrink it — a
// contention test that waits the real bound out costs the suite the full
// default. Unexported, and production never reassigns it.
var pairLockWait = devices.DefaultLockWait

// mintRequest is one ask of mintDevice: where to write, how long to wait for the
// lock, what to call the device, and whether it may answer remote permissions.
//
// A STRUCT RATHER THAN FIVE POSITIONAL ARGUMENTS because two of the fields are
// booleans-in-effect whose meaning is invisible at a call site — a bare `false`
// for the privilege flag and a bare `""` for the grantor check are exactly the
// values a reader must not have to count commas to identify.
type mintRequest struct {
	// devicesPath is the registry to append to; the lock sidecar is derived from
	// it by devices.WithLock.
	devicesPath string

	// lockWait bounds the acquisition. A PARAMETER RATHER THAN pairLockWait
	// directly, because both request-path minters have a client waiting and pass
	// their own short bound.
	lockWait time.Duration

	// deviceName is the label to file the record under. The EMPTY STRING is not an
	// error: it selects the device-<hash8> fallback, which is what makes a mint
	// with no name asked for indistinguishable between both entry points.
	//
	// UNVALIDATED HERE, AND DELIBERATELY SO. From the local control socket it is
	// operator-authored input; from the remote wire it has already passed the
	// relay handler's display-safety gate.
	deviceName string

	// allowRemotePermissions sets devices.Device.AllowRemotePermissions. The
	// mode-0600 local control request may pass true. The remote phone mint always
	// passes a literal false, and protocol.MintPairingPayload has no field that
	// could carry anything else.
	allowRemotePermissions bool

	// grantorHash, when non-empty, is a token hash that must STILL name a device
	// carrying AllowRemotePermissions at the moment of the write, checked against
	// the snapshot this mutates inside the held lock. It is what makes `pyry pair
	// revoke` effective against a live minting session rather than only against
	// the revoked device's next connection: v2 reloads devices.json per handshake,
	// so a conn's authenticated record is otherwise as of connect time.
	//
	// The mode-0600 local control provider passes "" — the host operator is the
	// authority this check exists to defer to, not a subject of it.
	grantorHash string
}

// mintedDevice is one freshly minted pairing's non-derivable half: the plaintext
// token and the label the record was actually filed under.
type mintedDevice struct {
	// token is the plaintext bearer credential, hex-encoded. It exists only in
	// this process's RAM: its hash is what reached disk, and § Token visibility's
	// single-egress rule binds every caller.
	token string

	// name is the label stored on the record — the caller's deviceName, or the
	// device-<hash8> fallback when it named none. Returned because a caller that
	// wants to record WHICH device it minted cannot re-derive the fallback without
	// hashing the token itself.
	name string
}

// errGrantorRevoked reports that mintRequest.grantorHash named no privileged
// device in the registry snapshot inside the lock. Its own sentinel rather than a
// generic error so the wire minter can answer the privilege refusal — which is
// permanent and audited — rather than the retryable host-failure code every other
// error from this function earns.
var errGrantorRevoked = errors.New("pair: minting device is no longer privileged")

// mintDevice performs the mint shared by the local control pairing.mint
// operation and the remote wire mint_pairing operation: draw a token, hash it,
// name the device, and append the record inside ONE held devices lock.
//
// ONE FUNCTION, TWO REQUEST PATHS, so their stampings cannot drift.
//
// THE LOAD RUNS INSIDE THE LOCK, and that is #1531's whole lesson rather than a
// stylistic preference: wrapping only the Save would still mutate a snapshot taken
// outside the region and leave the erase-a-concurrent-write bug fully intact.
// Everything the callers do around this — daemon identity and key loads, and
// payload encoding — stays outside, because none of it touches
// devices.json and redemptionLockWait's doc comment promises its peers hold
// sub-millisecond regions.
//
// ONE CLOCK READ feeds both stamps, so RedeemBy - PairedAt is exactly
// devices.RedemptionWindow rather than that window plus whatever scheduling delay
// fell between two time.Now() calls.
//
// IT FAILS CLOSED. Every error path returns before or instead of the Save, so a
// token whose hash never reached disk is discarded unrendered and unrecorded —
// unusable rather than unaccounted for.
//
// SECURITY: no error this function returns can carry the plaintext token. The four
// sources are crypto/rand.Read, devices.WithLock, devices.Load and Registry.Save;
// none is handed the token, and the registry holds hashes only. That is
// § Token visibility's argument, restated at the seam where a second caller now
// meets it.
func mintDevice(req mintRequest) (mintedDevice, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return mintedDevice{}, fmt.Errorf("read random: %w", err)
	}
	plain := hex.EncodeToString(raw[:])
	hash := devices.HashToken(plain)

	deviceName := req.deviceName
	if deviceName == "" {
		deviceName = "device-" + hash[:8]
	}

	mintedAt := time.Now().UTC()

	if err := devices.WithLock(req.devicesPath, req.lockWait, func() error {
		registry, err := devices.Load(req.devicesPath)
		if err != nil {
			return err
		}
		// The grantor check reads the SAME snapshot the append below mutates, so a
		// revocation that committed before this lock was acquired is visible here
		// and one committing after it cannot race in.
		if req.grantorHash != "" && !registryGrants(registry, req.grantorHash) {
			return errGrantorRevoked
		}
		registry.Add(devices.Device{
			TokenHash:              hash,
			Name:                   deviceName,
			PairedAt:               mintedAt,
			RedeemBy:               mintedAt.Add(devices.RedemptionWindow),
			AllowRemotePermissions: req.allowRemotePermissions,
		})
		return registry.Save(req.devicesPath)
	}); err != nil {
		return mintedDevice{}, err
	}
	return mintedDevice{token: plain, name: deviceName}, nil
}

// registryGrants reports whether hash names a device in registry that still
// carries AllowRemotePermissions. A device that is absent — revoked — and one
// present but no longer privileged answer identically: both mean the grantor may
// no longer mint, and neither is a distinction any caller acts on.
func registryGrants(registry *devices.Registry, hash string) bool {
	for _, d := range registry.List() {
		if d.TokenHash == hash {
			return d.AllowRemotePermissions
		}
	}
	return false
}

// errPairDeviceNotFound signals out of `pyry pair revoke`'s locked region that
// the named device was absent, so the os.Exit(1) that reports it can run
// outside. An os.Exit inside the region would skip WithLock's deferred unlock
// and close: the kernel releases the flock at process termination either way,
// but any other deferred cleanup added inside would silently stop running.
// WithLock returns its function's error verbatim, so a sentinel costs nothing.
//
// Callers match it with errors.Is, never by comparing the printed message, so a
// device whose Name happens to read like the not-found line cannot reach this
// branch. A failed acquisition arrives as devices.ErrLockBusy instead and falls
// through to the I/O-error path, so a busy lock never prints "no device named".
var errPairDeviceNotFound = errors.New("pair revoke: no such device")

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

// resolveStaticKeyBaseDir returns the parent directory under which
// internal/keys places <daemonName>/static_key.json. Returns ~/.pyry
// when HOME resolves; falls back to "" so keys.LoadOrCreate writes the
// keypair into "./<daemonName>/". Mirrors resolveDevicesPath /
// resolveServerIDPath's fallback shape.
func resolveStaticKeyBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pyry")
}

// pairArgs is the parsed shape of `pyry pair`'s flag set.
type pairArgs struct {
	instanceName           string // -pyry-name
	instanceExplicit       bool   // -pyry-name appeared on the command line
	deviceName             string // --name
	relay                  string // --relay
	relayExplicit          bool   // --relay appeared, including --relay=
	allowRemotePermissions bool   // --allow-remote-permissions
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
	allowRemotePermissions := fs.Bool("allow-remote-permissions", false, "authorize this device to answer remote permission/trust/destructive modals (default OFF)")
	if err := fs.Parse(args); err != nil {
		return pairArgs{}, err
	}
	if fs.NArg() > 0 {
		return pairArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	var instanceExplicit, relayExplicit bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pyry-name":
			instanceExplicit = true
		case "relay":
			relayExplicit = true
		}
	})
	return pairArgs{
		instanceName:           *instance,
		instanceExplicit:       instanceExplicit,
		deviceName:             *deviceName,
		relay:                  *relay,
		relayExplicit:          relayExplicit,
		allowRemotePermissions: *allowRemotePermissions,
	}, nil
}

// runPair dispatches `pyry pair [<verb>] [flags]`. With no leading
// non-flag positional, falls through to the bare-pair flow
// (runPairDefault). The first non-flag positional, if any, is treated
// as a sub-verb; unknown verbs exit 2 directly so the top-level
// `pyry: ` prefix does not appear on usage failures.
//
// Flags-first invocations like `pyry pair --name=foo` keep working
// because args[0] starting with "-" is not treated as a verb.
func runPair(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runPairList(args[1:])
		case "revoke":
			return runPairRevoke(args[1:])
		case "preflight":
			return runPairPreflight(args[1:])
		}
		if !strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "pyry pair: unknown verb %q\n", args[0])
			fmt.Fprintln(os.Stderr, "verbs:", pairVerbList, "(or omit for the default pair flow)")
			os.Exit(2)
		}
	}
	return runPairDefault(args)
}

type pairingService struct {
	name       string
	socketPath string
}

// runPairDefault implements bare `pyry pair` through one running daemon. An
// explicit -pyry-name targets only that service. Without one, the sole
// status-responsive socket under ~/.pyry is selected; zero or several services
// fail before a pairing is requested.
func runPairDefault(args []string) error {
	parsed, err := parsePairArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry pair:", err)
		fmt.Fprintln(os.Stderr, "usage: pyry pair [-pyry-name=<instance>] [--name <label>] [--allow-remote-permissions]")
		os.Exit(2)
	}
	if parsed.relayExplicit {
		fmt.Fprintln(os.Stderr, "pyry pair: --relay is not supported; the running service owns the relay destination")
		fmt.Fprintln(os.Stderr, "usage: pyry pair [-pyry-name=<instance>] [--name <label>] [--allow-remote-permissions]")
		os.Exit(2)
	}

	ctx := context.Background()
	service, err := selectPairingService(ctx, parsed)
	if err != nil {
		return err
	}
	encoded, err := control.MintPairing(ctx, service.socketPath, parsed.deviceName, parsed.allowRemotePermissions)
	if err != nil {
		return fmt.Errorf("pair: service %q at %s: %w", service.name, service.socketPath, err)
	}
	payload, err := pair.Decode(encoded)
	if err != nil {
		return fmt.Errorf("pair: service %q returned an invalid pairing", service.name)
	}
	if _, err := fmt.Fprintf(os.Stdout, "Service: %s\n\n", service.name); err != nil {
		return fmt.Errorf("pair: render service name: %w", err)
	}
	if err := pair.Render(payload, os.Stdout); err != nil {
		return fmt.Errorf("pair: render: %w", err)
	}
	return nil
}

func selectPairingService(ctx context.Context, parsed pairArgs) (pairingService, error) {
	if parsed.instanceExplicit {
		return pairingService{
			name:       sanitizeName(parsed.instanceName),
			socketPath: resolveSocketPath("", parsed.instanceName),
		}, nil
	}

	pattern := filepath.Join(filepath.Dir(resolveSocketPath("", DefaultName)), "*.sock")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return pairingService{}, fmt.Errorf("pair: discover running services: %w", err)
	}
	services := make([]pairingService, 0, len(paths))
	for _, socketPath := range paths {
		name := strings.TrimSuffix(filepath.Base(socketPath), ".sock")
		if name == "" || sanitizeName(name) != name {
			continue
		}
		if _, err := control.Status(ctx, socketPath); err != nil {
			continue
		}
		services = append(services, pairingService{name: name, socketPath: socketPath})
	}

	switch len(services) {
	case 0:
		return pairingService{}, errors.New("pair: no running services found; start pyry and retry")
	case 1:
		return services[0], nil
	default:
		names := make([]string, len(services))
		for i, service := range services {
			names[i] = service.name
		}
		return pairingService{}, fmt.Errorf("pair: multiple running services found: %s; specify -pyry-name", strings.Join(names, ", "))
	}
}

// pairListArgs is the parsed shape of `pyry pair list`'s flag set.
type pairListArgs struct {
	instanceName string // -pyry-name
}

// parsePairListArgs parses the flag set for `pyry pair list`. Only
// -pyry-name is accepted. Unknown flags or unexpected positionals
// produce errors propagated to the caller; runPairList maps these to
// exit 2.
func parsePairListArgs(args []string) (pairListArgs, error) {
	fs := flag.NewFlagSet("pyry pair list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	instance := fs.String("pyry-name", defaultName(), "instance name (state dir: ~/.pyry/<name>/)")
	if err := fs.Parse(args); err != nil {
		return pairListArgs{}, err
	}
	if fs.NArg() > 0 {
		return pairListArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return pairListArgs{instanceName: *instance}, nil
}

// runPairList implements `pyry pair list`: load the device registry
// for the resolved instance and write a tabular listing to stdout.
// Read-only — never calls Save/Add/Remove.
//
// Returns nil on success (including the empty-registry case). Returns
// a wrapped error for exit-1 conditions (registry I/O, malformed
// JSON, stdout write error). Calls os.Exit(2) directly for exit-2
// conditions (flag parse error, unexpected positional).
func runPairList(args []string) error {
	parsed, err := parsePairListArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry pair list:", err)
		fmt.Fprintln(os.Stderr, "usage: pyry pair list [-pyry-name=<instance>]")
		os.Exit(2)
	}
	devicesPath := resolveDevicesPath(parsed.instanceName)
	registry, err := devices.Load(devicesPath)
	if err != nil {
		return fmt.Errorf("pair list: %w", err)
	}
	if err := renderPairList(registry.List(), os.Stdout); err != nil {
		return fmt.Errorf("pair list: %w", err)
	}
	return nil
}

// renderPairList writes the tabular listing of paired devices to w.
// On an empty list, writes exactly "No paired devices.\n". On a
// non-empty list, writes a header row plus one data row per device,
// padded by text/tabwriter, sorted by (PairedAt, Name) ascending.
//
// Pure: no globals, no os.Stdout access, no clock reads. The output is
// a deterministic function of list, which makes the formatter
// unit-testable byte-for-byte.
func renderPairList(list []devices.Device, w io.Writer) error {
	if len(list) == 0 {
		_, err := io.WriteString(w, "No paired devices.\n")
		return err
	}
	sorted := append([]devices.Device(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].PairedAt.Equal(sorted[j].PairedAt) {
			return sorted[i].PairedAt.Before(sorted[j].PairedAt)
		}
		return sorted[i].Name < sorted[j].Name
	})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPAIRED\tLAST SEEN\tVERSION\tTOKEN-PREFIX")
	for _, d := range sorted {
		lastSeen := "never"
		if !d.LastSeenAt.IsZero() {
			lastSeen = d.LastSeenAt.Format(time.RFC3339)
		}
		prefix := d.TokenHash
		if len(prefix) >= 8 {
			prefix = prefix[:8]
		}
		// VERSION is the client_version the device's last accepted hello
		// reported (#2577), already admitted by the handshake; empty for a
		// device that has not connected since, or reported nothing usable.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			d.Name, d.PairedAt.Format(time.RFC3339), lastSeen, d.ClientVersion, prefix)
	}
	return tw.Flush()
}

// pairRevokeArgs is the parsed shape of `pyry pair revoke <name>`'s flag set.
type pairRevokeArgs struct {
	instanceName string // -pyry-name
	deviceName   string // sole positional; the entry to remove
}

// parsePairRevokeArgs parses the flag set for `pyry pair revoke`. Only
// -pyry-name is accepted, and exactly one positional (the device Name) is
// required. Zero, two, or more positionals — or any unknown flag — is an
// error propagated to the caller; runPairRevoke maps these to exit 2.
func parsePairRevokeArgs(args []string) (pairRevokeArgs, error) {
	fs := flag.NewFlagSet("pyry pair revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	instance := fs.String("pyry-name", defaultName(), "instance name (state dir: ~/.pyry/<name>/)")
	if err := fs.Parse(args); err != nil {
		return pairRevokeArgs{}, err
	}
	switch fs.NArg() {
	case 0:
		return pairRevokeArgs{}, fmt.Errorf("missing device name")
	case 1:
		return pairRevokeArgs{instanceName: *instance, deviceName: fs.Arg(0)}, nil
	default:
		return pairRevokeArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(1))
	}
}

// runPairRevoke implements `pyry pair revoke <name>`: under the
// cross-process devices lock, load the device registry for the resolved
// instance, remove the entry whose Name equals <name>, and persist the
// change. Re-reading inside the region is what stops the save
// resurrecting a device a racing `pyry pair` committed in between — the
// security-relevant direction, since it would make a credential the
// operator explicitly withdrew acceptable again.
//
// Returns nil on success (writes "Revoked <name>.\n" to stdout, exit 0).
// Calls os.Exit(2) directly for usage failures (flag parse, missing or
// extra positional) — bypasses main's `pyry: ` prefix.
// Calls os.Exit(1) directly for the not-found case ("no device named
// <name>" stderr) — same prefix-bypass reason.
// Returns a wrapped `fmt.Errorf("pair revoke: %w", err)` for I/O errors
// (Load/Save) — main.run prefixes with `pyry: ` to give the full
// `pyry: pair revoke: …` chain.
func runPairRevoke(args []string) error {
	parsed, err := parsePairRevokeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry pair revoke:", err)
		fmt.Fprintln(os.Stderr, "usage: pyry pair revoke [-pyry-name=<instance>] <name>")
		os.Exit(2)
	}
	devicesPath := resolveDevicesPath(parsed.instanceName)

	// Read, remove, and save inside one held lock, re-reading here rather than
	// mutating a snapshot taken before the acquisition — otherwise a device a
	// racing `pyry pair` committed in between is erased by this Save.
	err = devices.WithLock(devicesPath, pairLockWait, func() error {
		registry, err := devices.Load(devicesPath)
		if err != nil {
			return err
		}
		if !registry.Remove(parsed.deviceName) {
			return errPairDeviceNotFound
		}
		return registry.Save(devicesPath)
	})
	if errors.Is(err, errPairDeviceNotFound) {
		fmt.Fprintf(os.Stderr, "pyry pair revoke: no device named %s\n", parsed.deviceName)
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("pair revoke: %w", err)
	}
	fmt.Printf("Revoked %s.\n", parsed.deviceName)
	return nil
}

// pairPreflightArgs is the parsed shape of `pyry pair preflight`'s flag set.
type pairPreflightArgs struct {
	instanceName string // -pyry-name
}

// parsePairPreflightArgs parses the flag set for `pyry pair preflight`.
// Only -pyry-name is accepted. Unknown flags or unexpected positionals
// produce errors propagated to the caller; runPairPreflight maps these
// to exit 2.
func parsePairPreflightArgs(args []string) (pairPreflightArgs, error) {
	fs := flag.NewFlagSet("pyry pair preflight", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	instance := fs.String("pyry-name", defaultName(), "instance name (state dir: ~/.pyry/<name>/)")
	if err := fs.Parse(args); err != nil {
		return pairPreflightArgs{}, err
	}
	if fs.NArg() > 0 {
		return pairPreflightArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return pairPreflightArgs{instanceName: *instance}, nil
}

// preflightVerdict returns (exitCode, stderrLine) for the v2 release gate.
// exitCode == 0 means the gate passed (count == 0); stderrLine is "".
// exitCode == 2 means the gate failed (count > 0); stderrLine is the exact
// message to emit before os.Exit(2). Pure: deterministic on count.
func preflightVerdict(count int) (exitCode int, stderrLine string) {
	if count == 0 {
		return 0, ""
	}
	return 2, fmt.Sprintf("pyry pair preflight: %d paired device(s); v2 release gate requires zero.", count)
}

// runPairPreflight implements `pyry pair preflight`: load the device
// registry for the resolved instance and exit non-zero if any paired
// device exists. Strictly opt-in release gate for the v2 cutover; does
// not alter the default `pyry pair list` output.
//
// Returns nil on the gate-pass path (registry empty, exit 0). Returns a
// wrapped `fmt.Errorf("pair preflight: %w", err)` for I/O failures
// (Load) — main.run prefixes with `pyry: ` and os.Exit(1). Calls
// os.Exit(2) directly for the gate-fail and usage-failure paths to
// bypass main's `pyry: ` prefix.
func runPairPreflight(args []string) error {
	parsed, err := parsePairPreflightArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry pair preflight:", err)
		fmt.Fprintln(os.Stderr, "usage: pyry pair preflight [-pyry-name=<instance>]")
		os.Exit(2)
	}
	devicesPath := resolveDevicesPath(parsed.instanceName)
	registry, err := devices.Load(devicesPath)
	if err != nil {
		return fmt.Errorf("pair preflight: %w", err)
	}
	if exitCode, line := preflightVerdict(len(registry.List())); exitCode != 0 {
		fmt.Fprintln(os.Stderr, line)
		os.Exit(exitCode)
	}
	return nil
}
