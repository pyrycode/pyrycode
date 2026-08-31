# initialize_control_names_test.go
- `initialize_control_names_test.go` (#1696, extended #1712, #1722 and #1763) — **the
  fourth fixture-name lock in this package. #1696 built it for a single-input
  namer; #1712 gave the file a second, arm-carrying one and corrected the
  header section that used to say the family would stay single-input; #1722
  is what finally gives the arm-carrying namer a caller and turns the arm
  identifier list into a row table; #1763 grows that row table into its
  declared two-column shape and retires the single-arm marker #1722 added.**
  #1688 spent
  live tokens capturing claude's `initialize` `control_request`/`models` round
  trip; #1696 minted the filename those bytes land under —
  `initialize_control_v<slug>.json` via `initControlFixtureName`, reusing
  #1661's `poolRevokeNamePattern` row type and `anchorFixtureName` — and proved,
  with no claude binary and no disk I/O, that no minted name can join
  `fixtureGlob`, `dropcapFixtureGlob`, or #1595's `setModeFamilyGlob`. At the
  time, `poolRevokeFixtureName` (#1661) carried an arm parameter because #1643
  had three arms and this capture had one, so #1696 deliberately collapsed the
  arm dimension: one input, lock table tokens × 1. That was correct for a
  one-probe family and expired the moment #1715 gave the same capture three
  arms — through the one-input namer alone, all three arms mint the same path,
  the last write wins, and the other two vanish with nothing red anywhere.
  #1712 added `initControlArmFixtureName(versionToken, arm string)` alongside
  it (never replacing it — every current caller of the one-input namer stayed
  unchanged until #1722 migrated `writeInitControlFixture` onto the
  arm-carrying one) plus the read-only `initControlArms` declaration naming
  #1715's three send-point arms (`before_first_turn`, `after_completed_turn`,
  `control_no_request`), and a five-subtest lock proving: no minted name joins
  a committed family; every pattern's control still matches; no minted name
  collides with what `initControlFixtureName` mints from the same token
  (string equality, since `testdata/initialize_control_v*` is addressed by no
  glob at all); distinct declared arms mint distinct names; and every name is
  one clean path component for hostile inputs in both the token and arm
  columns. Registered in `finOfflineExecBans` with #1661's list plus
  `writeFixture`, `captureClaudeVersion`, and `os.LookupEnv` — the middle one
  because it is the package's own `claude --version` exec and returns exactly
  this namer's input, making it the exec a developer touching version tokens
  is likeliest to reach for. #1712 added no new banned names: the second
  namer performs no I/O either, so the existing seventeen-name entry already
  covered it.

  #1722 grew `initControlArms` in place from a `[]string` into a
  `[]initControlArm` of `{id string; probed bool}` rows — the growth point
  #1712's own doc comment named, rather than a second table keyed by the same
  names. `probed` marked the one arm `initialize_control_probe_test.go`'s
  single-arm run actually sent at; `initControlProbedArm` ranged the table
  and returned that row's `id`, or `""` when zero or more than one row
  carried the marker. The identifiers, their order and their meaning were
  unchanged — that was a shape change, not a vocabulary change — and the
  field was always meant to go away the moment a real three-arm rig ranged
  the table, at which point the arm becomes a parameter and no row is
  special. #1763 is that rig.

  #1763 grew `initControlArm` again, from `{id string; probed bool}` into
  `{id string; sendPointAfterFirstTurn bool; sendsRequest bool}` — two
  independent columns rather than a single marker, since a live three-arm
  run needs to know both *where* in the fixed two-turn drive sequence an
  arm's send point sits and *whether* a control request is actually written
  there. `control_no_request` carries `sendPointAfterFirstTurn: true` with
  `sendsRequest: false`: its anchor is read at the same point the other two
  arms write, which is what keeps the three arms' recorded windows
  comparable. `probed` and `initControlProbedArm` are deleted with the
  single-arm selector they existed only to serve, and `runInitControlChild`
  (`initialize_control_probe_test.go`) now takes `arm initControlArm` as a
  parameter instead of resolving one internally. The table's own doc bans
  the two shortcuts a change like this reaches for first — a second table
  keyed by the ids, or a `switch` on `id` inside the driver — and #1763 uses
  neither: the send point is one closure called from exactly one of two
  `if`s, keyed off `sendPointAfterFirstTurn`, with the write itself gated by
  `sendsRequest` alone.

  **Lessons that outlive these tickets:**
  - **Collapsing an input dimension can silently empty the hazard shape a
    lock measures — the collapse itself needs a recorded reason, because the
    fix is to re-expand it later, not to leave it collapsed.** #1661 covered
    path-separator escape through its `hostileArms` list, not its token list;
    #1643's arm names were the separator-bearing inputs, not its version
    tokens. Dropping the arm parameter in #1696 was correct for that ticket
    and it also removed nearly every `/`-bearing input from the table — a
    token list of plausible `claude --version` strings plus `..` and `""`
    leaves the whole file green against a namer that never calls
    `versionSlug`. When #1712 put the arm dimension back for a second namer,
    the separator-bearing shapes came back too, in both the token *and* the
    arm columns, because both inputs now reach the name. The general form:
    when a split drops a dimension a predecessor used to cover a property,
    record *why* the collapse is currently safe — the next reader needs that
    reason to know when it stops being safe, not just the fact that it was
    collapsed.
  - **A token table inherited across a namer whose glob-anchoring shape
    changed can silently stop exercising the mutant it exists for.** #1712's
    family-glob subtest needs tokens that smuggle a *different* fixture
    family's name past the new namer's literal prefix — but #1696's own
    smuggle tokens were the bare family heads (`permission_protocol`,
    `set_permission_mode`, `dropped_lines`), and those mint nothing that
    matches e.g. `set_permission_mode_v*_*.json`: the pattern needs a literal
    `v` immediately after the head, and the bare head never supplies one.
    Measured by substitution: swap #1712's `_v1`-suffixed smuggle tokens back
    for #1696's bare heads and the whole file goes green, all five subtests —
    the one mutant the family-glob subtest exists to catch stops existing.
    Any future addition to a family-glob token table must re-check the
    pattern's exact anchoring, not just reuse the vocabulary of a working
    table from a sibling lock.
  - **A pattern-based lock cannot cover a committed family that no glob
    describes.** True through #1763: `testdata/initialize_control_v*` was
    matched by no pattern in this package — it was addressed only by exact
    filename — so the family-glob subtest sweeps straight past the one
    collision that is actually reachable by a live #1715 run. (#1764 later
    gave the arm-carrying half of this family its own glob,
    `initControlArmFixtureGlob`, once a comparison test needed discovery
    that didn't hard-code a version token — see that entry below. The one
    legacy unarmed capture, `initialize_control_v2.1.239.json`, still has
    no glob: `filepath.Match` needs a literal `_` after the version
    segment, and that file has none.) That gap needed its own
    string-equality subtest (`initControlArmFixtureName(token, arm) !=
    initControlFixtureName(token)`) rather than a fourth glob; the empty-arm
    token/arm pairing is the only row that reddens its mutant (drop the
    separator between the two slugs and an empty arm makes the two namers
    agree). Before assuming three globs plus a pairwise-distinctness check
    cover a fixture family end to end, check whether the family is matched by
    a glob at all — a committed capture addressed by exact name only needs an
    equality check, and no amount of pattern coverage substitutes for it.
  - **`-overlay` cannot verify a check that parses source at run time**, and
    this is a different reason than #1634's daemon-side overlay gap above.
    `TestFinOfflineFilesReachNoExecHelper` calls `parser.ParseFile` with a
    `nil` source, so it reads the registered file off disk at test run time;
    `-overlay` is a build-time mapping consumed by the `go` command and
    never interposes on the test binary's own reads. A banned call injected
    via overlay compiles cleanly while the check parses the unmodified file
    and stays green — a misleading pass reading as "the ban does not bite."
    Verifying a ban entry in this file needs a real edit and a real revert,
    confirmed byte-identical against the pristine file before committing;
    `-overlay` stays correct for the ordinary compiled-code namer mutants
    (both files' worth, as of #1712).
  - **A degenerate-table selector can collapse its distinct failure modes on
    purpose, if the collapse buys one sole-red instrument instead of two
    partial ones.** #1722's `initControlProbedArm` returns `""` for both a
    zero-marked and a multi-marked `initControlArms`, so one non-emptiness
    assertion is the sole red for both shapes instead of needing a second
    "exactly one row is marked" count that would otherwise be the only thing
    catching one of them. The cost lands in the failure message, which has
    to name both possible causes since the selector itself cannot tell the
    reader which one produced the empty string — worth the trade whenever a
    selector's callers only need "did this resolve", not "how did it fail to
    resolve."

  Zero production files touched by either ticket. See
  `docs/specs/architecture/1696-initialize-capture-fixture-name-lock.md` for
  #1696's full design and per-mutant table, and
  `docs/specs/architecture/1712-arm-carrying-initialize-fixture-name.md` for
  #1712's. The write half is #1702
  (#1697, named here at the time, was superseded and closed before it was
  built); #1713 closed as a split and #1722 is what performs the migration
  onto `initControlArmFixtureName`, described in the writer entry below.
