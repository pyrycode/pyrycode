package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// openDaemonApps owns one instance's channel-independent registrations. A failed
// open exposes no registry and logs only a static reason, never storage errors.
func openDaemonApps(name string, logger *slog.Logger) *apps.Registry {
	unavailable := func(reason string) *apps.Registry { logger.Warn("apps: unavailable", "reason", reason); return nil }
	root, err := filepath.Abs(filepath.Join(resolveInstanceDirPath(name), "apps"))
	if err != nil {
		return unavailable("root_resolution_failed")
	}
	serverID, err := identity.LoadOrCreate(resolveServerIDPath(name))
	if err != nil {
		return unavailable("host_identity_unavailable")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return unavailable("root_unavailable")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return unavailable("root_not_directory")
	}
	if err := os.Chmod(root, 0700); err != nil {
		return unavailable("root_permissions_failed")
	}
	reg, err := apps.Open(root, string(serverID))
	if err != nil {
		return unavailable("storage_open_failed")
	}
	return reg
}

// newDaemonAppsOwner reconciles committed releases independently of channels.
// Storage or runtime unavailability never prevents normal daemon startup.
func newDaemonAppsOwner(reg *apps.Registry, node string, logger *slog.Logger) *apps.Owner {
	if reg == nil {
		return nil
	}
	owner, err := apps.NewOwner(reg, node)
	if err != nil {
		logger.Warn("apps: unavailable", "reason", "owner_construction_failed")
		return nil
	}
	return owner
}

// newDaemonAppsManager attaches only fully initialized storage to a valid
// manager. HostedAppsReady remains the caller's complete hosting attestation;
// registration and cancellable delivery alone establish no service readiness.
func newDaemonAppsManager(cfg relay.V2SessionConfig, reg *apps.Registry) (*relay.V2SessionManager, func(), error) {
	if reg != nil {
		cfg.HostedAppsDiscovery = reg
		cfg.HostedAppsSend = cfg.OutboundContext
	}
	mgr, err := relay.NewV2SessionManager(cfg)
	if err != nil {
		return nil, nil, err
	}
	detach := func() {}
	if reg != nil {
		detach = reg.Subscribe(mgr.HostedAppsChanged)
	}
	return mgr, detach, nil
}
