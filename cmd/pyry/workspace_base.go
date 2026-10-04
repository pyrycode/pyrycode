package main

import (
	"log/slog"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/relay"
)

// resolveStartupWorkspaceBase selects the service process folder independently
// of the Claude spawn override. It only resolves paths; the channel creator
// still owns creation, confinement and trust marking when seeding.
func resolveStartupWorkspaceBase(getwd func() (string, error), logger *slog.Logger) string {
	fallback := relay.WorkspaceRoot()
	if fallback != "" {
		cwd, err := getwd()
		if err == nil && filepath.IsAbs(cwd) {
			if real, err := confineWorkdirToHome(cwd); err == nil {
				return real
			}
		}
	}
	// Neither resolution errors nor path-derived attributes belong in this
	// startup event: workspace metadata is disclosed only to admitted peers.
	logger.Warn("workspace base: using startup fallback", "event", "workspace_base.fallback")
	return fallback
}
