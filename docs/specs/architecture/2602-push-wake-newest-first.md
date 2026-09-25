# #2602 — push-wake: wake the most recently paired device first

## Files read

- `cmd/pyry/push_wake.go` → `pushWaker.wakeAbsent` — the only loop that orders wakes; walks `pushWakeDevices.List()` as returned.
- `internal/devices/registry.go` → `Registry.List` (returns a copy, so sorting it in place is safe), `Registry.save` (sorts oldest `PairedAt` first on disk).
- `cmd/pyry/push_wake_test.go` → `newTestPushWaker`, `fcmDevice`, `TestPushWaker_CoalescesPerDevice` — fakes and the existing order-sensitive assertion.

## Change

In `wakeAbsent`, sort the copy returned by `w.devs.List()` newest `PairedAt` first
(`slices.SortStableFunc`, comparing `b.PairedAt` to `a.PairedAt`) before the send loop.
The relay's per-server-id burst (6) drops everything past it, so the send order decides
who gets woken; dead registrations from old pairings are always older than the live phone.
The sort is explicit rather than reversing `List()`, so it does not depend on the
registry's in-memory order. It is stable, so devices with equal (e.g. zero) `PairedAt`
keep their registry order and `TestPushWaker_CoalescesPerDevice` is unaffected.
Eligibility, coalescing and logging are unchanged. Removing dead tokens stays out of
scope (needs the relay to report FCM's answer).

## Testing strategy

New `TestPushWaker_NewestPairedFirst` beside `TestPushWaker_Eligibility`: seven eligible
devices listed oldest first with distinct `PairedAt`; one pass sends all seven, and the
first send is the newest device's token (full order asserted newest → oldest).
RED against current code (first send is the oldest).

## Documentation handoff

None required by the ticket.
