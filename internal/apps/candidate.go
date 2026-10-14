package apps

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Toolchain is daemon-selected: Node is an absolute executable and NPM is the
// absolute npm CLI JavaScript entry, executed directly by that Node.
type Toolchain struct{ Node, NPM string }

// CandidateFailure contains static, actionable feedback without child diagnostics.
type CandidateFailure struct{ Phase, Reason string }

func (f *CandidateFailure) Error() string { return "apps: " + f.Phase + ": " + f.Reason }

// PrepareCandidate checks and records immutable bytes; it neither activates a
// service nor prepares persistent data. Trusted app commands are not sandboxed.
func (r *Registry) PrepareCandidate(ctx context.Context, serverID, appID string, tools Toolchain) (release Release, err error) {
	phase := "admission"
	reasons := map[string]string{
		"admission":  "Use a live registration on this host.",
		"staging":    "Remove unsafe source entries and check private staging access.",
		"validation": "Use Node 24.15.0 or newer within 24.x, a matching manifest and consistent module package/lock metadata.",
		"install":    "Check the lockfile and npm ci dependencies, then retry.",
		"typecheck":  "Fix frontend and service type errors, then retry.",
		"test":       "Fix the app tests, then retry.",
		"build":      "Fix the app compilation, then retry.",
		"artifacts":  "Check compiled entries, public assets and retained dependencies, then retry.",
		"record":     "Check retained build integrity, use a new increasing version for changed bytes and check registry storage access.",
	}
	defer func() {
		if err != nil {
			release = Release{}
			reason := reasons[phase]
			if ctx.Err() != nil {
				reason = "Preparation cancelled; retry when ready."
			}
			err = &CandidateFailure{Phase: phase, Reason: reason}
		}
	}()
	source, err := r.Inspect(ctx, serverID, appID)
	if err != nil {
		return release, err
	}
	phase = "staging"
	stage, cleanup, err := candidateStaging(ctx, source.SourcePath)
	if err != nil {
		return release, err
	}
	defer cleanup()
	phase = "validation"
	if !filepath.IsAbs(tools.Node) || !filepath.IsAbs(tools.NPM) {
		return release, ErrInvalidStorage
	}
	env, cleanupEnvironment, err := candidateEnvironment(stage, tools.Node)
	if err != nil {
		return release, err
	}
	defer func() { err = errors.Join(err, cleanupEnvironment()) }()
	versionCtx, cancel := context.WithTimeout(ctx, candidateVersionTimeout)
	defer cancel()
	node, err := candidateCommand(versionCtx, stage, env, tools.Node, []string{"--version"}, true)
	if err != nil || !supportedNode(node) {
		return release, ErrInvalidManifest
	}
	npm, err := candidateCommand(versionCtx, stage, env, tools.Node, []string{tools.NPM, "--version"}, true)
	if err != nil || !validRelease(npm) {
		return release, ErrInvalidManifest
	}
	manifest, err := candidateInputs(stage, serverID, appID)
	if err != nil {
		return release, err
	}
	for _, step := range []struct {
		phase string
		args  []string
	}{
		{"install", []string{"ci"}}, {"typecheck", []string{"run", "typecheck"}}, {"test", []string{"test"}}, {"build", []string{"run", "build"}},
	} {
		phase = step.phase
		if _, err = candidateCommand(ctx, stage, env, tools.Node, append([]string{tools.NPM}, step.args...), false); err != nil {
			return release, err
		}
	}
	phase = "artifacts"
	completed, err := candidateInputs(stage, serverID, appID)
	if err != nil {
		return release, err
	}
	if completed != manifest {
		return release, ErrConflict
	}
	build, err := ValidateArtifacts(ctx, stage, serverID, appID)
	if err != nil {
		return release, err
	}
	if build.Manifest != manifest {
		return release, ErrConflict
	}
	phase = "record"
	return r.recordCandidate(ctx, serverID, appID, stage, build, strings.TrimPrefix(node, "v"), npm)
}

func candidateEnvironment(stage, node string) ([]string, func() error, error) {
	for _, n := range []string{".tools", ".home", ".cache"} {
		if err := os.Mkdir(filepath.Join(stage, n), 0700); err != nil {
			return nil, nil, err
		}
	}
	if err := os.Symlink(node, filepath.Join(stage, ".tools/node")); err != nil {
		return nil, nil, err
	}
	userConfig := filepath.Join(stage, ".home/user.npmrc")
	globalConfig := filepath.Join(stage, ".home/global.npmrc")
	for _, path := range []string{userConfig, globalConfig} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			return nil, nil, err
		}
	}
	// npm prepends dependency bins before invoking its lifecycle shell. Restore
	// the selected Node's precedence for commands and env-node shebangs, including
	// dependency install hooks, while retaining lookup of other package binaries.
	shell := filepath.Join(stage, ".tools/script-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nPATH=\"${0%/*}:$PATH\"\nexport PATH\nexec /bin/sh \"$@\"\n"), 0700); err != nil {
		return nil, nil, err
	}
	// Independent service test data must remain outside app trees, even when the
	// daemon's TMPDIR points into one. /tmp is available on supported Linux/macOS.
	tmp, err := candidateTemporary("/tmp")
	if err != nil {
		return nil, nil, err
	}
	return []string{
		"PATH=" + filepath.Join(stage, ".tools") + ":/usr/bin:/bin",
		"HOME=" + filepath.Join(stage, ".home"),
		"TMPDIR=" + tmp,
		"npm_config_cache=" + filepath.Join(stage, ".cache"),
		"npm_config_userconfig=" + userConfig,
		"npm_config_globalconfig=" + globalConfig,
		"npm_config_script_shell=" + shell,
		"npm_config_update_notifier=false",
	}, func() error { return os.RemoveAll(tmp) }, nil
}

func candidateTemporary(parent string) (string, error) {
	path, err := os.MkdirTemp(parent, "pyry-app-check-")
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		for _, location := range []string{path, resolved} {
			for _, part := range strings.Split(location, string(filepath.Separator)) {
				if part == "source" || part == "staging" || part == "builds" || part == "dist" {
					err = ErrInvalidStorage
				}
			}
		}
	}
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(path))
	}
	return resolved, nil
}
func candidateRead(stage, name string) ([]byte, error) {
	dir, err := privateDir(stage)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := regularAt(dir, name, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
