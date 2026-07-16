# Spec #969 — `make e2e-install` / `make e2e-update` targets for the orphaned opt-in suites

**Size:** XS (PO-sized `size:xs`; confirmed). Two `.PHONY` Makefile targets + one doc
section. **Zero production Go files** touched. Not `security-sensitive` (label absent;
a target that merely *runs* an existing opt-in suite introduces no design surface —
the suites already exist and are unchanged).

## Files to read first

- `Makefile:15-33` — the `check`/`preship` header block and the standing note
  *"e2e_install and e2e_update stay separate opt-in tags … deliberately not part of
  preship."* AC3 requires this comment be **preserved verbatim**.
- `Makefile:55-65` — the `e2e-realclaude` and `e2e-liverelay` targets. **These are the
  exact template.** Copy their shape: a comment block, `.PHONY: <name>`, then a
  one-line `$(GO) test -tags <tag> <pkg>` recipe. Note the style is bare — **no**
  `-race`, **no** `-count=1` (unlike the `e2e` target at :51-53). Match that.
- `Makefile:67-73` — `preship: check e2e-realclaude e2e-liverelay`. The new targets are
  **NOT** added here or to `check` (:40-41). Leaving them out is the whole point (AC3).
- `internal/e2e/install_darwin_test.go:1` / `install_linux_test.go:1` — build tags
  `darwin && e2e_install` and `linux && e2e_install`. The OS gate means only the host's
  install test compiles under one `-tags e2e_install ./internal/e2e/...` run.
- `cmd/pyry/update_e2e_test.go:1,16,59-70` — build tag `(darwin || linux) && e2e_update`,
  `package main` (⇒ scope is `./cmd/pyry/...`, **not** `internal/e2e`), and the
  `os.MkdirTemp` temp-HOME setup that proves the update suite is **hermetic** (in-process
  fake release server, temp HOME destroyed on cleanup) — it does **not** touch the real
  launchd/systemd domain.
- `docs/release-tooling.md:85-125` — the `## Live-relay smoke test` section added by #968.
  This is the model for the new section, and its natural neighbour: append after it.

## Context

Two real, maintained e2e suites are reachable only by hand-typed build tags, so they
effectively never run:

| Suite | Tag | Package scope | What it does | What it mutates |
|---|---|---|---|---|
| `internal/e2e/install_{darwin,linux}_test.go` | `e2e_install` | `./internal/e2e/...` | real launchd (`launchctl bootstrap gui/<uid>`) / systemd (`systemctl --user`) install round-trip | **the real user launchd/systemd domain** |
| `cmd/pyry/update_e2e_test.go` | `e2e_update` | `./cmd/pyry/...` (`package main`) | full `pyry update` flow: fetch → verify → atomic-replace → daemon restart, against an in-process fake release server | a **temp HOME** only; spawns/restarts a real daemon but touches **nothing outside the temp dir** |

They are deliberately excluded from `check`/`preship` and must stay excluded (the install
suite mutates the operator's real service domain). The only gap: no named, documented way
to run them on purpose — on the release checklist, or before touching install/update code.
This mirrors `e2e-liverelay` from #968.

## Design

Two changes, both mechanical, following the established template exactly.

### 1. `Makefile` — two new targets

Insert both targets after `e2e-liverelay` (i.e. after `Makefile:65`) and before the
`preship` comment block (`Makefile:67`). Do **not** touch `check`, `preship`, or the
`e2e_install`/`e2e_update` header note at `:31-33` — that note already documents the
deliberate exclusion (AC3) and stays as-is.

Contract (recipe shape, mirroring `e2e-realclaude` at :55-57):

```
.PHONY: e2e-install
e2e-install:
	$(GO) test -tags e2e_install ./internal/e2e/...

.PHONY: e2e-update
e2e-update:
	$(GO) test -tags e2e_update ./cmd/pyry/...
```

Each target gets a leading comment block (like the `e2e-liverelay` block at :59-62)
covering AC2: **when to run it** (on the release checklist; before touching install /
update code respectively) and **what it mutates**.

**Comment-accuracy design note (do not blind-copy the AC wording).** AC2 and AC4 say
"the warning that it mutates the real user launchd/systemd domain." That is precisely true
for `e2e-install` and **false** for `e2e-update` (temp-HOME hermetic; see the table and
`update_e2e_test.go:62-70`). Write each comment to its own suite's actual footprint —
this is what the existing header note already does by splitting the two ("the real
launchd/systemd user domain **and** the full update flow"):

- `e2e-install` comment: run on the release checklist and before touching install-service
  code; **mutates the real user launchd/systemd domain** (`launchctl bootstrap` /
  `systemctl --user`) — never run it where a live daemon must stay untouched.
- `e2e-update` comment: run on the release checklist and before touching `pyry update`
  code; drives the **full update flow** (fetch → verify → atomic binary replace → daemon
  restart) — spawns and restarts a real daemon, hermetic under a temp HOME.

### 2. `docs/release-tooling.md` — new section

Append a new top-level section after `## Live-relay smoke test` (after `:125`), following
the #968 section's structure. Covers AC4 for **both** targets: what each runs, when to run
it, and its real footprint (install → real user domain; update → full update flow under
temp HOME). Keep it accurate rather than echoing the "mutates the real user domain" phrase
onto the update half. Suggested shape (prose, developer writes the final copy):

- One `## Install & update e2e suites` heading (or two sibling `###` subsections under it).
- Per suite: the `make` invocation in a fenced `sh` block, the underlying `go test -tags …`
  command + package scope, when to run it, and its footprint.
- Note the OS gate for install (only the host platform's test compiles/runs under
  `e2e_install`) and the skip-loud behaviour on Linux when `systemctl --user
  is-system-running` reports an unusable session (`install_linux_test.go` header).
- One line stating both are **excluded from `check`/`preship` by design** and are opt-in.

## Concurrency model

N/A — no Go code. Makefile recipes and Markdown only.

## Error handling

N/A for the change itself. Behaviour on the developer's end: `go test` exits non-zero if a
suite fails; the install suite skips-loud (does not fail) when the systemd/launchd domain
is unusable — that behaviour lives in the test files and is not modified here.

## Testing strategy

No unit tests — this is build tooling + docs. Verification is a dry-run + build-tag check:

- `make -n e2e-install` prints `go test -tags e2e_install ./internal/e2e/...` and
  `make -n e2e-update` prints `go test -tags e2e_update ./cmd/pyry/...` — confirms recipe
  wiring without executing the mutating suites.
- `make -n check` and `make -n preship` still show **no** `e2e_install` / `e2e_update`
  invocation (AC3 — exclusion intact).
- `grep -n 'e2e_install\|e2e_update' Makefile` shows the header note at `:31-33` unchanged
  plus the two new recipes — no accidental edit to the exclusion note.
- Optional smoke on the host platform only: `make e2e-install` and `make e2e-update`
  compile and run (install mutates the real user domain — run only on a throwaway/host you
  control). Not required for the PR; the dry-run checks above are the gate.
- Standard gate unaffected: `go vet ./...` / `make check` behaviour is byte-identical to
  before (targets are additive `.PHONY`).

## Open questions

None. The template, tags, package scopes, and section placement are all fixed by the
existing `e2e-realclaude`/`e2e-liverelay`/#968 precedents.
