# Writing attachment bytes (#1782)

`Store` (`storage.go`) takes the directory `EnsureDir` returned, a
client-supplied filename, and already-verified bytes, and writes them there
with the house temp-and-rename recipe (temp file created *in* `dir`, `Chmod`
`0o600`, write, `Sync`, `Close`, `Rename`), returning the joined path. It
trusts `dir` completely and adds no resolution or containment check of its
own — re-deriving one would fork the check `EnsureDir` exists to own, and
would break the reason two attachments carrying the same client filename can
coexist: the per-`attachment_id` directory component disambiguates them, not
the sanitised name, which `SanitizeFilename` (#1772) documents as explicitly
not unique. `ErrWriteFailed` is a third top-level sentinel in `storage.go`,
beside `ErrInvalidID`/`ErrNotContained` — not folded into `accumulator.go`'s
grouped block, whose opening sentence scopes it to `Add`/`Assemble`. `Store`
is idempotent (`Rename`, not `O_EXCL`), which is what lets a phone's
reconnect-and-resend overwrite with the same bytes instead of failing. No
production caller yet; #1744 wires the dispatch site.

- **A privacy rule stated as one property across several failure branches
  needs checking branch by branch, not as a whole.** The spec banned a
  rename-failure error from naming the sanitised client filename — correct,
  since `os.Rename` returns `*os.LinkError`, whose `Error()` prints its
  destination unconditionally — and then extended the same reasoning to rule
  out a test on the create-temp branch too, calling it "vacuous on the
  create-temp path where the name is never touched." Code review measured
  it: an overlay mutant touching *only* the create-temp branch's `%q`
  operand (`dir` → `path`) leaked the sanitised filename into the error text
  and the full suite — six tests, ten subtests — stayed green. The
  create-temp branch never names `path` in the correct build, but nothing
  had asserted it couldn't in a wrong one. The reasoning that made one branch
  unpinnable didn't actually apply to the other five; it was accepted for
  all six because it was true for one. When a spec calls an assertion
  vacuous across several code paths, verify the claim against each path the
  assertion would cover, not just the one that prompted the reasoning.
- **An already-extracted, same-shaped atomic-write helper can still be the
  wrong copy target.** `internal/update.AtomicReplace` is an exported,
  doc-commented version of this exact recipe whose signature would have
  dropped in almost verbatim. It was correctly not used: its rename-failure
  message formats the destination path into the text unconditionally (making
  the branch-privacy gap above permanent instead of one CR finding from
  closed), and it collapses five distinguishable failure points into one
  message, losing the per-step `<op>` this package's error strings need for
  the operator's log. The Technical Notes' "ten packages hand-roll this
  recipe, none imports another's — don't extract a shared helper" is
  guidance against building a *new* one; it isn't a green light to reach for
  an existing one without reading what that helper puts in its own errors.
