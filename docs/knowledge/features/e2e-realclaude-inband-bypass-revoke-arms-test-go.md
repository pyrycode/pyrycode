# inband_bypass_revoke_arms_test.go
- `inband_bypass_revoke_arms_test.go` (#1651) — **the deterministic, credential-free
  half of #1643's three-arm substrate: which stored posture each arm launches
  with.** #1622's `seedBypassRegistry` wrote `yolo:true` unconditionally; it now
  takes the posture as a parameter (`seedBypassRegistry(t, path, yolo)`), and
  #1622's own call site is unchanged (`true`). The trap this ticket exists to
  avoid: for the `false` posture, a **stored** false and a **cold start** (no
  registry file at all) both read back as `YOLO: false` through
  `Pool.DefaultSettings()` — that seam cannot tell a correctly-seeded
  `control_default` arm from a completely broken one. The only signal that
  separates them is the registry **entry's existence** (`bootstrap: true`, an
  `id` `sessions.ValidID` accepts), so `TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues`
  reads the seeded file directly rather than the Pool, decoding through
  `map[string]json.RawMessage` — never `revokeSeedEntry` or `registryEntry` — so
  a `yolo` key misspelled in the seed can't round-trip through its own struct and
  hide from the test. `revokeSeedEntry.YOLO` keeps its `json:"yolo"` tag with no
  `omitempty` (unlike `registryEntry.YOLO`'s `json:"yolo,omitempty"`) precisely so
  a stored false is a **present** false key on disk, not an absent one; adding
  `omitempty` to "match" production would make the key vanish, and only the
  test's presence clause (checked separately from the value) reddens on it — a
  value-only check passes, since a struct decode of an absent key is
  indistinguishable from a decoded `false`. The second new test,
  `TestPoolRevokeArms_PinLaunchPostureAndUpdateByName`, pins the package-level
  `poolRevokeArms` table (`revoke`/`control_default`/`control_bypass`, each
  carrying its launch posture and whether it takes a mid-run settings update) by
  **name**, in both directions — deliberately not "the two controls differ",
  since swapping `control_default` and `control_bypass` still leaves them
  differing while making the arm names lie to every downstream consumer.
  `poolRevokeArms` is read-only by convention (ranged over from `t.Parallel()`
  tests in this file and by #1652); nothing appends to or reassigns it. Both
  tests report **PASS**, not SKIP, with no claude binary and no credentials — the
  file takes neither `WithWorktreeAuthenticated` nor `resolveClaudeBin`, enforced
  by a new `finOfflineExecBans` entry over the file's AST rather than its prose.
  **Two lessons surfaced while mutating the presence/value split**, reported
  candidly against the spec's own prediction rather than silently patched: a
  misspelled `yolo` tag reddens the presence clause on **both** rows, not the
  spec-predicted value clause on the `true` row — because the value check is
  nested inside the presence check's `else`, and a nested assertion is never the
  sole red for a mutant that trips its guard; the guard is. And the map-decode
  itself is not the "vacuous value-only check" the spec's hazard prose describes
  for a **struct** decode: `json.Unmarshal(nil, &b)` over an absent key's `nil`
  `json.RawMessage` errors rather than silently decoding to `false`, so a
  map-based value check alone would have caught the vanished key too — the
  separate presence clause earns its place on readability and naming the real
  cause, not on being the only thing that reddens. Zero production files
  touched. See
  `docs/specs/architecture/1651-bypass-seed-posture-and-arm-table.md` for the
  full design, and #1622's entry above for the seam this ticket parameterizes.

- `inband_bypass_revoke_names_test.go` (#1661) — **locks #1643's fixture-name
  family out of #1595's committed one before the live run that could collide
  exists.** #1643's three arm names (`revoke`/`control_default`/`control_bypass`)
  are the same strings #1595 already uses, and #1595's `setModeFixtureName` is
  package-level and reachable — reusing it on the same claude version would
  silently overwrite three of #1595's four committed fixtures while every test
  stayed green. `poolRevokeFixtureName` is a pure two-string namer whose
  `pool_revoke_` prefix is a literal neither input can reach, with **both**
  inputs (not just the version, unlike the #1595 precedent) run through
  `versionSlug` so a hostile arm like `a/b` can't escape containment either —
  an unslugged-but-equally-pure counterfactual namer escapes containment on 40
  of 110 measured pairs, so that property is load-bearing, not green by
  construction. **The property this file exists to prove is a negative claim,
  and a negative claim needs its control anchored the same way it's checked**:
  `fixtureGlob`/`dropcapFixtureGlob` are matched with a `testdata/` prefix,
  #1595's family glob is matched bare, and getting either direction wrong makes
  the "no minted name matches" loop pass unconditionally with nothing checked.
  `anchorFixtureName` is the single function both the negative loop and each
  row's control call, so the two can't drift apart — proven by mutation:
  flipping the family row to `underTestdata: true` produced **zero** reds from
  the negative loop and reddened only its control, i.e. the control was the
  sole detector for a mis-anchored pattern going silently vacuous. Code review
  flagged one residual, left for #1662 rather than fixed here (closed there —
  see below): the file's header claims it can't reach any `os` read or write,
  but its `finOfflineExecBans` entry enumerates four verbs
  (`os.ReadFile`/`WriteFile`/`Create`/`ReadDir`), so `os.OpenFile` and a
  `writeFixture`-shaped third `packageDir` wrapper sit outside the ban table's
  actual coverage — true of this file today (it imports no `os`) but a gap for
  whatever #1662 adds next to this package. Zero production files touched. See
  `docs/specs/architecture/1661-pool-revoke-fixture-name-family.md` for the
  full design and the anchoring table.

- `inband_bypass_revoke_fixture_test.go` (#1662) — **the write half of #1643's
  three-arm substrate: the fixture record one arm commits, and a writer whose
  target directory is a parameter.** `poolRevokeFixtureRecord` carries exactly
  the eighteen fields #1643 can fill — no `env` field, inherited from
  `setModeFixtureRecord`'s constraint, since the credential reaches the child
  through the environment while the argv carries none — and
  `writePoolRevokeFixture` mints its target filename by passing the record's
  slugged version token and arm **unmodified** into #1661's
  `poolRevokeFixtureName`, never formatting its own name. `capFixtureCapture`
  reuses #1595's `stderrFixtureCap`/`truncateString` and adds a rune-boundary
  trim: `encoding/json` substitutes U+FFFD per invalid byte rather than
  erroring on bad UTF-8, so a plain byte cap over a capture cut mid-rune reads
  back over the stated cap — measured, a five-byte cut string round-trips at
  seven bytes. Two lessons surfaced during mutation testing, both reported
  candidly against the design's own predictions rather than silently
  absorbed: **a reused helper's own guard can make the new wrapper's guard
  unpinnable** — `capFixtureCapture`'s `len(s) <= cap` early return is
  measurably dead for the value path, because `truncateString` already
  carries the identical guard and its trim loop breaks immediately on a valid
  tail, so dropping the wrapper's own early return reddens nothing; the
  function's doc comment says so rather than claiming coverage it doesn't
  have. And **`json.MarshalIndent` reflows an embedded `json.RawMessage`**, so
  a `control_response` envelope written and read back is not byte-equal until
  both sides are compacted first — which blinds only that whitespace and
  still catches a dropped field, a `json:"-"` tag, or a wrong-tag decode. The
  round-trip fixture's arm (`"revoke arm/2"`) is deliberately not
  slug-clean: every real arm name and slugged version token already passes
  `versionSlug` unchanged, so a writer that formats its own name instead of
  minting through `poolRevokeFixtureName` would produce the identical name
  and AC 2's name-equals-namer assertion would be vacuous — this is the one
  literal choice that keeps that assertion coupled to the write path.
  `finOfflineExecBans`' entry for the file carries a fourth wrapper beyond the
  `packageDir`/`setModeFixturePath`/`writeSetModeFixture` trio —
  `writeFixture`, the spike's own third `packageDir` wrapper — closing the
  residual #1661 flagged and left open (above). Code review also flagged,
  non-blocking, that "a field decoded from the wrong tag" — carried verbatim
  from the design into the file's header as something non-zero, distinct
  values catch — overstates it for a symmetric struct round trip: only a
  **colliding** tag is caught (`encoding/json` drops both); a unique wrong tag
  round-trips green. Worth remembering for any future file in this family
  that reuses that phrasing. Zero production files touched. See
  `docs/specs/architecture/1662-pool-revoke-fixture-record-and-capped-writer.md`
  for the full design and the mutation-to-assertion table.
