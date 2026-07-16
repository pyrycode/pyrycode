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
check: vet test staticcheck substrate-guard e2e

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test
test:
	$(GO) test -race ./...

.PHONY: e2e
e2e:
	$(GO) test -tags e2e -race -count=1 ./internal/e2e/...

.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...

# e2e-liverelay proves the daemon ↔ real pyrycode-relay pairing with one
# round-trip against a locally built relay binary. Offline-capable and
# credential-free (hermetic loopback relay); skips loud without the sibling
# ../pyrycode-relay checkout. See docs/release-tooling.md § Live-relay smoke test.
.PHONY: e2e-liverelay
e2e-liverelay:
	$(GO) test -tags e2e_liverelay ./internal/e2e/liverelay/...

# e2e-install runs the real install round-trip (opt-in tag e2e_install). The
# darwin/linux build tags mean only the host platform's install test compiles
# and runs. Run it on the release checklist and before touching install-service
# code. WARNING: it mutates the real user launchd/systemd domain (launchctl
# bootstrap / systemctl --user) — never run it where a live daemon must stay
# untouched. See docs/release-tooling.md § Install & update e2e suites.
.PHONY: e2e-install
e2e-install:
	$(GO) test -tags e2e_install ./internal/e2e/...

# e2e-update runs the full `pyry update` flow (opt-in tag e2e_update): fetch →
# verify → atomic binary replace → daemon restart, against an in-process fake
# release server. Run it on the release checklist and before touching
# `pyry update` code. It spawns and restarts a real daemon but is hermetic —
# everything lives under a temp HOME destroyed on cleanup, so it touches nothing
# outside the temp dir. Scoped to ./cmd/pyry/... (package main), not
# internal/e2e. See docs/release-tooling.md § Install & update e2e suites.
.PHONY: e2e-update
e2e-update:
	$(GO) test -tags e2e_update ./cmd/pyry/...

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

.PHONY: build
build:
	$(GO) build -o $(BIN) ./cmd/pyry

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
