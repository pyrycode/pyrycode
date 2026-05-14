package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// authGate builds the dispatcher's FirstFrame closure that bridges
// dispatch.FirstFrameGate and relay.AuthenticateFirstFrame. The token
// is read from env.Token (relay-prepended on the first phone→binary
// frame); the gate never logs the token, never wraps it into an error,
// and never echoes it.
func authGate(registry *devices.Registry, serverID string, logger *slog.Logger) dispatch.FirstFrameGate {
	return func(ctx context.Context, env protocol.RoutingEnvelope) dispatch.FirstFrameOutcome {
		outcome, err := relay.AuthenticateFirstFrame(env, env.Token, registry, serverID, logger)
		if err != nil {
			// Today only ErrMalformedHelloFrame is reachable. Surface to
			// the dispatcher's malformed-frame fall-through.
			return dispatch.FirstFrameOutcome{Err: err}
		}
		out := dispatch.FirstFrameOutcome{
			Response: outcome.Response,
			Device:   outcome.Device, // nil on reject; populated on accept
		}
		if outcome.CloseConn {
			out.CloseConn = true
			out.Code = uint16(relay.StatusUnauthorized) // 4401
		}
		return out
	}
}

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
	convReg *conversations.Registry,
	sess handlers.TurnWriter,
	sup *supervisor.Supervisor,
	bridge *supervisor.Bridge,
) (cleanup func(), err error) {
	if relayURL == "" {
		logger.Info("relay: disabled (no URL configured)")
		return func() {}, nil
	}

	serverID, err := identity.LoadOrCreate(resolveServerIDPath(instanceName))
	if err != nil {
		return nil, fmt.Errorf("load server-id: %w", err)
	}

	// Load the device registry once at daemon startup. A missing file
	// (ENOENT) yields an empty registry — every phone rejects until
	// `pyry pair` runs. Malformed JSON fails fast.
	registry, err := devices.Load(resolveDevicesPath(instanceName))
	if err != nil {
		return nil, fmt.Errorf("load device registry: %w", err)
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

	d := dispatch.New(dispatch.Config{
		Frames:     conn.Frames(),
		Logger:     logger,
		FirstFrame: authGate(registry, string(serverID), logger),
	})
	d.Register(protocol.TypeListConversations, handlers.ListConversations(convReg))
	d.Register(protocol.TypeRegisterPushToken, handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger))
	d.Register(protocol.TypeSendMessage, handlers.SendMessage(sess, logger))

	// The assistant-turn bridge taps Bridge.Write so PTY chunks fan out
	// to every active phone conn as a `message` envelope (#311). Skip in
	// foreground mode (bridge == nil) — there is no PTY-output observer
	// surface in that path; inbound `send_message` still works.
	var bridgeCleanup func()
	if bridge != nil {
		bridgeCleanup = startAssistantTurnBridge(ctx, sup, bridge, d, logger)
	}

	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		if err := d.Run(ctx); err != nil {
			logger.Debug("relay: dispatcher run returned", "err", err)
		}
	}()

	forwarderDone := make(chan struct{})
	go func() {
		defer close(forwarderDone)
		for env := range d.Outbound() {
			if err := conn.Send(env); err != nil {
				// Transport-internal reconnect handles transient drops;
				// a Send error here means the conn is currently dropped
				// or closed. We log and continue draining so the
				// dispatcher's Outbound close still unblocks Run.
				logger.Debug("relay: outbound forward dropped",
					"conn_id", env.ConnID, "err", err)
			}
		}
	}()

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
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
		// Stop the assistant-turn observer first so no new PTY chunks
		// queue while the dispatcher is winding down. The cleanup waits
		// for the emitter goroutine on ctx-cancel.
		if bridgeCleanup != nil {
			bridgeCleanup()
		}
		_ = conn.Close()
		// Order: Connection.run defers close(frames) → dispatcher.Run
		// returns (Frames closed) → dispatcher closes Outbound → forwarder
		// exits. Wait returns once Connection.run completes.
		<-dispatcherDone
		<-forwarderDone
		<-waitDone
	}
	return cleanup, nil
}
