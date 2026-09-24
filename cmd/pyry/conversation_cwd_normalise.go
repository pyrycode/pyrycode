package main

import (
	"log/slog"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// normaliseLegacyCwds rewrites, once at startup, every stored conversation Cwd
// that is relative or "~"-prefixed to the realpath form create_conversation has
// recorded since #2568, rekeying any workspace label stored under it, and saves
// the registry when anything changed. Before #2568 create recorded the client's
// spelling of a folder, so "default", "~/pyry-workspace/default" and its absolute
// form sat in the registry as three workspaces for one folder.
//
// Only non-absolute, non-empty values are candidates: an absolute row is never
// touched, whether or not it still resolves, and an empty Cwd names no folder
// anyone asked for. A relative value resolves against the daemon's process cwd,
// the base create resolved it against.
//
// resolve is resolveWorkspaceDir in production — tilde expansion plus the STRICT
// $HOME confinement, which neither creates a folder nor trust-marks one, so a
// hostile or stale stored value cannot make startup create or auto-trust
// anything. For an existing folder it yields the same realpath create records.
//
// A value that fails to resolve (the folder is gone, or it escapes $HOME) is left
// exactly as stored — every later spawn from it re-validates anyway — and logged
// by a static event with a row count. Never the path and never the error: the
// confine error names the path, and a workspace path is the operator's
// filesystem layout. The daemon starts whatever happens here; a failed Save is
// logged and the in-memory rewrite is persisted by the next one.
func normaliseLegacyCwds(reg *conversations.Registry, registryPath string, resolve func(string) (string, error), logger *slog.Logger) {
	rows := make(map[string]int)
	for _, c := range reg.List() {
		if c.Cwd != "" && !filepath.IsAbs(c.Cwd) {
			rows[c.Cwd]++
		}
	}
	rekey := make(map[string]string, len(rows))
	for cwd, n := range rows {
		resolved, err := resolve(cwd)
		if err != nil {
			logger.Warn("conversations: legacy cwd left as stored",
				"event", "conversations.legacy_cwd_unresolved",
				"rows", n)
			continue
		}
		rekey[cwd] = resolved
	}
	n := reg.RekeyCwds(rekey)
	if n == 0 {
		return
	}
	if err := reg.Save(registryPath); err != nil {
		logger.Error("conversations: saving normalised cwds failed",
			"event", "conversations.legacy_cwd_save_failed",
			"err", err)
		return
	}
	logger.Info("conversations: legacy cwds normalised",
		"event", "conversations.legacy_cwd_normalised",
		"rows", n)
}
