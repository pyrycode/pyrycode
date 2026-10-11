# Draining turnevents into the interactive emitter (#1098)

The turn I/O parser (#1088) emits neutral `turnevent.Event`s from its sink callback, but that sink is
fixed where the runner is **constructed** (`newStreamRunnerFactory`), which sees only a
`supervisor.Config` — no handle on `interactiveTurnEmitterV2`, which is built later and separately on the
relay leg. `cmd/pyry/stream_turn_drain.go` lines the two lifetimes up with a late-bound, daemon-singleton
fan-in, and reproduces the PTY path's `startInteractiveTurnStreamV2` `OnEvent`/`FlushSignal`/`OnFlush`
triple **without `turnbridge`** — the Parser already emits `turnevent.Event`, so there is nothing to
un-map back to a `tuidriver.Event` for `turnbridge` to re-map.

| Section | Contents |
|---|---|
| [Fan-in and interactive emission](streamsup-package-draining-turnevents-into-the-interactive-emitter-fan-in.md) | Source capture, per-source emitter state, lifecycle ordering and drain publication. |
| [Daemon-retained live state](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md) | Stream, control and on-demand readings, prompt answerability, generations, revisions and cached provenance. |
| [Native reply suggestions after the result](streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md) | Eligibility, retention before publication, fallback isolation, cancellation and evidence. |
