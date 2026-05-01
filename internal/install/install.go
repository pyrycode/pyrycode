// Package install writes service-manager unit files (systemd, launchd) for
// pyry. Used by the `pyry install-service` subcommand.
//
// The package never enables, starts, or otherwise activates the unit — it
// only writes the file and returns the path. The user runs the platform's
// own enable/start commands afterward, with the next-step instructions
// printed by the caller.
package install

import (
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Platform identifies which service manager the unit is for.
type Platform int

const (
	PlatformAuto Platform = iota
	PlatformSystemd
	PlatformLaunchd
)

func (p Platform) String() string {
	switch p {
	case PlatformSystemd:
		return "systemd"
	case PlatformLaunchd:
		return "launchd"
	default:
		return "auto"
	}
}

// Detect resolves PlatformAuto to the right platform for the running OS.
// PlatformSystemd and PlatformLaunchd are returned unchanged.
func (p Platform) Detect() Platform {
	if p != PlatformAuto {
		return p
	}
	switch runtime.GOOS {
	case "linux":
		return PlatformSystemd
	case "darwin":
		return PlatformLaunchd
	default:
		// Best-effort fallback. Caller will surface the error if the host
		// has no service manager we wrote a template for.
		return PlatformSystemd
	}
}

// Options for [Install].
type Options struct {
	// Platform picks the service manager. PlatformAuto detects from GOOS.
	Platform Platform

	// Name is the instance name. Defaults to "pyry". Used in the unit's
	// filename, label, and (when non-default) baked into ExecStart as
	// -pyry-name <name> so the supervisor binds to ~/.pyry/<name>.sock.
	Name string

	// WorkDir is what gets baked into WorkingDirectory. Defaults to
	// "%h/pyry-workspace" for systemd (resolved by systemd at runtime) and
	// "$HOME/pyry-workspace" expanded for launchd.
	WorkDir string

	// Binary is the absolute path to the pyry binary. Defaults to
	// os.Executable() — the binary currently running.
	Binary string

	// PathEnv is the PATH environment variable baked into the unit. Defaults
	// to the value of $PATH at install time (with $HOME/ rewritten to %h/
	// for systemd portability). Users typically don't set this — the
	// inherited PATH already covers nvm, pyenv, brew, and other shimmed
	// tools their interactive shell sees. Falls back to a conservative
	// system PATH if $PATH is unset.
	PathEnv string

	// ClaudeArgs, if non-empty, are baked into ExecStart after the pyry
	// flags. If empty, the unit is written with commented suggestions and
	// the user is expected to edit it before starting the service.
	ClaudeArgs []string

	// Force allows overwriting an existing file. Without it, [Install]
	// returns [ErrFileExists] if the destination is already a file.
	Force bool

	// HomeDir is the user's home directory. Defaults to os.UserHomeDir().
	// Exposed for testing.
	HomeDir string

	// EnvPath is the value of $PATH inherited from the install-time shell.
	// Defaults to os.Getenv("PATH"). Exposed for testing.
	EnvPath string
}

// ErrFileExists is returned when the unit file already exists and Force is
// false.
var ErrFileExists = errors.New("unit file already exists; pass --force to overwrite")

// Install writes the unit file and returns the absolute path it was written
// to plus the resolved Platform. If the destination already exists and
// Options.Force is false, returns [ErrFileExists] without touching it.
func Install(opt Options) (path string, plat Platform, err error) {
	plat = opt.Platform.Detect()

	if opt.Name == "" {
		opt.Name = "pyry"
	}

	if opt.HomeDir == "" {
		opt.HomeDir, err = os.UserHomeDir()
		if err != nil {
			return "", plat, fmt.Errorf("resolve home dir: %w", err)
		}
	}

	if opt.Binary == "" {
		opt.Binary, err = os.Executable()
		if err != nil {
			return "", plat, fmt.Errorf("resolve pyry binary path: %w", err)
		}
	}

	if opt.WorkDir == "" {
		switch plat {
		case PlatformSystemd:
			opt.WorkDir = "%h/pyry-workspace"
		default:
			opt.WorkDir = filepath.Join(opt.HomeDir, "pyry-workspace")
		}
	}

	if opt.PathEnv == "" {
		envPath := opt.EnvPath
		if envPath == "" {
			envPath = os.Getenv("PATH")
		}
		opt.PathEnv = derivePathEnv(plat, envPath, opt.HomeDir)
	}

	// Build the ExecStart args: [binary, optional -pyry-name name, ...claudeArgs].
	execArgs := []string{opt.Binary}
	if opt.Name != "pyry" {
		execArgs = append(execArgs, "-pyry-name", opt.Name)
	}
	execArgs = append(execArgs, opt.ClaudeArgs...)

	data := templateData{
		Name:          opt.Name,
		WorkDir:       opt.WorkDir,
		PathEnv:       opt.PathEnv,
		ExecArgs:      execArgs,
		HasClaudeArgs: len(opt.ClaudeArgs) > 0,
	}

	switch plat {
	case PlatformSystemd:
		path = filepath.Join(opt.HomeDir, ".config/systemd/user", opt.Name+".service")
	case PlatformLaunchd:
		path = filepath.Join(opt.HomeDir, "Library/LaunchAgents", "dev.pyrycode."+opt.Name+".plist")
	default:
		return "", plat, fmt.Errorf("unsupported platform: %s", plat)
	}

	if !opt.Force {
		if _, statErr := os.Stat(path); statErr == nil {
			return path, plat, ErrFileExists
		}
	}

	tmplName := templateNameFor(plat)
	tmpl, err := template.New(tmplName).Funcs(template.FuncMap{
		"joinShell": joinShell,
		"xmlEscape": xmlEscape,
	}).ParseFS(templates, "templates/"+tmplName)
	if err != nil {
		return "", plat, fmt.Errorf("parse template: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, plat, fmt.Errorf("create unit dir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return path, plat, fmt.Errorf("create unit file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return path, plat, fmt.Errorf("render template: %w", err)
	}

	return path, plat, nil
}

// templateData is the data passed to text/template.
type templateData struct {
	Name          string
	WorkDir       string
	PathEnv       string
	ExecArgs      []string
	HasClaudeArgs bool
}

func templateNameFor(p Platform) string {
	switch p {
	case PlatformSystemd:
		return "systemd.service.tmpl"
	case PlatformLaunchd:
		return "launchd.plist.tmpl"
	default:
		return ""
	}
}

// joinShell joins args with spaces, quoting any arg that contains whitespace,
// quotes, or other shell-significant characters. Used for systemd's ExecStart
// line, which uses POSIX-shell-like word splitting.
func joinShell(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

// shellQuote returns a single-quoted form of s that's safe to paste into a
// systemd ExecStart= line. If s has no characters needing quoting, it's
// returned as-is.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !needsShellQuote(s) {
		return s
	}
	// Single-quote and escape any embedded single quotes by closing the
	// quote, inserting an escaped quote, and reopening: '\''.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsShellQuote(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == '/', r == ':', r == '=', r == '@', r == '+', r == '%':
			continue
		default:
			return true
		}
	}
	return false
}

// derivePathEnv returns the PATH string to bake into the unit. If envPath is
// non-empty, it's used (with $HOME/ rewritten to %h/ for systemd portability,
// and stripped to a conservative system PATH if there's nothing usable).
// Otherwise falls back to a hardcoded conservative default.
//
// Empty entries (`::`) and duplicates are stripped. The caller can split the
// result on `:` to display entries to the user.
func derivePathEnv(plat Platform, envPath, homeDir string) string {
	if envPath == "" {
		switch plat {
		case PlatformSystemd:
			return "%h/.local/bin:/usr/local/bin:/usr/bin:/bin"
		default:
			return filepath.Join(homeDir, ".local/bin") + ":/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
		}
	}

	homePrefix := homeDir
	if !strings.HasSuffix(homePrefix, "/") {
		homePrefix += "/"
	}

	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, entry := range strings.Split(envPath, ":") {
		if entry == "" {
			continue
		}
		// Replace $HOME/ prefix with %h/ for systemd portability. For
		// launchd the literal absolute path is what we want.
		if plat == PlatformSystemd && homePrefix != "/" && strings.HasPrefix(entry, homePrefix) {
			entry = "%h/" + strings.TrimPrefix(entry, homePrefix)
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		out = append(out, entry)
	}
	if len(out) == 0 {
		// Pathological PATH (only colons / duplicates of empty). Fall back.
		return derivePathEnv(plat, "", homeDir)
	}
	return strings.Join(out, ":")
}

// xmlEscape escapes a string for XML attribute / element content.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// Should never fail for an in-memory writer.
		return s
	}
	return b.String()
}
