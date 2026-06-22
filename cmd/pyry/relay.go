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
	"github.com/pyrycode/pyrycode/internal/keys"
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
	allowInsecure, v2Enabled bool,
	shutdown context.CancelFunc,
	convReg *conversations.Registry,
	creator handlers.SessionCreator,
	router handlers.SessionRouter,
	active *activeConversation,
	boundHost boundHostFunc,
	sup *supervisor.Supervisor,
	bridge *supervisor.Bridge,
	claudeSessionsDir string,
	defaultCwd string,
	transitions transitionObserverSink,
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

	// legCleanup tears down the protocol-specific consumers of conn.Frames()
	// (the v1 dispatcher path or the v2 Noise manager); the shared waitDone
	// classifier below is appended to it. Exactly one leg consumes the frame
	// stream — there is no mixed-mode path (ADR 024: v2 is a hard cutover).
	var legCleanup func()

	if v2Enabled {
		logger.Info("relay: PYRY_MOBILE_V2=1 — Mobile Protocol v2 (Noise_IK) cutover enabled")
		drain, err := startRelayV2(ctx, logger, instanceName, conn, registry, serverID, convReg, creator, router, active, boundHost, sup, bridge, claudeSessionsDir, defaultCwd, transitions)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		legCleanup = func() {
			// Close the connection first so Connection.run closes Frames,
			// which unblocks the manager's Run; drain then waits for it.
			_ = conn.Close()
			drain()
		}
	} else {
		d := dispatch.New(dispatch.Config{
			Frames:     conn.Frames(),
			Logger:     logger,
			FirstFrame: authGate(registry, string(serverID), logger),
		})
		d.Register(protocol.TypeListConversations, handlers.ListConversations(convReg))
		d.Register(protocol.TypeCreateConversation, handlers.CreateConversation(convReg, creator, resolveConversationsRegistryPath(instanceName), defaultCwd, logger))
		d.Register(protocol.TypeRegisterPushToken, handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger))
		d.Register(protocol.TypeSendMessage, handlers.SendMessage(router, logger))

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

		legCleanup = func() {
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
		}
	}

	// The conn.Wait() classifier is identical for both legs — a 4409
	// server-id conflict unwinds the daemon (no reconnect loop); ctx-cancel
	// is the clean-shutdown path; any other terminal error is logged at warn.
	// Shared after the branch so the v2 leg inherits the contract unchanged.
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
		legCleanup()
		<-waitDone
	}
	return cleanup, nil
}

// startRelayV2 wires the Mobile Protocol v2 (Noise_IK E2E) dispatch leg: it
// loads the binary's persistent static keypair, builds a V2SessionManager
// against conn.Frames() registering the same three relay handlers as the v1
// path, and runs the manager in one goroutine. The returned drain func blocks
// until that goroutine has exited; the caller Close()s conn before calling
// drain so the manager's Run unblocks on the closed Frames channel.
//
// The static key is loaded with the same (baseDir, sanitizeName(name)) pair
// `pyry pair` uses, so the loaded private key derives the public key the phone
// pinned at pairing. On error the leg fails fast at startup, mirroring the
// identity.LoadOrCreate / devices.Load posture in startRelay's prologue.
//
// The structured interactive turn stream (#633) wires the #615 producer to the
// #632 capability-gated emitter, fanning turn_state / assistant_delta / tool /
// turn_end envelopes to interactive phones. It is gated on bridge != nil
// (foreground has no PTY-output observer surface) plus a non-empty
// claudeSessionsDir (the dir the rotation-following JSONL resolver scans; ""
// already disables reconcile + the rotation watcher, so disabling the producer
// too is coherent).
//
// SECURITY: StaticPriv is the binary's 32-byte X25519 static secret. It is
// passed to the manager as an opaque slice and is never logged, wrapped into
// an error, or emitted on any wire surface here — the same contract
// internal/keys and internal/noise enforce for these bytes.
func startRelayV2(
	ctx context.Context,
	logger *slog.Logger,
	instanceName string,
	conn *relay.Connection,
	registry *devices.Registry,
	serverID identity.ServerID,
	convReg *conversations.Registry,
	creator handlers.SessionCreator,
	router handlers.SessionRouter,
	active *activeConversation,
	boundHost boundHostFunc,
	sup *supervisor.Supervisor,
	bridge *supervisor.Bridge,
	claudeSessionsDir string,
	defaultCwd string,
	transitions transitionObserverSink,
) (drain func(), err error) {
	staticKey, err := keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(instanceName))
	if err != nil {
		return nil, fmt.Errorf("load static key: %w", err)
	}
	priv := staticKey.PrivateKey()

	mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
		Frames:     conn.Frames(),
		Outbound:   conn.Send,
		StaticPriv: priv[:],
		Devices:    registry,
		ServerID:   string(serverID),
		Logger:     logger,
		Handlers: map[string]dispatch.Handler{
			protocol.TypeListConversations:  handlers.ListConversations(convReg),
			protocol.TypeCreateConversation: handlers.CreateConversation(convReg, creator, resolveConversationsRegistryPath(instanceName), defaultCwd, logger),
			protocol.TypeRegisterPushToken:  handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger),
			protocol.TypeSendMessage:        handlers.SendMessage(router, logger),
		},
		// Screen-snapshot seam (#618): the supervisor renders the live screen
		// inside the tui-driver seal; KnownConversation gates request_snapshot
		// on registry membership (AC #4), mirroring the established
		// conversations-registry validation pattern but returning a bool so the
		// relay needs no conversations import or errors.Is coupling.
		Snapshotter: sup,
		KnownConversation: func(id string) bool {
			_, ok := convReg.Get(conversations.ConversationID(id))
			return ok
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build v2 session manager: %w", err)
	}

	mgrDone := make(chan struct{})
	go func() {
		defer close(mgrDone)
		if err := mgr.Run(ctx); err != nil {
			logger.Debug("relay: v2 manager run returned", "err", err)
		}
	}()

	// Wire the structured interactive turn stream (#633): the #615 producer over
	// Supervisor.Session() + the rotation-following JSONL resolver, bridged to the
	// #632 capability-gated emitter over mgr. Gated on bridge != nil
	// (foreground has no phone-mirroring surface) plus a resolvable sessions dir
	// (an empty dir would make the resolver perpetually error and Warn-spam every
	// retry; "" already disables reconcile + the rotation watcher).
	var streamCleanup func()
	if bridge != nil && claudeSessionsDir != "" {
		streamCleanup = startInteractiveTurnStreamV2(ctx, sup, active, boundHost, mgr, claudeSessionsDir, logger)
	} else if bridge != nil {
		logger.Info("relay: interactive turn stream disabled; claude sessions dir unresolved",
			"event", "interactive_turn_stream.no_sessions_dir")
	}

	// Wire the session-transition producer (#657): install #659's pool-side
	// observer and fan a session_transition envelope to capability-gated
	// interactive phones on each /clear rotation or idle/cap eviction. Unlike the
	// coarse bridge and the structured turn stream above, this has NO PTY
	// dependency — it consumes pool transitions, which fire in any mode — so it is
	// wired unconditionally whenever the v2 manager exists (no bridge != nil
	// gate). The capability filter (ActiveConns → Interactive) is the real
	// delivery gate: with no interactive phone connected, the fan-out reaches
	// nobody.
	streamTransitionsCleanup := startSessionTransitionStreamV2(ctx, transitions, mgr, logger)

	return func() {
		// Stop both producers — the structured turn stream and the session-
		// transition producer — before waiting on the manager so no fan-out races
		// a winding-down manager. Each cleanup waits for its goroutine on
		// ctx-cancel (already cancelled by the time drain runs). Then wait for the
		// manager's Run to exit on the closed Frames channel.
		if streamCleanup != nil {
			streamCleanup()
		}
		streamTransitionsCleanup()
		<-mgrDone
	}, nil
}
