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
#   make preship  — the full pre-binary-swap gate: check + e2e-realclaude
#                   (check now includes the fake-daemon e2e suite). Run before
#                   every ~/.local/bin/pyry swap so the operator is never the
#                   first real-stack execution. Needs live claude creds (see
#                   docs/knowledge/features/e2e-realclaude.md); spends real
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

# preship is the pre-binary-swap gate: everything hermetic (via check, which
# now runs the fake-daemon e2e suite) plus the live-claude suite. Deliberately
# runs the realclaude suite in FULL — the operator must never be the first
# real-stack execution, and trimming for speed is a fix-when-it-hurts decision,
# not a default.
.PHONY: preship
preship: check e2e-realclaude

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
