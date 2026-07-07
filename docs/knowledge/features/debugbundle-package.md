# `internal/debugbundle` — session debug-bundle assembler

The **producer** half of the debug-bundle feature (design: the desktop
"Diagnostics and Debug Bundle" vault note). When a paired client misbehaves, an
operator wants the session's evidence — the terminal recording (when #802 debug
capture was on) plus the recent daemon logs — without SSHing into the host. This
package packages those two already-existing content sources into **one in-memory
`tar`+`gzip` archive** with a content-free manifest (#811).

It is a pure leaf: it reads two content sources (a recordings directory and a
caller-supplied log-line snapshot) and returns the archive bytes. It **never
writes to disk**, does not import `internal/control` (log lines arrive as a
parameter), and makes **zero `slog` calls** — so no recording byte or log-line
value can leak through a log emission from this path.

**Ships unwired** — 0 non-test callers, which is correct for a primitive. Serving
the bytes to a paired client is the sibling serving path: #812 (chunked transport
under the 64 KB frame cap) then #813 (the request verb + recipient authorization).
This package carries **no policy about who may receive a bundle**.

- Spec: [`811-assemble-session-debug-bundle.md`](../../specs/architecture/811-assemble-session-debug-bundle.md)
- Ticket record: [codebase/811.md](../codebase/811.md) (patterns + lessons)

## Files

```
internal/debugbundle/
├── bundle.go        Assemble + Manifest + DefaultRecordingsDir + private helpers
└── bundle_test.go   AC1–AC5 + in-flight read + read-failure honesty + O_NOFOLLOW symlink rejection
```

One file, zero external deps, stdlib only (`archive/tar`, `compress/gzip`,
`bytes`, `encoding/json`, `fmt`, `io`, `os`, `path/filepath`, `strings`,
`syscall`, `time`).

## Public API

```go
// Manifest is the content-free description of a bundle. Every field is metadata
// (name, size, count) — never recording bytes or log-line values.
type Manifest struct {
    RecordingPresent bool   `json:"recording_present"`
    RecordingName    string `json:"recording_name,omitempty"` // on-disk basename; a data string, never a path
    RecordingBytes   int64  `json:"recording_bytes"`          // size of the included .cast, 0 when absent
    LogLineCount     int    `json:"log_line_count"`
    LogBytes         int    `json:"log_bytes"`                // byte length of the joined log text
}

// Assemble reads the newest .cast in recordingsDir + the caller-supplied log
// snapshot, streams them plus a JSON manifest into a gzip-wrapped tar archive
// held entirely in memory, and returns the archive bytes + the content-free Manifest.
func Assemble(recordingsDir string, logs []string) (archive []byte, m Manifest, err error)

// DefaultRecordingsDir resolves ~/.local/share/pyry-recordings, mirroring #802's recorder.
func DefaultRecordingsDir() (string, error)
```

`Assemble` takes an **explicit** dir so tests drive it with a temp directory;
`DefaultRecordingsDir` is the production resolver #813 calls to feed it.

## The two content sources

- **Recording** — the most-recent `.cast` in the recordings directory. #802 turns
  the [ptyrunner flight recorder](ptyrunner-package.md#session-flight-recorder-pyry_record_dir)
  (#552) on and points it at the compile-time-fixed `~/.local/share/pyry-recordings/`,
  where it writes `<stamp>-<sessionID>[-ok|-err].cast` (`stamp =
  time.Now().UTC().Format("20060102T150405Z")`, per-second UTC, session id
  embedded). The `-ok`/`-err` suffix is a post-run rename, so the **currently-running
  session's recording is the in-flight file with no suffix**, still held open `0600`
  by tui-driver. A single non-recursive `filepath.Glob("<dir>/*.cast")` matches all
  three name forms (files sit at the top level, never nested). `DefaultRecordingsDir`
  **duplicates** the `filepath.Join(home, ".local", "share", "pyry-recordings")`
  rather than importing `cmd/pyry`'s unexported `resolveRecordingsDir` (package-main,
  unimportable — the ticket directs the duplication).

- **Logs** — the caller passes the [`internal/control`](control-plane.md) ring
  snapshot (`RingBuffer.Snapshot() []string`), exactly the lines `pyry logs`
  returns. The ring is a **flat 200-line buffer with no per-session key**, so
  per-session log filtering is impossible here and out of scope — whole-ring only,
  even though the `.cast` filename now carries a session id (the log ring still has
  no key to filter on). Passing logs as a parameter keeps this package a pure leaf
  with no `internal/control` import.

## Newest-by-mtime selection

`newestRecording` globs `*.cast`, `os.Stat`s each match, and tracks the entry with
the greatest `ModTime()`. **Newest-by-mtime, not lexical-stamp**, is the robust
selector: two sessions started in the same UTC second share a stamp prefix, so a
lexical sort would need a tiebreak; mtime does not. Zero matches — or all skipped —
yields `present == false` with no error. An entry whose `Stat` fails is **skipped**
(a file finalized and renamed between glob and stat is a benign TOCTOU race, not a
bundle failure).

## Archive layout — fixed member names

| Member | Always present? | Body |
|---|---|---|
| `manifest.json` | yes | `json.MarshalIndent(m, "", "  ")` |
| `logs.txt` | yes (even when empty) | `strings.Join(logs, "\n")` |
| `recording.cast` | only when `RecordingPresent` | streamed from the on-disk `.cast` via `io.CopyN` |

**Member names are hard-coded constants, never derived from the on-disk
filename** — a security choice: a crafted filename in the recordings dir can never
influence the tar structure (no injected `../../x` traversal a naive downstream
extractor might follow). The real basename travels only as a JSON *value* in
`manifest.RecordingName`, never as a path. The in-memory members
(`manifest.json`/`logs.txt`) carry a **zero-value `ModTime`** so `Assemble` reads
no clock and is fully deterministic in tests; only `recording.cast` carries the
file's real mtime. All members use mode `0600`, mirroring the on-disk recording
posture.

## Security (`security-sensitive`)

The recording is "the highest-value secret surface in the system" (every PTY byte,
unencrypted by design so it stays `asciinema play`-able). This ticket is the
**first read path** over it. The confidentiality invariants are satisfied
**structurally**, not by discipline:

- **No disk write.** `Assemble` opens files read-only (`Glob`, `Stat`, `OpenFile
  O_RDONLY`) and writes only to an in-memory `bytes.Buffer` — no `os.Create` /
  `WriteFile` / temp file anywhere. The bundle is **never written to any
  synced/backed-up path** (the ticket's explicit constraint). A mid-assembly
  process kill loses only the in-memory buffer.
- **`O_NOFOLLOW` on the recording open** hardens the stat→open TOCTOU: a same-user
  symlink swapped into the recordings dir after selection fails the open (honest
  wrapped error) instead of exfiltrating the target's bytes into the bundle.
  tui-driver creates real files with `O_EXCL` and never symlinks, so a legitimate
  recording is never rejected.
- **Content-free manifest + zero logging.** The package makes no `slog`/`log` call,
  so there is structurally no log emission that could carry recording bytes or
  log-line contents (AC5 by construction). Wrapped errors carry only op name + path
  + underlying OS errno, never file content. `RecordingName` is a UTC stamp + random
  UUIDv4 session id + outcome suffix — an identifier, not a credential.
- **Authorization is out of scope** — *who* may receive a bundle is owned by the
  request verb #813 over #812's Noise-encrypted transport. This producer carries no
  recipient policy, and bundle-size bounding under the frame cap is #812's charter
  (there is no remote-controlled input size here — a paired client cannot inflate
  the recording, only request what the local session already produced).

Security review verdict: **PASS** (see the spec's `## Security review`).

## Concurrency & error posture

No goroutines, channels, or locks — `Assemble` is synchronous and reentrant-safe.
Reading the **in-flight** (unsuffixed) recording concurrently with tui-driver's
writer is safe: `O_EXCL` constrains only the create, and a read-only open sees the
flushed valid asciinema-v2 prefix (header first). A **read failure on a recording
that was selected** is returned as a wrapped error, **not** silently reported as
absent — "absent" means *no recording exists*, and a manifest must never lie about
that (contrast the genuinely-zero-matches absent case). Every error keeps a stable
`"debugbundle: …"` wrap prefix and returns `nil, Manifest{}, err` (no partial
archive).

## Related

- [ptyrunner-package.md § Session flight recorder](ptyrunner-package.md#session-flight-recorder-pyry_record_dir)
  — the recording source (#552 recorder, #802 persisted gate + fixed location).
- [control-plane.md](control-plane.md) — `internal/control` `RingBuffer.Snapshot()`,
  the log source (same content `pyry logs` returns).
- [update-package.md](update-package.md) — the in-repo `archive/tar` +
  `compress/gzip` precedent (`ExtractBinary`, reader side) this package mirrors on
  the writer side.
- Sibling serving path: #812 (chunked transport), #813 (request verb + authorization).
