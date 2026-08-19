---
title: serialize status poll ordering
state: inbox
created: 2026-08-19
tags: [defect]
log: docs/journal/2026-08-19.md
---

Make the dashboard's held status document monotonic. `poll()` in `ui/src/App.tsx` runs on a bare 10-second interval that never awaits its predecessor, and every response is committed to `statusRef.current` unconditionally — so if a status request outlives the interval and an older response lands last, it overwrites a newer one. The display clock jumps backward, `receivedAt` resets against the older stamp, and the panel's landing rule starts judging arrivals against a "newest status" that is not the newest.

The narrow fix: a pure `supersedesStatus(held, arriving)` in `ui/src/history.ts` — arriving `generated` not older than held — with `poll` returning early instead of committing when it fails, plus acceptance cases beside the existing ordering table. Serializing the poll loop is the heavier alternative and changes the cadence's character; prefer the guard.

## why

Surfaced by terminus during the per-check-history-detail-view arc (round 11) and deliberately deferred: the behavior is pre-existing, predates that work order entirely, and needs a status response slower than the poll interval to trigger. It was left alone rather than widening a panel work order into the status loop.

It is worth doing anyway because two rules the panel now depends on assume monotonicity that nothing enforces: the arrival revalidation re-decides the dirty flag "against the newest status document the page holds," and the panel's two-condition landing rule requires an echoed `to` at or after "the newest status's `generated`." Both silently weaken if the held status can regress.

Not in scope: the stale-flag ordering. An older *failed* poll can still mark the page stale after a newer successful one, but a failure carries no document to order by, and the status poll's retry-and-stale posture is deliberately exempt from the history document's discipline.
