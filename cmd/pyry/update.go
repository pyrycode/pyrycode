package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/update"
)

// releaseSigningPublicKeyHex is the hex-encoded raw 32-byte Ed25519 public
// key whose private half signs checksums.txt in the release pipeline. Every
// `pyry update` verifies the downloaded checksums.txt against this key before
// trusting any digest in it, so a compromised GitHub release or stolen
// publishing token cannot install attacker-chosen bytes: without the private
// key an attacker cannot produce a signature that verifies here.
//
// Generated with:
//
//	openssl genpkey -algorithm ed25519 -out signing_key.pem
//	openssl pkey -in signing_key.pem -pubout -outform DER | tail -c 32 | xxd -p -c 32
//
// The release workflow reads the private half from the PYRY_RELEASE_SIGNING_KEY
// Actions secret on pyrycode/pyrycode — never committed. Regenerating the keypair means
// updating this constant in the same change (a mismatch makes every update
// fail closed). See docs/release-tooling.md.
const releaseSigningPublicKeyHex = "c1839980c8be806616a2761058692426a33f15999b7021d04627d3b99616d282"

// runUpdate implements `pyry update`: fetch the latest release, verify the
// tarball's SHA-256, extract the pyry binary, atomically replace the running
// binary on disk, and (unless --no-restart is set) restart the managed pyry
// daemon if a launchd plist or systemd user unit is detected.
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("pyry update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "print current and latest versions, then exit")
	pinVersion := fs.String("version", "", "install this version instead of the latest release")
	noRestart := fs.Bool("no-restart", false, "skip daemon restart even if a managed unit is detected")
	if err := fs.Parse(args); err != nil {
		return err
	}

	o, err := productionUpdateOptions(os.Stdout)
	if err != nil {
		return err
	}
	o.checkOnly = *checkOnly
	o.pinVersion = *pinVersion
	o.noRestart = *noRestart
	return doUpdate(context.Background(), o)
}

// productionUpdateOptions returns the real seams `pyry update` and the daemon's
// auto-update share, writing progress to out. Both callers build from here so
// the release source, signing key and install path cannot drift apart.
func productionUpdateOptions(out io.Writer) (updateOptions, error) {
	signingPubKey, err := hex.DecodeString(releaseSigningPublicKeyHex)
	if err != nil {
		return updateOptions{}, fmt.Errorf("update: signing key: decode hex: %w", err)
	}
	if len(signingPubKey) != ed25519.PublicKeySize {
		return updateOptions{}, fmt.Errorf("update: signing key: got %d bytes, want %d",
			len(signingPubKey), ed25519.PublicKeySize)
	}

	return updateOptions{
		currentVersion: Version,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		repo:           "pyrycode/pyrycode",
		releaseBaseURL: "https://github.com/pyrycode/pyrycode/releases/download",
		fetcher: &update.Fetcher{
			UserAgent:  "pyry/" + Version,
			HTTPClient: &http.Client{Timeout: 60 * time.Second},
		},
		executablePath: resolveExecutable,
		replace:        update.AtomicReplace,
		signingPubKey:  ed25519.PublicKey(signingPubKey),
		out:            out,
		probeRestart:   defaultProbeRestart,
		runRestart:     defaultRunRestart,
	}, nil
}

// resolveExecutable returns the path to the running pyry binary. Falls back
// to os.Args[0] if os.Executable() errors (rare; mostly /proc unavailability
// on exotic platforms).
func resolveExecutable() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return os.Args[0]
}

// updateOptions bundles the seams the integration test overrides: the
// fetcher's BaseURL, the release-asset BaseURL template, the executable-path
// resolver, the AtomicReplace function, the checksums-signing public key,
// the daemon-restart probe and executor, and stdout. Production callers pass
// real defaults; tests substitute httptest + tempdir equivalents (and their
// own throwaway test key, never the production key).
type updateOptions struct {
	currentVersion string
	goos, goarch   string
	repo           string
	releaseBaseURL string
	fetcher        *update.Fetcher
	executablePath func() string
	replace        func(target string, data []byte, mode os.FileMode) error
	signingPubKey  ed25519.PublicKey
	out            io.Writer
	checkOnly      bool
	pinVersion     string
	noRestart      bool
	probeRestart   func() update.RestartProbe
	runRestart     func(ctx context.Context, argv []string) error
}

// defaultProbeRestart stats the canonical launchd plist and systemd user-unit
// paths to determine which (if either) managed pyry daemon is installed.
// Stat errors of any kind collapse to "not present" — the only question is
// whether the file is there. If $HOME is unresolvable, both stats fail and
// DetectRestartCommand returns nil (silent skip).
func defaultProbeRestart() update.RestartProbe {
	home, _ := os.UserHomeDir()
	_, plistErr := os.Stat(filepath.Join(home, "Library/LaunchAgents", "dev.pyrycode.pyry.plist"))
	_, unitErr := os.Stat(filepath.Join(home, ".config/systemd/user", "pyry.service"))
	return update.RestartProbe{
		LaunchdPlistExists: plistErr == nil,
		SystemdUnitExists:  unitErr == nil,
		UID:                strconv.Itoa(os.Getuid()),
	}
}

// defaultRunRestart execs the restart argv with stdio wired to the real
// terminal so any launchctl/systemctl diagnostics reach the user verbatim.
func defaultRunRestart(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func doUpdate(ctx context.Context, o updateOptions) error {
	target := o.executablePath()
	if strings.HasPrefix(target, "/opt/homebrew/") {
		fmt.Fprintln(o.out, "Hint: this pyry was installed via Homebrew; consider 'brew upgrade pyry' instead.")
	}

	fmt.Fprintf(o.out, "==> Current version: %s\n", o.currentVersion)

	var targetVer string
	if o.pinVersion != "" {
		targetVer = o.pinVersion
	} else {
		body, err := o.fetcher.FetchLatestRelease(ctx, o.repo)
		if err != nil {
			return fmt.Errorf("update: fetch latest release: %w", err)
		}
		tag, err := update.ParseLatestRelease(body)
		if err != nil {
			return fmt.Errorf("update: parse latest release: %w", err)
		}
		targetVer = tag
	}
	fmt.Fprintf(o.out, "==> Latest version:  %s\n", targetVer)

	cmp, err := update.CompareVersions(o.currentVersion, targetVer)
	switch {
	case errors.Is(err, update.ErrInvalidVersion):
		// A development build (Version == "dev" by default) cannot be
		// compared to a release tag. Replacing it would silently revert
		// the developer's working copy, so we skip the self-update.
		fmt.Fprintf(o.out, "==> Running a development build (%s); skipping update.\n", o.currentVersion)
		return nil
	case err != nil:
		return fmt.Errorf("update: compare versions: %w", err)
	case cmp == update.Same:
		fmt.Fprintf(o.out, "==> Current version: %s — already at latest.\n", o.currentVersion)
		return nil
	}

	if o.checkOnly {
		return nil
	}

	// A latest release *older* than the running binary is a rollback: either a
	// yanked release, or a publisher whose token was stolen re-marking an old
	// genuine release as latest. The signature gate cannot catch that — the old
	// release was validly signed when it shipped — so refuse the install and
	// point at the sanctioned route. Both halves of the condition are
	// load-bearing: update.Newer means *current* is newer than latest (the
	// rollback direction), and an explicit --version pin is the documented
	// downgrade route, which must keep working. This guard sits below the
	// checkOnly return on purpose: `pyry update --check` still prints both
	// versions and exits 0.
	if o.pinVersion == "" && cmp == update.Newer {
		return fmt.Errorf("update: refuse downgrade: latest release %s is older than the running version %s; "+
			"run 'pyry update --version %s' to downgrade intentionally", targetVer, o.currentVersion, targetVer)
	}

	if err := installRelease(ctx, o, target, targetVer); err != nil {
		return err
	}

	if !o.noRestart {
		probe := o.probeRestart()
		if argv := update.DetectRestartCommand(probe); argv != nil {
			manager := "launchd"
			if probe.SystemdUnitExists && !probe.LaunchdPlistExists {
				manager = "systemd"
			}
			fmt.Fprintf(o.out, "==> Restarting daemon (%s: %s)...\n", manager, argv[len(argv)-1])
			if err := o.runRestart(ctx, argv); err != nil {
				return fmt.Errorf("update: binary replaced to %s, but daemon restart failed: %w", targetVer, err)
			}
		}
	}

	fmt.Fprintf(o.out, "==> Updated to %s.\n", targetVer)
	return nil
}

// installRelease downloads release tag, verifies checksums.txt against the
// baked-in signing key and the tarball against checksums.txt, extracts the pyry
// binary, keeps the binary at target as pyry.prev, and atomically replaces
// target. Progress goes to o.out. Nothing on disk changes unless both
// verifications pass. It does not restart anything.
func installRelease(ctx context.Context, o updateOptions, target, tag string) error {
	asset, err := update.AssetName(tag, o.goos, o.goarch)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	tarballURL := fmt.Sprintf("%s/%s/%s", o.releaseBaseURL, tag, asset)
	checksumsURL := fmt.Sprintf("%s/%s/checksums.txt", o.releaseBaseURL, tag)

	fmt.Fprintf(o.out, "==> Downloading %s...\n", asset)
	tgz, err := o.fetcher.FetchAsset(ctx, tarballURL)
	if err != nil {
		return fmt.Errorf("update: download tarball: %w", err)
	}

	sumsBytes, err := o.fetcher.FetchAsset(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("update: download checksums: %w", err)
	}

	// Verify checksums.txt against the baked-in signing key *before* any
	// digest is parsed from it. A missing signature (404) fails closed here
	// exactly like a missing tarball — there is no unsigned fallback — and a
	// signature that does not verify aborts before the binary is extracted or
	// written over the target. Verify over the raw fetched bytes: the
	// signature covers the exact file GoReleaser signed, so do not trim or
	// normalize sumsBytes first.
	fmt.Fprint(o.out, "==> Verifying signature... ")
	sig, err := o.fetcher.FetchAsset(ctx, checksumsURL+".sig")
	if err != nil {
		fmt.Fprintln(o.out, "FAIL")
		return fmt.Errorf("update: download signature: %w", err)
	}
	if err := update.VerifySignature(sumsBytes, sig, o.signingPubKey); err != nil {
		fmt.Fprintln(o.out, "FAIL")
		return fmt.Errorf("update: verify signature: %w", err)
	}
	fmt.Fprintln(o.out, "ok")

	digest, err := update.ParseChecksumsFile(string(sumsBytes), asset)
	if err != nil {
		return fmt.Errorf("update: parse checksums: %w", err)
	}

	fmt.Fprint(o.out, "==> Verifying SHA-256... ")
	if err := update.VerifySHA256(tgz, digest); err != nil {
		fmt.Fprintln(o.out, "FAIL")
		return fmt.Errorf("update: verify checksum: %w", err)
	}
	fmt.Fprintln(o.out, "ok")

	bin, err := update.ExtractBinary(tgz, "pyry")
	if err != nil {
		return fmt.Errorf("update: extract binary: %w", err)
	}

	fmt.Fprintf(o.out, "==> Replacing %s...\n", target)
	current, err := os.ReadFile(target)
	switch {
	case err == nil && bytes.Equal(current, bin):
		// Already installed, by an earlier run or another daemon sharing this
		// binary. Writing again would overwrite pyry.prev with the new build and
		// lose the rollback copy.
		return nil
	case err == nil:
		if err := keepPrevious(o, target, current); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("update: read current binary: %w", err)
	}
	if err := o.replace(target, bin, 0o755); err != nil {
		return fmt.Errorf("update: replace binary: %w", err)
	}
	return nil
}

// keepPrevious saves the binary about to be replaced as pyry.prev beside it,
// the name and place `make rollback` restores from. It runs only after the
// signature and checksum gates have passed, so a failed verification leaves
// any existing pyry.prev untouched. An error aborts the install before the
// target is written: replacing without a rollback copy is not allowed.
func keepPrevious(o updateOptions, target string, current []byte) error {
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	prev := filepath.Join(filepath.Dir(target), "pyry.prev")
	if err := o.replace(prev, current, mode); err != nil {
		return fmt.Errorf("update: keep previous binary: %w", err)
	}
	return nil

}
