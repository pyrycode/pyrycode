package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// resolveRelayURL returns the first non-empty value among:
//  1. flagValue (from -pyry-relay)
//  2. envValue  (from PYRY_RELAY_URL)
//  3. cfg.RelayURL (from ~/.pyry/config.json — config.Load already
//     overlays DefaultConfig, so this leg covers both the operator
//     file and the built-in default)
//
// Returns "" only if all three are empty (config.Load's overlay makes
// that effectively unreachable in production).
func resolveRelayURL(flagValue, envValue string, cfg config.Config) string {
	if flagValue != "" {
		return flagValue
	}
	if envValue != "" {
		return envValue
	}
	return cfg.RelayURL
}

// startRelay opens the binary↔relay leg in a supervisor-owned goroutine.
// Returns a no-op cleanup and nil err when relayURL is empty (relay
// disabled — see operator note below). Otherwise loads the server-id,
// calls relay.Connect, and spawns one goroutine that:
//
//   - drains conn.Frames() (the dispatcher slice consumes them later)
//   - blocks on conn.Wait()
//   - on relay.ErrServerIDConflict: logs the conflict and calls shutdown()
//     to unwind the daemon (AC#3: no reconnect-loop on 4409)
//   - on any other terminal error: logs at warn (transport-internal
//     reconnect already handled non-fatal closes; reaching this path
//     means a genuinely unrecoverable transport error surfaced)
//   - on ctx.Err: logs at debug; returns
//
// The returned cleanup func is idempotent: it Close()s the connection
// and waits for the goroutine to drain so the daemon process does not
// exit while a WS handle is still in flight.
//
// startRelay does NOT swallow relay.Connect's synchronous errors
// (invalid scheme, missing identity). Those are programmer/config errors
// that should surface as a daemon startup failure — return wrapped, let
// runSupervisor fail fast. Lifecycle errors (post-Connect) flow through
// the goroutine.
func startRelay(
	ctx context.Context,
	logger *slog.Logger,
	instanceName, relayURL, version string,
	allowInsecure bool,
	shutdown context.CancelFunc,
) (cleanup func(), err error) {
	if relayURL == "" {
		logger.Info("relay: disabled (no URL configured)")
		return func() {}, nil
	}

	serverID, err := identity.LoadOrCreate(resolveServerIDPath(instanceName))
	if err != nil {
		return nil, fmt.Errorf("load server-id: %w", err)
	}

	if allowInsecure {
		logger.Info("relay: PYRY_ALLOW_INSECURE_RELAY=1 — accepting ws:// scheme")
	}
	logger.Info("relay: connecting", "url", relayURL, "server_id", string(serverID))

	conn, err := relay.Connect(ctx, relay.Config{
		ServerID:            serverID,
		RelayURL:            relayURL,
		BinaryVersion:       version,
		Logger:              logger,
		AllowInsecureScheme: allowInsecure,
	})
	if err != nil {
		return nil, fmt.Errorf("relay connect: %w", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Drain Frames() until the lifecycle closes it. The dispatcher
		// slice replaces this discard with real handling.
		for range conn.Frames() {
		}
		// Frames closing means Wait is about to return — Connection.run
		// defers close(c.done) then close(c.frames), so the range exit
		// is the safe signal that Wait will not block beyond the next
		// statement.
		err := conn.Wait()
		switch {
		case errors.Is(err, relay.ErrServerIDConflict):
			logger.Error("relay: server-id conflict; shutting down daemon",
				"server_id", string(serverID), "err", err)
			shutdown()
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			logger.Debug("relay: lifecycle ended via ctx cancel", "err", err)
		case err != nil:
			logger.Warn("relay: lifecycle ended with terminal error", "err", err)
		default:
			logger.Debug("relay: lifecycle ended cleanly")
		}
	}()

	cleanup = func() {
		_ = conn.Close()
		<-done
	}
	return cleanup, nil
}
