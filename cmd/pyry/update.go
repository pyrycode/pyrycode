package main

import (
	"context"
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

	return doUpdate(context.Background(), updateOptions{
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
		out:            os.Stdout,
		checkOnly:      *checkOnly,
		pinVersion:     *pinVersion,
		noRestart:      *noRestart,
		probeRestart:   defaultProbeRestart,
		runRestart:     defaultRunRestart,
	})
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
// resolver, the AtomicReplace function, the daemon-restart probe and
// executor, and stdout. Production callers pass real defaults; tests
// substitute httptest + tempdir equivalents.
type updateOptions struct {
	currentVersion string
	goos, goarch   string
	repo           string
	releaseBaseURL string
	fetcher        *update.Fetcher
	executablePath func() string
	replace        func(target string, data []byte, mode os.FileMode) error
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

	asset, err := update.AssetName(targetVer, o.goos, o.goarch)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	tarballURL := fmt.Sprintf("%s/%s/%s", o.releaseBaseURL, targetVer, asset)
	checksumsURL := fmt.Sprintf("%s/%s/checksums.txt", o.releaseBaseURL, targetVer)

	fmt.Fprintf(o.out, "==> Downloading %s...\n", asset)
	tgz, err := o.fetcher.FetchAsset(ctx, tarballURL)
	if err != nil {
		return fmt.Errorf("update: download tarball: %w", err)
	}

	sumsBytes, err := o.fetcher.FetchAsset(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("update: download checksums: %w", err)
	}
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
	if err := o.replace(target, bin, 0o755); err != nil {
		return fmt.Errorf("update: replace binary: %w", err)
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
