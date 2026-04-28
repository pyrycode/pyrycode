# Pyrycode developer Makefile.
#
# Targets that matter day to day:
#   make check    — run the same checks CI runs (vet + race tests + staticcheck).
#                   Run before every push to avoid the "every PR fails CI on the
#                   same lint warning" cycle that filled inboxes in late Apr 2026.
#   make build    — build the pyry binary at ./pyry (gitignored)
#   make test     — race-enabled tests only
#   make linux    — cross-compile for pyrybox (linux/amd64)
#   make clean    — remove build artifacts

GO          ?= go
STATICCHECK ?= $(shell which staticcheck 2>/dev/null || echo $(HOME)/go/bin/staticcheck)
BIN         ?= ./pyry
DIST        ?= ./dist

.PHONY: check
check: vet test staticcheck

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test
test:
	$(GO) test -race ./...

.PHONY: staticcheck
staticcheck:
	@if [ ! -x "$(STATICCHECK)" ]; then \
		echo "staticcheck not found; installing..."; \
		$(GO) install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi
	$(STATICCHECK) ./...

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
