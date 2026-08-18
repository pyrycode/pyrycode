# Release tooling

Operational reference for cutting a `pyry` release. The release runs from a
`v*` tag push and is driven by `.github/workflows/release.yml` +
`.goreleaser.yaml`. This doc covers the two secrets those files depend on.

## Secrets used by the release workflow

| Secret | Purpose | Custody |
|---|---|---|
| `PYRY_RELEASE_SIGNING_KEY` | Ed25519 private key (PEM) that signs `checksums.txt` | **Tightest** — see below |
| `HOMEBREW_TAP_TOKEN` | PAT with `repo` scope to push the formula to `pyrycode/homebrew-tap` | Repo Actions secret |

Both are repo Actions secrets on `pyrycode/pyrycode`. They are **distinct**
secrets: a stolen tap token must not be able to read the signing key.

## Checksums signing key

`pyry update` verifies `checksums.txt` against a public key compiled into the
binary (`releaseSigningPublicKeyHex` in `cmd/pyry/update.go`) before it trusts
any digest in the file. A compromised release or a stolen publishing token
therefore cannot install attacker-chosen bytes: without the private key an
attacker cannot produce a signature that verifies. The private key's whole
value rests on custody, so keep it out of reach of the publishing path.

### Generate the keypair

```sh
# Private key (PEM) — this is the CI secret. Never commit it.
openssl genpkey -algorithm ed25519 -out signing_key.pem

# Raw 32-byte public key, hex-encoded — paste into releaseSigningPublicKeyHex.
openssl pkey -in signing_key.pem -pubout -outform DER | tail -c 32 | xxd -p -c 32
```

### Install it

1. Paste the hex public key into `releaseSigningPublicKeyHex` in
   `cmd/pyry/update.go`. The `TestReleaseSigningKey_Decodes` unit test checks
   it decodes to 32 bytes, catching a typo before release.
2. Store the **private** PEM (the full `signing_key.pem` contents, `BEGIN
   PRIVATE KEY` through `END PRIVATE KEY`) as the `PYRY_RELEASE_SIGNING_KEY`
   Actions secret on `pyrycode/pyrycode`.
3. Delete your local copy of `signing_key.pem` once it is in the secret store.

### Custody rules

- **Never commit and never log the private key.** In CI it flows
  secret → temp file (`0600`, under `${RUNNER_TEMP}`, destroyed with the job)
  → `openssl -inkey`. It never appears on stdout or in an error message.
- **Tighter custody than publishing.** Do not expose
  `PYRY_RELEASE_SIGNING_KEY` to untrusted or third-party workflow steps; it is
  only injected into the `goreleaser` step of the release workflow. If the
  publishing token (`HOMEBREW_TAP_TOKEN`) and the signing key were equally
  reachable, the signature gate would add little.
- The public key is not secret — it ships as source.

### Keep public and private matched

Regenerating the keypair means updating `releaseSigningPublicKeyHex` **in the
same change** as rotating the `PYRY_RELEASE_SIGNING_KEY` secret. A mismatch
makes every `pyry update` fail closed (safe, but broken) until a release built
from the new public key ships. Rotation/revocation beyond
"regenerate + rebuild + re-release" is a manual procedure; there is no
key-id or multi-key trust store by design (single algorithm, single trust
root, no downgrade vector).

## First-signed-release smoke test

Unit tests sign fixtures with a throwaway key, so they cannot exercise the
real CI signer config (the OpenSSL invocation + the `PYRY_SIGNING_KEY_FILE`
secret wiring). After the first release that emits `checksums.txt.sig`, run an
update against it once from a machine already on an older signed build:

```sh
pyry update --check   # confirms it reaches the release
pyry update           # must print "==> Verifying signature... ok"
```

If the OpenSSL-produced signature does not verify against the baked-in key,
the update fails closed with `update: verify signature: …` (never insecure).
`openssl pkeyutl -sign -rawin` requires OpenSSL 3.0+, which `ubuntu-latest`
provides; a pinned older image would surface here.

## Live-claude suite — read the count, not the exit code

`make e2e-realclaude` drives real `claude` over the subscription. It has two
failure modes that report success, and from outside they look the same as a
clean run:

- **No credentials.** Every live test skips and the run exits 0.
- **The package does not build.** Zero tests run and the run still exits 0
  through any shell wrapper.

A third mode — the suite replaying a cached `ok` without spawning
anything — existed here until #1501 added `-count=1` to this recipe (and to
`e2e-liverelay`, `e2e-install`, `e2e-update`, the other three opt-in recipes
with the same blind spot). This suite builds its `cmd/pyry` binary under test
with a **subprocess** `go build`, whose file reads never enter the test
binary's cache key — so an edit confined to `cmd/pyry` left the key
unchanged and `go test` replayed the previous `ok` without spawning claude at
all. `-count=1` forces every invocation to execute for real; it does not
change what the count-not-exit-code check below needs to catch, since a
build failure still exits 0 with nothing run.

So the only trustworthy reading is the number of tests that executed. Count the
`=== RUN` lines. A healthy full run is in the 700s as of August 2026, was 521 on
2026-08-09, and reads zero when the build is broken.

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...      # subscription login; without it everything skips
make e2e-realclaude 2>&1 | tee /tmp/e2e.log
grep -c '^=== RUN' /tmp/e2e.log         # this is the number that matters
grep -cE '^\s*--- FAIL' /tmp/e2e.log
```

**About a dozen tests skip by design.** Some are opt-in evidence probes behind
their own environment flags, and each says so in its own skip message. The MCP
smoke tests need `ANTHROPIC_API_KEY`, the metered API credential, which is a
different thing from the subscription login. Read the skip reasons; the skip
count alone cannot distinguish design from breakage.

**`make check` does not compile this package.** It is behind the
`e2e_realclaude` build tag, so the standard gate is blind to it and can be
green while the package does not build. `make preship` is the gate that
compiles it, and it is the one to run after deleting or moving test files —
a deleted test file takes its shared helpers with it, and other tests may be
calling them.

## Live-relay smoke test

`make e2e-liverelay` proves the daemon ↔ **real** `pyrycode-relay` pairing with
one automated round-trip: device identity → WS connect → Noise_IK handshake →
a `list_conversations` verb round-trip. It fails if any stage errors or the
reply does not match the seeded conversation — not a no-panic smoke check. This
closes the gap that every other relay e2e test leaves open by running against
the in-process `fakerelay`: the deployed relay server is otherwise exercised by
no automated test.

```sh
make e2e-liverelay
```

**What it requires — no credentials.** Unlike `e2e-realclaude`, this test spends
**no** real resources and needs **no** secrets. It builds and spawns a
`pyrycode-relay` binary hermetically on a loopback-bound (`127.0.0.1`) plaintext
listener, mints an **ephemeral** device identity under a temp `HOME` (destroyed
when the test ends), and tears the relay + daemon down on cleanup. Nothing is
persisted, nothing reaches production.

It resolves the relay binary in this order:

1. `PYRY_LIVERELAY_BIN` — path to a prebuilt `pyrycode-relay` binary.
2. `PYRY_RELAY_REPO` — path to a `pyrycode-relay` checkout to `go build` from.
3. Default: the sibling `../pyrycode-relay` checkout (next to this repo).

If none is present the test **skips loud** with a named diagnostic (it does not
fail), so `make preship` stays green on a machine without the sibling. Get the
sibling with:

```sh
git clone https://github.com/pyrycode/pyrycode-relay ../pyrycode-relay
```

**Where it sits in the release flow.** Because it is offline-capable and
credential-free, it is wired into **`preship`** (`check → e2e-realclaude →
e2e-liverelay`), run before every `~/.local/bin/pyry` swap. It is **not** part
of `check` or the `e2e` target — the `e2e_liverelay` build tag keeps it out of
`go build`/`go vet`/`make test`/`make e2e`.

## Install & update e2e suites

Two real, maintained e2e suites are reachable only by opt-in build tags and are
**deliberately excluded from `check` and `preship`** — run each on purpose, on
the release checklist and before touching the code it exercises.

### `make e2e-install`

```sh
make e2e-install   # go test -tags e2e_install -count=1 ./internal/e2e/...
```

Drives a real install round-trip: writes the service file via `install.Install`,
brings the daemon up through the OS service manager, exercises `pyry status`
against it, then tears it down. The `darwin`/`linux` build tags mean only the
host platform's install test compiles and runs under one `e2e_install`
invocation — launchd (`launchctl bootstrap gui/<uid>`) on macOS, systemd
(`systemctl --user`) on Linux.

**Footprint: it mutates the real user launchd/systemd domain.** Run it only on a
host you control, never where a live `pyry` daemon must stay untouched. On Linux
it **skips loud** (does not fail) when `systemctl --user is-system-running`
reports an unusable session; on macOS `gui/<uid>` needs a logged-in GUI session
for the running uid.

When: on the release checklist, and before touching install-service code
(`internal/install`, `pyry install-service`).

### `make e2e-update`

```sh
make e2e-update   # go test -tags e2e_update -count=1 ./cmd/pyry/...
```

Drives the full `pyry update` flow — fetch → verify → atomic binary replace →
daemon restart — against an **in-process fake release server**, then asserts the
binary inode and daemon PID changed and the post-update daemon still answers
`pyry status` / `pyry sessions list`. The suite lives in `package main`, so it
is scoped to `./cmd/pyry/...` rather than `internal/e2e`.

**Footprint: hermetic.** It spawns and restarts a real daemon, but everything —
binary, HOME, socket — lives under a temp directory destroyed on cleanup. It
spends no real resources, needs no credentials, and touches nothing outside the
temp dir. Unlike `e2e-install`, it does **not** mutate the real user domain.

When: on the release checklist, and before touching `pyry update` code
(`cmd/pyry/update*.go`, `internal/update`).
