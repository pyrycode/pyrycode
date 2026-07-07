# Spec: Assemble a session debug bundle (recording + daemon logs) (#811)

**Ticket:** #811 · **Size:** S · **Labels:** `size:s`, `security-sensitive`
**Chosen mechanism:** a new zero-dependency `internal/debugbundle` package whose `Assemble(recordingsDir, logs)` reads the newest `.cast` from a caller-supplied recordings directory and the caller-supplied log-ring snapshot, streams them plus a JSON manifest into an in-memory `tar`+`gzip` archive, and returns the archive bytes alongside a content-free `Manifest`. Pure content assembly — no wire, no pairing, no disk write. Serving the bytes to a paired client is the sibling ticket #813 (over the chunked transport of #812).

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:612-623` — `recordingPath`: the **as-shipped** `.cast` name is `<stamp>-<sessionID>.cast`, `stamp = time.Now().UTC().Format("20060102T150405Z")` (per-second UTC, session id embedded). Extract: the name forms a single `*.cast` glob must match.
- `internal/agentrun/ptyrunner/runner.go:625-648` — `finalizeRecording`: the post-run rename appends `-ok`/`-err` **before** `.cast`, so finalized files are `<stem>-ok.cast` / `<stem>-err.cast` and the **in-flight** file has no suffix. Extract: three name forms, all matched by `*.cast`; "current session" = newest.
- `internal/agentrun/ptyrunner/runner.go:650-676` — `pruneOldRecordings`: the `filepath.Glob(filepath.Join(dir, "*.cast"))` idiom + the comment proving `*` never crosses a separator (non-recursive, cannot escape `dir`). Extract: the exact glob to reuse; do **not** copy the prune/mtime-cutoff logic (out of scope).
- `internal/control/logs.go:57-73` — `RingBuffer.Snapshot() []string` (oldest-first copy) and `Cap()`. Extract: the log source is a `[]string`; `Assemble` accepts it as a parameter (no import of `internal/control`).
- `internal/control/server.go:498-503` — the `pyry logs` handler builds its reply from `s.logs.Snapshot()`. Extract: proof that "the same content `pyry logs` returns" is exactly `Snapshot()`.
- `cmd/pyry/main.go:707` — `control.NewRingBuffer(200)`: the flat 200-line ring, one shared buffer, **no per-session key**. Extract: confirms per-session log filtering is impossible here (out of scope; whole-ring only).
- `internal/update/install.go:35-70` — `ExtractBinary`: the in-repo `archive/tar` + `compress/gzip` precedent (reader side). Extract: the stdlib API shape and import set to mirror on the **writer** side.
- `docs/specs/architecture/802-debug-capture-setting.md` §§ "Design", "Security review" — the recorder's security posture (`0600 O_EXCL`, non-synced dir, "recording is the highest-value secret surface", content-free logging). Extract: the inherited invariants this ticket must not weaken.

## Context

When a client app misbehaves, the operator wants the session's evidence — the terminal recording (#802, when capture was on) plus the recent daemon logs — without SSHing into the host. This ticket packages those two sources into one in-memory archive with a manifest. It is the **producer**; the sibling serving path (#812 chunked transport, #813 request verb) is the consumer that hands the bytes to a paired client over the encrypted channel.

Scope is deliberately narrow: read two already-existing content sources, emit one archive, in memory. No new wire type, no pairing, no policy about *who* may receive the bundle (that authorization lives in #813). The package ships **unwired** — 0 non-test callers — which is correct for a primitive; #813 is its first caller.

## Design

### Package `internal/debugbundle`

One new file `internal/debugbundle/bundle.go`. Flat, zero external deps, stdlib only (`archive/tar`, `compress/gzip`, `bytes`, `encoding/json`, `fmt`, `io`, `os`, `path/filepath`, `strings`, `syscall`, `time`). Does **not** import `internal/control` (log lines arrive as a parameter) — keeps it a pure leaf.

#### Exported surface (the contract)

- `type Manifest struct { … }` — content-free description of the bundle. Fields (all metadata, never content):
  - `RecordingPresent bool   json:"recording_present"`
  - `RecordingName    string json:"recording_name,omitempty"` — the on-disk basename (timestamp + session id + `-ok`/`-err`); a data string, never used as a path.
  - `RecordingBytes   int64  json:"recording_bytes"` — size of the included `.cast` (0 when absent).
  - `LogLineCount     int    json:"log_line_count"`
  - `LogBytes         int    json:"log_bytes"` — byte length of the joined log text.
- `func Assemble(recordingsDir string, logs []string) (archive []byte, m Manifest, err error)` — the sole entry point. Behaviour summary:
  1. Select the newest recording via the private `newestRecording` helper (below).
  2. Build the `Manifest` from the selection + `len(logs)` + joined-log byte length.
  3. Stream `manifest.json`, `logs.txt`, and (when present) `recording.cast` into a `gzip`-wrapped `tar` writer over a `bytes.Buffer`; close tar then gzip; return `buf.Bytes()`.
  Returns a wrapped error only on a genuine I/O/encode failure (see § Error handling). Emits **no logs** (see § Security).
- `func DefaultRecordingsDir() (string, error)` — resolves `filepath.Join(home, ".local", "share", "pyry-recordings")` from `os.UserHomeDir()`, mirroring #802's `resolveRecordingsDir`. The ticket directs duplicating the join rather than importing the unexported package-`main` helper. Co-located here because this package is the recordings **reader**; #813 calls it to feed `Assemble`. `Assemble` itself takes an explicit dir so tests drive it with `t.TempDir()`.

#### Private helper (the AC1/AC2 seam)

- `func newestRecording(dir string) (name string, size int64, mtime time.Time, present bool, err error)` — `filepath.Glob(filepath.Join(dir, "*.cast"))`; for each match `os.Stat`, **skipping** entries whose stat fails (a file finalized/renamed between glob and stat — a benign TOCTOU race, not a bundle failure); track the entry with the greatest `ModTime()`. Zero matches (or all skipped) → `present == false`, no error. Newest-by-mtime is the selector: two sessions started in the same UTC second share a stamp prefix, so a lexical-stamp sort would need a tiebreak; mtime does not. `name` is `filepath.Base(match)` of the winner.

#### Archive layout (fixed member names)

| Member | Always present? | Body |
|---|---|---|
| `manifest.json` | yes | `json.MarshalIndent(m, "", "  ")` |
| `logs.txt` | yes (even when empty) | `strings.Join(logs, "\n")` |
| `recording.cast` | only when `RecordingPresent` | streamed from the on-disk `.cast` |

**Member names are hard-coded constants**, never derived from the on-disk filename. This is a deliberate security choice (§ Security, Trust boundaries): a crafted filename in the recordings dir can never influence the tar structure. The real basename travels only inside `manifest.RecordingName` as a JSON string value.

The recording is streamed with `io.Copy(tarWriter, file)` after setting the tar header `Size` from the stat — the raw `.cast` is never held whole in a second `[]byte`. The read open is `os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)`: the final path component is opened only if it is a regular file, so a same-user symlink swapped into the recordings dir between selection and read (the stat→open TOCTOU) fails the open rather than exfiltrating the symlink's target into the bundle. tui-driver creates real files with `O_EXCL` and never symlinks, so a legitimate `.cast` is never rejected by this flag. `syscall.O_NOFOLLOW` is present on both Linux and macOS. The tar header `ModTime` for `recording.cast` is the file's mtime (informative); `manifest.json`/`logs.txt` use a zero-value `ModTime` (documented) so `Assemble` calls no clock and is fully deterministic in tests. Members use mode `0600` in their tar headers, mirroring the on-disk recording posture.

### Data flow

```
recordingsDir  ──filepath.Glob("*.cast")──▶ newestRecording ──▶ (name,size,mtime,present)
     │                                                                     │
logs []string ──strings.Join("\n")──▶ logs.txt body                        │
     │                                                                     ▼
     └──────────────────────────────▶ Manifest{present,name,recBytes,lines,logBytes}
                                                 │
                          gzip.Writer→tar.Writer over bytes.Buffer (in memory ONLY)
                          write manifest.json, logs.txt, [recording.cast via io.Copy]
                                                 │
                                                 ▼
                                    ([]byte archive, Manifest, error)
```

Nothing in this path opens a file for writing; the only disk touch is read-only (`Glob`, `Stat`, `os.Open` of the `.cast`). The archive lives solely in the returned `[]byte`.

## Concurrency model

No goroutines, channels, or locks. `Assemble` is a synchronous, single-goroutine function. Reading the **in-flight** (unsuffixed) recording concurrently with tui-driver's writer is safe: `O_EXCL` constrains only the create, not later read-only opens; a read-only `os.Open` sees the flushed prefix (a valid, replayable asciinema-v2 cast — header first). No coordination with the writer is needed or attempted. Callers may invoke `Assemble` from any goroutine; it shares no state.

## Error handling

- **`recordingsDir` glob error** — only reachable via `filepath.ErrBadPattern`, impossible with the fixed `*.cast` pattern; still returned wrapped (`fmt.Errorf("debugbundle: glob recordings: %w", err)`) for completeness.
- **Per-file `Stat` failure during selection** — skipped, not fatal (finalize-rename race). If *every* matched entry is unstattable, the bundle reports `RecordingPresent: false` and succeeds with logs only.
- **Chosen recording fails to `OpenFile`/`io.Copy`** — returned as a wrapped error. This is the honest choice: silently reporting `RecordingPresent: false` when a recording exists would make the manifest **lie**. "Absent" means *no recording exists*; a read failure is a real assembly failure. (Contrast AC2's absent case, which is genuinely zero-matches.) A symlink at the selected path also lands here: `O_NOFOLLOW` makes the open fail (`ELOOP`) → wrapped error, never a silent read of the target.
- **`tar`/`gzip`/`json` write/encode error** — wrapped and returned; a `bytes.Buffer` never fails to write, so these are effectively unreachable but handled per Go idiom.
- Every error keeps a stable `"debugbundle: …"` wrap prefix. No partial archive is returned on error (return `nil, Manifest{}, err`).

## Testing strategy

All unit tests in `internal/debugbundle/bundle_test.go`, same-package, table-driven where natural, stdlib `testing` only, `t.Parallel()`. A small `readArchive(t, b []byte) (members map[string][]byte)` helper untars-and-ungzips a bundle so assertions read member bodies. Scenarios:

- **AC1 — newest recording included, exactly one.** Write three `.cast` files into `t.TempDir()` with staggered mtimes (e.g. `os.Chtimes`), plus the newest with known bytes. Assert the archive contains exactly one `recording.cast`, its bytes equal the newest file's, `Manifest.RecordingPresent == true`, `RecordingName` == the newest basename, `RecordingBytes` == its size. Include a same-second-stamp pair with differing mtimes to prove mtime (not lexical) selection.
- **AC2 — no recording → marked absent.** Empty recordings dir (and a variant with a non-`.cast` file present). Assert no `recording.cast` member, `Manifest.RecordingPresent == false`, `RecordingBytes == 0`, and the bundle still untars cleanly.
- **AC3 — logs always included.** Non-empty `logs` → `logs.txt` present with `strings.Join(logs, "\n")`; and an **empty** `logs` slice → `logs.txt` still present (empty body), `LogLineCount == 0`. Covers "always includes a snapshot."
- **AC4 — single in-memory archive + manifest.** Assert `Assemble` returns non-nil `[]byte` that is a valid gzip→tar with a parseable `manifest.json` whose decoded fields match the returned `Manifest`. One archive, both content sources inside.
- **AC5 — manifest carries no content.** Seed the recording body and log lines with sentinel secret strings; assert neither sentinel appears anywhere in the marshalled `manifest.json` (only name/size/count fields). Complements the structural no-logging property (§ Security) — the package makes no `slog` call, so there is no log emission to leak into.
- **In-flight read.** Open a `.cast` with `O_EXCL`, write a partial (header-only) body, leave it open, and assert `Assemble` still reads the flushed prefix without error (models the currently-running session).
- **Read-failure honesty.** A `.cast` whose mode denies read (`0000`, `t.Chmod`) as the sole/newest match → `Assemble` returns a wrapped error, not a false `RecordingPresent: false`. (Skip on `root`/CI where mode is ignored.)
- **Symlink rejection (`O_NOFOLLOW`).** Make the newest `*.cast` match a symlink pointing at another file; assert `Assemble` returns a wrapped error and does **not** read the target into the bundle. (Skip on platforms/filesystems where the symlink can't be created.)

**Non-vacuity note for the developer:** `readArchive` must assert real member bodies (byte-compare the recording, string-compare `logs.txt`), not merely that the archive is non-empty.

## Open questions

- **Multiple recordings / all-since-boot.** The AC fixes "exactly one, the newest" when a recording exists; this spec ships exactly that. Bundling several recordings (e.g. all since daemon boot) is a future enhancement and would change only `newestRecording` → a multi-select + N `recording-<n>.cast` members; deferred, no observed need.
- **Per-session log correlation.** The ring is a flat 200-line buffer with no session key; per-session log filtering (the design note's "one shared correlation identifier") is explicitly out of scope and unaffected by the `.cast` filename now carrying a session id. Whole-ring only.
- **Compression choice.** `tar`+`gzip` chosen over `zip` for the in-repo precedent (`internal/update`) and streaming-friendliness. A `.cast` is already text-ish and compresses well; no need to revisit unless bundle size becomes a transport concern in #812.

## Security review

**Verdict:** PASS

This ticket is the **first read path** over the recording — "the highest-value secret surface in the system" (every PTY byte, unencrypted by design). Each category below is walked against "could this leak, persist to a synced location, or let a hostile actor influence the recorder/bundle without a local operator's own privilege?"

**Findings:**

- **[Trust boundaries]** No finding — the file→memory boundary is the two reads (`.cast` bytes, log lines) and both sources are local and trusted. The one adversarial angle, a **crafted filename** in the recordings dir, is defeated two ways: `filepath.Glob("<dir>/*.cast")` is non-recursive (`*` never crosses a separator, so a match is a single top-level component — proven by `pruneOldRecordings`'s comment), and every tar member name is a **hard-coded constant** (`manifest.json`, `logs.txt`, `recording.cast`), never derived from the on-disk basename. An attacker cannot inject a traversal path (e.g. `../../x`) into the archive structure that a naive downstream extractor might follow. The real basename travels only as a JSON *value* in `manifest.RecordingName`, never used as a path.
- **[Tokens, secrets, credentials]** N/A — no tokens generated, stored, or compared here. The recording *is* the secret being packaged; its confidentiality is handled under File operations (no persistence) and Errors/logs (no leak).
- **[File operations]** No MUST FIX. **No file is created** — `Assemble` opens files read-only (`Glob`, `Stat`, `OpenFile` `O_RDONLY`) and writes exclusively to an in-memory `bytes.Buffer`; there is no `os.Create`/`WriteFile`/temp file anywhere, so the assembled bundle is **never written to any synced/backed-up path** (the ticket's explicit security constraint, satisfied structurally). The stat→open **TOCTOU** on the selected `.cast` is hardened with `syscall.O_NOFOLLOW`: a same-user symlink swapped in after selection fails the open (honest error) instead of exfiltrating the target's bytes into the bundle. (The swap requires write access to the `0700` owner-only recordings dir — already a local-account compromise, no privilege boundary crossed — but the flag is free defense-in-depth and is designed in, not deferred.) Per-file stat races during selection are skipped, not fatal.
- **[Subprocess / external command execution]** N/A — no `exec.Command`, no `sh -c`, no environment handling. Pure in-process assembly.
- **[Cryptographic primitives]** N/A — no RNG, no hashing, no key material. The bundle is plaintext by design (must stay `asciinema play`-able); confidentiality **in transit** is the sibling channel's job (#812, Noise-encrypted chunked transport). This ticket must only avoid *persisting* the plaintext (handled under File operations).
- **[Network & I/O]** OUT OF SCOPE (owner: **#812**). A recording is unbounded in size (a long session = every PTY byte), and `Assemble` holds the compressed archive in memory. There is **no remote-controlled input size** here — a paired client cannot inflate the recording, only request whatever the local session already produced — so this is not a remote DoS vector. Bounding/streaming a large bundle under the 64 KB frame cap is exactly #812's charter; #811 correctly produces the whole archive and #812 owns transport-side chunking. Per Evidence-Based Fix Selection, no in-assembly size cap is added for an unobserved OOM.
- **[Error messages, logs, telemetry]** No finding — the package makes **zero** `slog`/`log` calls, so there is structurally no log emission from this path that could carry recording bytes or log-line contents (AC5, satisfied by construction, not just by discipline). Wrapped errors carry only the operation name + path + underlying OS error (`%w` over an `os.PathError`/glob error — path + errno, never file content). The returned `Manifest` exposes only name/size/count; the AC5 test asserts seeded secret sentinels never appear in `manifest.json`. `RecordingName` is a UTC stamp + random UUIDv4 session id + `-ok`/`-err` — an identifier, not a credential.
- **[Concurrency]** No finding — no goroutines spawned, no locks, no shared mutable state; `Assemble` is synchronous and reentrant-safe. Reading the in-flight (unsuffixed) recording concurrently with tui-driver's writer is safe: `O_EXCL` constrains only the create, a read-only open sees the flushed valid prefix. A mid-assembly process kill loses only the in-memory buffer — nothing partial is left on disk (there is no disk write to interrupt).
- **[Threat model alignment]** No finding — the recording-confidentiality threat is met: in-memory-only assembly (no synced-path write), content-free manifest, no new log leakage, recordings read from the fixed non-synced `~/.local/share/pyry-recordings` established by #802. The **authorization** threat — *who* is allowed to receive a bundle — is explicitly **out of scope** and owned by the request verb #813 (over #812's encrypted transport); this producer carries no policy about recipients.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-07
