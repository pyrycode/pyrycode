# Spec: e2e/realclaude — scaffold directory + build tag + make target

Ticket: [#361](https://github.com/pyrycode/pyrycode/issues/361)
Size: XS (pure plumbing)

## Files to read first

- `internal/e2e/fakeclaude_test.go:1-10` — example of an `//go:build e2e` file in the existing e2e package; mirror the build-tag header style.
- `internal/e2e/harness.go:1-10` — confirms the existing tag form is `//go:build e2e || e2e_install`. The new tag is a sibling, not an alias.
- `Makefile:18-30` — existing `.PHONY: test` target and the surrounding section where the new `e2e-realclaude` target slots in.

(No codegraph context entries: there are no existing symbols in `internal/e2e/realclaude` to look up. This ticket creates the directory.)

## Context

Phase C on 2026-05-14 broke because `internal/agentrun/drive_e2e_test.go` uses `TestHelperProcess` fakes that skip the trust boundary with the real `claude` binary (the `/doctor` prompt-poisoning class of bug). Subsequent tickets (#362, #363) will add real-`claude` integration tests, but they need a build-tag-gated home that does NOT run in `make test`.

This ticket creates that home. No business logic. One new test file, one Makefile target.

## Design

### Directory

```
internal/e2e/realclaude/
  smoke_test.go      (new, build-tagged)
```

`realclaude` is its own Go package (separate from `internal/e2e` which uses tag `e2e || e2e_install`). Sibling subpackage keeps real-`claude` work isolated and lets `make test` skip it via tag exclusion alone — no path filter needed.

### Build tag

All files in the directory carry:

```go
//go:build e2e_realclaude
```

Single tag, no alternation. This is deliberately distinct from `e2e` so that a future `make e2e` target (if introduced) does NOT pick up real-claude tests, and vice versa. Tag form follows the established `e2e_install` convention.

### `smoke_test.go` (the only file)

Package: `realclaude` (not `realclaude_test`).

Contents — single test `TestClaudeBinaryAvailable`:

- Asserts `exec.LookPath("claude")` returns nil error. If it fails, `t.Fatalf` with a message telling the developer that this suite requires the real `claude` binary on PATH.
- Runs `exec.Command("claude", "--version")` with a `context.WithTimeout(ctx, 10*time.Second)`. Asserts `Run()` returns nil. On non-zero exit or timeout, `t.Fatalf` with stdout+stderr captured.

That is the entire file. ~25 lines including imports and the build tag.

The test does NOT parse the version string. The goal is "real claude is on PATH and executes" — content checks belong in #362+.

### Makefile target

Add after the existing `test:` target (Makefile line ~28):

```make
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...
```

No `-race` flag. Real-claude integration tests are I/O-bound trust-boundary checks, not goroutine-stress tests; `-race` adds noise without value here. If a future test in this directory does spin goroutines, the author can add `-race` to that specific invocation; we don't bake it into the directory's default.

`make check` is unchanged — does NOT include `e2e-realclaude`. CI continues to run only `make check`. This suite is opt-in, developer-local, and runs only when explicitly invoked.

### Verification of tag exclusion (Done-when #5)

The developer should verify by running `make test` after the new file lands and confirming output shows zero tests from `internal/e2e/realclaude/`. Standard Go behavior — files with an unsatisfied build tag are skipped at the build stage, so the package compiles to an empty test binary and `go test` reports `ok  internal/e2e/realclaude  [no test files]` or similar. No assertion code needed; this is a manual one-shot verification on the developer's machine.

## Concurrency model

N/A. The single test invokes `exec.Command(...).Run()` synchronously with a context timeout. No goroutines.

## Error handling

- `exec.LookPath("claude")` failure → `t.Fatalf` with PATH guidance. The test does NOT skip — failure is the point. If real claude isn't on PATH, the suite is misconfigured for the developer's environment and they need to know.
- `claude --version` non-zero exit or timeout → `t.Fatalf` with combined stdout+stderr captured via `cmd.CombinedOutput()`.

Rationale for fatal-not-skip: this suite is invoked only when the developer types `make e2e-realclaude`. They opted in. Skipping silently would hide the misconfiguration.

## Testing strategy

The smoke test IS the test. Verification of the scaffolding itself:

1. `make e2e-realclaude` passes locally (developer with real `claude` on PATH).
2. `make test` output does NOT mention any test in `internal/e2e/realclaude`. (Run `make test 2>&1 | grep realclaude` and confirm empty output — or `ok` line only with `[no test files]`.)
3. `make check` (vet + test + staticcheck) passes — confirming the build-tagged file doesn't break vet/staticcheck on the default tag set.

## Open questions

None. The ticket is fully specified; this spec adds only the minor choices (separate package, single tag, no `-race`, fatal-not-skip) and their rationales.
