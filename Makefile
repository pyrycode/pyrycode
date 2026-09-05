# Pyrycode developer Makefile.
#
# Targets that matter day to day:
#   make check    — run the same checks CI runs (vet + race tests + staticcheck
#                   + substrate-guard + the fake-daemon e2e suite). Run before
#                   every push to avoid the "every PR fails CI on the same lint
#                   warning" cycle that filled inboxes in late Apr 2026.
#   make build    — build the pyry binary at ./pyry (gitignored)
#   make test     — race-enabled tests only
#   make e2e      — fake-daemon end-to-end suite (builds pyry, spawns real
#                   daemons against fakeclaude/fakerelay/fakephone; hermetic,
#                   no creds, ~4 min with -race). Now part of `check`, so a
#                   core-daemon regression fails the standard gate; still
#                   runnable standalone for a focused e2e run.
#   make e2e-liverelay — live-relay smoke test: one round-trip against a REAL
#                   pyrycode-relay binary (opt-in tag e2e_liverelay). Builds and
#                   spawns the sibling ../pyrycode-relay checkout hermetically on
#                   a loopback listener; spends NO real resources and needs no
#                   credentials. Skips loud if the sibling checkout is absent
#                   (set PYRY_LIVERELAY_BIN or PYRY_RELAY_REPO). See
#                   docs/release-tooling.md § Live-relay smoke test.
#   make preship  — the full pre-binary-swap gate: check + e2e-realclaude +
#                   e2e-liverelay (check now includes the fake-daemon e2e suite).
#                   Run before every ~/.local/bin/pyry swap so the operator is
#                   never the first real-stack execution. Needs live claude creds
#                   (see docs/knowledge/features/e2e-realclaude.md); spends real
#                   tokens and shares the Max-plan usage window.
#   make install  — build HEAD stamped dev-<sha>, swap it into
#                   ~/.local/bin/pyry by rename, keep a dated backup plus
#                   pyry.prev, and bounce the managed daemon. NO_RESTART=1
#                   swaps only. Never `cp` over the live binary by hand: on
#                   Apple silicon that kills the daemon and leaves a file the
#                   kernel refuses to exec (seen 2026-09-02). Runs no gate;
#                   preship stays the deliberate step before a swap.
#   make rollback — put pyry.prev back and bounce the daemon.
#   make linux    — cross-compile for pyrybox (linux/amd64)
#   make clean    — remove build artifacts
#
# e2e_install and e2e_update stay separate opt-in tags (they touch the real
# launchd/systemd user domain and the full update flow) — deliberately not
# part of preship.

GO          ?= go
STATICCHECK ?= $(shell which staticcheck 2>/dev/null || echo $(HOME)/go/bin/staticcheck)
BIN         ?= ./pyry
DIST        ?= ./dist

.PHONY: check
check: vet test staticcheck substrate-guard cite-guard docs-guard e2e

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test
test:
	$(GO) test -race ./...

.PHONY: e2e
e2e:
	$(GO) test -tags e2e -race -count=1 ./internal/e2e/...

# e2e-realclaude, e2e-liverelay, e2e-install and e2e-update each pass -count=1,
# and it is load-bearing. Every one of them builds its binary under test with a
# subprocess `go build` (ensurePyryBuilt in internal/e2e, internal/e2e/realclaude
# and internal/e2e/liverelay; ensureRelayBuilt for the sibling relay; both build
# sites in cmd/pyry's update e2e test). A child process's file reads never enter
# the test binary's cache key, so `go test` cannot see the sources these suites
# actually compile — an edit confined to cmd/pyry, internal/brokenpyry or the
# sibling relay checkout leaves the key unchanged and replays the previous `ok`,
# proving nothing. Do not drop the flag as redundant. `test` above keeps its
# cache deliberately: it is untagged, reaches none of those build sites, and
# runs on every `make check`.
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude -count=1 ./internal/e2e/realclaude/...

# e2e-liverelay proves the daemon ↔ real pyrycode-relay pairing with one
# round-trip against a locally built relay binary. Offline-capable and
# credential-free (hermetic loopback relay); skips loud without the sibling
# ../pyrycode-relay checkout. See docs/release-tooling.md § Live-relay smoke test.
.PHONY: e2e-liverelay
e2e-liverelay:
	$(GO) test -tags e2e_liverelay -count=1 ./internal/e2e/liverelay/...

# e2e-install runs the real install round-trip (opt-in tag e2e_install). The
# darwin/linux build tags mean only the host platform's install test compiles
# and runs. Run it on the release checklist and before touching install-service
# code. WARNING: it mutates the real user launchd/systemd domain (launchctl
# bootstrap / systemctl --user) — never run it where a live daemon must stay
# untouched. See docs/release-tooling.md § Install & update e2e suites.
.PHONY: e2e-install
e2e-install:
	$(GO) test -tags e2e_install -count=1 ./internal/e2e/...

# e2e-update runs the full `pyry update` flow (opt-in tag e2e_update): fetch →
# verify → atomic binary replace → daemon restart, against an in-process fake
# release server. Run it on the release checklist and before touching
# `pyry update` code. It spawns and restarts a real daemon but is hermetic —
# everything lives under a temp HOME destroyed on cleanup, so it touches nothing
# outside the temp dir. Scoped to ./cmd/pyry/... (package main), not
# internal/e2e. See docs/release-tooling.md § Install & update e2e suites.
.PHONY: e2e-update
e2e-update:
	$(GO) test -tags e2e_update -count=1 ./cmd/pyry/...

# preship is the pre-binary-swap gate: everything hermetic (via check, which
# now runs the fake-daemon e2e suite) plus the live-claude suite and the
# live-relay round-trip. Deliberately runs the realclaude suite in FULL — the
# operator must never be the first real-stack execution, and trimming for speed
# is a fix-when-it-hurts decision, not a default.
.PHONY: preship
preship: check e2e-realclaude e2e-liverelay

.PHONY: staticcheck
staticcheck:
	@if [ ! -x "$(STATICCHECK)" ]; then \
		echo "staticcheck not found; installing..."; \
		$(GO) install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi
	$(STATICCHECK) ./...

# substrate-guard fails if any claude-TUI substrate literal (screen string,
# escape sequence, glyph) appears in pyrycode .go source outside the allowlist.
# The text-fabric backstop to the tui-driver compiler seal — see
# cmd/substrate-guard. Fast (a file walk); no network or install needed.
.PHONY: substrate-guard
substrate-guard:
	$(GO) run ./cmd/substrate-guard

# Bans a comment citation by file and line where a symbol name would do —
# see cmd/cite-guard. Line numbers rot on every insertion and nothing
# maintained them; codegraph resolves a symbol on demand. Same fabric-of-a-
# different-kind argument as substrate-guard above: a style-guide rule cannot
# police a style-guide rule. Fast (a file walk); no network or install needed.
.PHONY: cite-guard
cite-guard:
	$(GO) run ./cmd/cite-guard

# Bounds a package overview's size and bans a line that parses as a heading
# only because a wrapped paragraph put a ticket reference first — see
# cmd/docs-guard. Search chunks markdown by byte count with no heading
# awareness, so an overview past the cap stops being retrievable at all and a
# lesson folded into it is a lesson lost. Same fabric-of-a-different-kind
# argument as the two guards above: the documentation agent already carries a
# prose rule for both, and a prose rule cannot police a prose rule. Fast (a
# file walk); no network or install needed.
.PHONY: docs-guard
docs-guard:
	$(GO) run ./cmd/docs-guard

.PHONY: build
build:
	$(GO) build -o $(BIN) ./cmd/pyry

# install and rollback drive the operator's own daemon. The logic lives in
# scripts/install-dev.sh so the swap is one reviewed path rather than a
# recipe retyped from a runbook. PYRY_INSTALL_DIR overrides ~/.local/bin.
.PHONY: install
install:
	NO_RESTART=$(NO_RESTART) ./scripts/install-dev.sh install

.PHONY: rollback
rollback:
	NO_RESTART=$(NO_RESTART) ./scripts/install-dev.sh rollback

.PHONY: linux
linux:
	mkdir -p $(DIST)
	GOOS=linux  GOARCH=amd64 $(GO) build -o $(DIST)/pyry-linux-amd64  ./cmd/pyry

.PHONY: dist
dist: linux
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(DIST)/pyry-darwin-arm64 ./cmd/pyry
	GOOS=darwin GOARCH=amd64 $(GO) build -o $(DIST)/pyry-darwin-amd64 ./cmd/pyry

.PHONY: clean
clean:
	rm -f $(BIN)
	rm -rf $(DIST)
