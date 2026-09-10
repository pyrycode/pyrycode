# Development verification

Practices shared by Claude, Codex and the pipeline. These consolidate recurring
review findings. Historical ticket examples remain in the vault archive; current
source and the owning package documentation decide current behaviour.

## Establish the change surface

Count construction sites and consumers, not only type declarations. A payload field
can require several builders and narrow interfaces to change. Include test doubles
when widening a function type. A named configuration field may avoid breaking every
function literal, but check whether that shape suits the contract.

A matching method name does not prove two interfaces are the same dependency.
Trace the field's declared type and its assigned implementation before counting hits.
Codegraph has previously missed cross-package callers and selected the wrong symbol
when names collide. Confirm the package identity and supplement its edges with a
source search for qualified calls. The canonical index cannot see pending edits.

A new event variant has consumers beyond exhaustive test tables. Search both its
marker interface and the type switches. Default branches can silently ignore a new
variant even after the build passes. Check logging names, payload mapping, lifecycle
handling and internal busy-state tracking separately.

## Verify inherited premises

A merged dependency may have implemented more or less than the ticket predicted.
Find the production installation and the actual call site. Read existing tests before
rebuilding the same proof. Search specifications for explicitly deferred vocabulary,
missing producers and unwired callers before estimating the remaining work.

Read the validator's complete rejection behaviour before copying it. A membership
check and a live-session routing check serve different requests. Inspect the sibling
that deliberately avoids validation; its explanation often states the boundary of
an exception more clearly than the validating example.

Before copying a sibling's late guard, trace the target operation to its first side
effect. An operation that already returns without changing state may not need that
guard. A shared request shape does not establish identical side effects.

## Check searches and citations

An empty search is not proof of absence. Run a known-present control with the same
flags and paths. Use shell arrays for path lists. Use fixed-string matching for
literal text and check the regular-expression dialect before relying on boundaries.
Search a short fragment when a quoted comment may wrap across lines, then read the
named declaration. Check that cited symbols still exist and still support the claim.
Keep historical citations in historical documents unchanged.

When correcting a behavioural claim, search prose as well as symbols. Counts, key
lists, test headers and comments can encode the same claim without sharing its words.
Run dependent tests and compile relevant build-tagged packages. A green assertion
can retain a false rationale after the behaviour it described has changed.

## Prove that tests distinguish the change

A real capture can already exercise an older matching branch. Check the test against
the pre-change implementation. Remove the older discriminator while preserving the
new one, and confirm that the input mutation actually changed the bytes.

For combined predicates, exercise each branch independently. A shared log site cannot
prove which branch was taken if both inputs satisfy the first condition. Use an
independent observable result of the path under test.

Read the consumer above a mapper. A pure payload mapper does not decide whether a
frame is emitted. A whole message family can be ignored before subtype handling, so
absence of an error frame cannot prove which subtype branch ran. Child stdout,
client-visible frames and internal turn state are separate observations.

A verifier can use Go's `-overlay` option with replacement files outside the worktree
to test a mutation without editing the branch. Assert that each replacement applied.
Run the named test being evaluated so a neighbouring assertion cannot mask its weakness.

## Protocol boundaries

Round-trip tests must marshal the decoded payload back into the envelope. Comparing
an untouched raw payload with itself does not check struct tags or new fields.
Explicitly assert decoded field values. Distinct fixture values detect accidental
field swaps. To distinguish an absent key from a present null, inspect raw JSON too.
The current protocol test helper uses `json.Compact`, which removes whitespace but
preserves key order; do not describe it as sorting keys.

An aggregate envelope-fit test and a producer's single-field limit prove different
things. Reference the constant in the package that owns it. A second literal in
another package can remain green after the real limit changes.

Check every field before widening a provenance or logging claim. Metadata derived
by the daemon does not make an echoed client identifier daemon-authored. A queue's
client-visible text can differ from the delivered prompt, which may contain a host
path. Use the client-safe projection when persisting or serving conversation history.

When declaring a frame ahead of its producer, record that state in both the protocol
table row and the section text. Update both when emission lands. Review current group
counts and their other mentions, using the last correction as a locator. Leave dated
changelog statements historical.

## Captures and live evidence

An acceptance criterion requiring a capture must be reachable under the gate's normal
invocation. An operator-only environment switch cannot be its sole trigger. Missing
fixtures can arm a capture, with an explicit force switch for later recapture. Validate
the evidence before writing it, and keep the observed version consistent with the
reader's path and pin.

A committed-fixture reader must fail when its fixture is missing. Do not copy a skip
state from a probe that was written before evidence could exist. Conversely, a capture
that stops running after its fixture exists can retain stale assertions indefinitely.
When changing the parser's result for its frames, inspect the disarmed probe and compile
the tagged suite even if no new live capture is needed.

Record enough redacted context to diagnose an empty capture. Count other line types,
subtypes, tool outcomes and timing without retaining secret-bearing inputs. A witness
controlled by the test rig is stronger than a model being asked to wait. Ensure its
release path cannot hang when no reader starts.

Read the evidence and the expression behind a conclusive flag. A boolean can omit the
very observation that changes its interpretation. Reusing a decoder does not validate
an unobserved envelope key; a wrong key can silently produce an empty result.

## Test execution and artifact survival

An exit code alone does not prove tests ran. Read named results, counts and skip reasons.
A local authentication failure says nothing about the separately launched live gate.
Use the gate's own results to establish what it exercised. Current role instructions
control whether agents or the dispatcher run each test tier.

Before accepting a baseline comparison as attribution, compare suite composition and
fixture state. Re-running one failing reader on the base without its fixture-producing
sibling is not the same experiment. Check for an existing issue before filing another.

A temporary worktree is not durable storage. For an agent-produced artifact, commit it.
For a gate-produced capture, retain an external recovery copy and use the documented
promotion step. Staging alone does not survive worktree removal. Require the committed
reader to reject present-but-unpinned evidence. Read the current release runbook before
claiming that a gate automatically commits generated files.
