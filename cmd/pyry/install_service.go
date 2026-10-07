package main

import (
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"strings"

	"github.com/pyrycode/pyrycode/internal/install"
)

// runInstallService implements `pyry install-service`: write a systemd unit
// (Linux) or launchd plist (macOS) for pyry, ready to enable. The user's
// claude flags are split off after `--` and baked into ExecStart; without
// them, the unit is written as a documented template the user edits before
// enabling.
func runInstallService(args []string) error {
	// Split pyry-side flags from claude flags at the first "--".
	var pyrySide, claudeSide []string
	for i, a := range args {
		if a == "--" {
			pyrySide = args[:i]
			claudeSide = args[i+1:]
			break
		}
	}
	if pyrySide == nil {
		pyrySide = args
	}

	fs := flag.NewFlagSet("pyry install-service", flag.ContinueOnError)
	name := fs.String("pyry-name", defaultName(), "instance name (filename + ExecStart suffix)")
	systemdFlag := fs.Bool("systemd", false, "force systemd output (default: detect from OS)")
	launchdFlag := fs.Bool("launchd", false, "force launchd output (default: detect from OS)")
	workdir := fs.String("workdir", "", "WorkingDirectory baked into the unit (default: current directory)")
	pathEnv := fs.String("path", "", "PATH baked into the unit (default: inherit your current shell's PATH)")
	force := fs.Bool("force", false, "overwrite an existing unit file")
	if err := fs.Parse(pyrySide); err != nil {
		return err
	}
	if *systemdFlag && *launchdFlag {
		return fmt.Errorf("--systemd and --launchd are mutually exclusive")
	}
	plat := install.PlatformAuto
	switch {
	case *systemdFlag:
		plat = install.PlatformSystemd
	case *launchdFlag:
		plat = install.PlatformLaunchd
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("install-service: get cwd: %w", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("install-service: home dir: %w", err)
	}
	resolvedWorkDir, err := install.ResolveWorkDir(*workdir, cwd, homeDir)
	if err != nil {
		return fmt.Errorf("install-service: resolve workdir: %w", err)
	}

	fmt.Printf("WorkingDirectory: %s\n", resolvedWorkDir)
	if _, err := os.Stat(resolvedWorkDir); errors.Is(err, iofs.ErrNotExist) {
		fmt.Printf("warning: %s does not exist; create it with: mkdir -p %s\n",
			resolvedWorkDir, resolvedWorkDir)
	}

	path, resolved, err := install.Install(install.Options{
		Platform:   plat,
		Name:       *name,
		WorkDir:    resolvedWorkDir,
		PathEnv:    *pathEnv,
		ClaudeArgs: claudeSide,
		Force:      *force,
	})
	if err != nil {
		if errors.Is(err, install.ErrFileExists) {
			return fmt.Errorf("%w: %s", err, path)
		}
		return fmt.Errorf("install-service: %w", err)
	}

	fmt.Printf("Wrote %s (%s)\n\n", path, resolved)
	if *pathEnv == "" {
		// Tell the user what we inherited so surprises surface here, not
		// after a service starts and a hook silently fails.
		fmt.Printf("Inherited PATH from current shell (review with: systemctl --user cat %s):\n", *name)
		for _, entry := range strings.Split(os.Getenv("PATH"), ":") {
			if entry == "" {
				continue
			}
			fmt.Printf("    %s\n", entry)
		}
		fmt.Println()
	}
	switch resolved {
	case install.PlatformSystemd:
		if len(claudeSide) == 0 {
			fmt.Printf("Edit ExecStart for your claude flags, then:\n")
		} else {
			fmt.Printf("Next:\n")
		}
		fmt.Printf("    systemctl --user daemon-reload\n")
		fmt.Printf("    systemctl --user enable --now %s\n\n", *name)
		fmt.Printf("Verify with:\n")
		fmt.Printf("    pyry status\n")
		fmt.Printf("    pyry logs\n")
		fmt.Printf("    journalctl --user -u %s -f\n\n", *name)
		fmt.Printf("For boot-before-login persistence: sudo loginctl enable-linger $USER\n")
	case install.PlatformLaunchd:
		if len(claudeSide) == 0 {
			fmt.Printf("Edit ProgramArguments for your claude flags, then:\n")
		} else {
			fmt.Printf("Next:\n")
		}
		fmt.Printf("    launchctl load %s\n\n", path)
		fmt.Printf("Verify with:\n")
		fmt.Printf("    pyry status\n")
		fmt.Printf("    pyry logs\n")
		fmt.Printf("    tail -f /tmp/pyry.%s.{out,err}.log\n", *name)
	}
	return nil
}
