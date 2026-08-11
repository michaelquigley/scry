# State History

Scry currently answers one question: what is the state of the estate right now, and when did each check last change. The state file holds exactly one record per check, and every transition overwrites the memory of the one before it. That is the right shape for the pager, and the wrong shape for the operator's other question — what have the states *been*. Was last night's failure the first, or the fourth this month? When did the vpn start getting flaky? Did that "brief" outage actually run four hours? The current model cannot answer, because it was designed to forget.

State history extends scry's memory backwards without changing what scry is. The unit of record is the transition — the event scry already treats as first-class, already persists at the moment it happens, already fans out to notifiers. Instead of overwriting `last_transition`, scry additionally appends every transition to a durable ledger, and the dashboard renders the ledger as per-check state bands over time. Nothing is sampled, nothing is measured, nothing grows when the estate is quiet.

## The Fence, Amended

The seam census fences metrics and time-series permanently: *"the moment it grows time-series storage and graphs it becomes a bad Prometheus."* This design touches that fence deliberately, and the resolution is recorded here.

A transition ledger is not a time-series. Time-series is sampled data — measurements taken on a clock, accumulating whether or not anything happens, meaningful only in aggregate. Transitions are discrete events, sparse by design, each one individually meaningful, and the ledger is empty exactly as often as the estate is healthy. The rendering differs in kind the same way: state bands are reconstructed *exactly* from events, where a Prometheus graph is interpolated from samples. The fence still holds against what it was built to refuse — per-probe result recording, latency series, anything measured — and this spec refuses those too (see Deferred). What changes is the fence's claim to permanence: the operator's judgment call is the actual gate on every addition to scry, and the census language should say so rather than pretending a word can bind future judgment. When this work lands, `docs/current/seam-census.md` gets amended accordingly: the fence guards against *sampled series and their graphs*, the transition ledger is recorded as the judged exception, and "permanent" softens to reflect that fences are standing judgments, not law that outranks the judge.

## The Ledger

History is an append-only JSONL ledger — one event per line, segmented by year, living in a directory beside the state file:

```yaml
history_dir: "~/.local/state/scry/history"   # compiled default; follows $XDG_STATE_HOME
```

A segment is named for its year (`2026.jsonl`) and holds every event stamped in it. At scry's scale — a hand-curated estate of tens of checks, transitions rare by design — a segment is thousands of lines at worst, and five years of history is a filter over a few small files. This is not a database problem, and scry does not grow a database for it. Sqlite remains the known house pattern to copy if a real query need ever appears; the ledger is the shape that keeps files as the truth.

Three event types share the line format. Each line carries its own version, because a segment accretes across binary upgrades and the line is the unit that must stay individually readable:

```json
{"v":1,"ts":"2026-08-10T14:12:03Z","event":"transition","check":"web","kind":"http","from":"ok","to":"late","prev_since":"2026-08-02T07:30:00Z","detail":"connection refused"}
{"v":1,"ts":"2026-08-10T13:00:00Z","event":"start"}
{"v":1,"ts":"2026-08-11T02:00:00Z","event":"stop"}
```

A transition event records the check id, its kind at the time, both endpoints of the transition, and the result detail (omitted when empty, like the state file's optional fields). Every transition is recorded — including the notification-silent ones. An active check's entry into `late` is silent for the pager and real for history; the announce decision belongs to notification, not to memory. `from` earns its place on the line: it is what lets the API resolve a check's state at an arbitrary past instant without walking history from the beginning. `prev_since` — when the from-state began, carried straight off the model's transition — is the existence boundary: no state claim ever extends backward past it, which is how a check born mid-window stays blank before its registration without registration becoming an event.

The engine is the ledger's only writer, appending inside the same serialized apply that persists transition state. The state save leads and the append follows, so a crash between the two can lose at most the final transition from the ledger — an accepted imprecision at the rarest edge: the strip renders the prior state until the check's next transition (for a sticky `failed`, until recovery), while the row and the pager carry the operational truth throughout. The trade is deliberate: the reverse ordering loses nothing, but demands boot-time reconciliation machinery to square the ledger against the resumed state, and at this scale that machinery is not worth its weight. Each append is a single write of a complete line to an append-only descriptor, followed by a file sync; segment creation syncs the directory, in the same discipline as the state file.

Timestamps come from the injected clock, like every other timestamp in scry. Registration is not a transition and produces no event — `prev_since` and `since` carry the birth boundary. Removal needs no event either: the ledger answers only for the configured registry, and history under a name no longer configured is simply ignored — held in the files, warned about once at boot, removed only by the operator's own hand with `scry prune <name>`. A kind change under an existing id keeps appending with the new kind on each line; per-event kind attributes the old bands, and when the operator wants a clean break instead of continuity, prune is the tool there too. Identity continuity is curation, not machinery — this estate is hand-curated, and the curator does not name a new thing after a dead one. The daemon never rewrites the ledger; `prune` is an offline operator command run against a stopped daemon, and it retires the name from both truths — the ledger's transitions and the state file's record — so a still-configured id resumes as a fresh baseline at the next boot, with the `saved` stamp untouched because prune makes no liveness claim. Retention is "keep everything," revisited only if size ever becomes real, which the arithmetic says it will not.

Error tier: one law, the state file's. Scry does not run with history it cannot read or record — a gap it cannot mark is a silent lie in the strip, and a segment it cannot parse is a history it cannot vouch for. At boot, before steady state, the daemon parses the whole ledger — every segment, strict `df/dd` binding per line, taking the orphan census as it goes — then performs its boot appends, which proves the history path writable exactly the way the reconciliation save proves the state path. A segment that does not parse fails boot loudly, naming the file and line; recovery is repairing the file or discarding the *whole* history directory — a fresh ledger's first `start` opens a new vouching boundary, and everything before it honestly renders unwatched. Deleting a single segment is not a recovery: a missing year is indistinguishable from a quiet one, and the closure arithmetic would bridge the hole as watched. `prune` is a curation tool for a healthy ledger — it strict-reads, and refuses a malformed one. There is no degraded history mode. After a successful boot the daemon's own appends are the only writes, so a query-time read can fail only the way any I/O fails, and it fails the request.

## Daemon Lifecycle and Gaps

Transitions alone cannot make the strip honest. If the daemon is down for four hours, the last band would silently stretch across the outage — the ledger cannot distinguish "green all night" from "nobody was watching." So the ledger also records the daemon's own lifecycle: a `start` event on every boot, a `stop` event on clean shutdown. The renderer paints the span between a `stop` and the next `start` as an unwatched gap — no state claim at all. The same treatment applies before the ledger's first `start`: scry may well have been running before history landed, but the ledger cannot testify to it, so pre-ledger time is unwatched, never backfilled from live records.

Unclean death needs one more piece, because a crash writes no `stop`. The state file gains a `saved` timestamp stamped on every write, and the scheduler's 60-second periodic flush becomes unconditional, so the state file is refreshed on a steady cadence whether or not anything is dirty. At boot, when the current segment does not end in `stop`, the daemon appends the missing `stop` back-dated to the later of the state file's `saved` stamp and the ledger's newest event, before appending its `start` — the ledger's own last line is liveness evidence exactly as `saved` is, whichever is later marks the last instant anything testified the daemon was alive, and the gap is bounded to within about a minute of the actual death using only the daemon's own recorded arithmetic, no wall-clock guessing. (When no `saved` stamp exists — the pre-stamp upgrade boot, or a crash before the first stamped save ever lands — closure falls back to the newest ledger event's timestamp: everything after the last recorded evidence is unknown, so the gap opens there. The boot sequence never produces two `start`s without a `stop` between them.)

The alternative — a periodic liveness mark appended to the ledger itself — was considered and rejected: a record that grows on a clock while nothing happens is exactly the time-series shape the fence refuses. The state file already breathes; letting its breath carry the liveness bound keeps the ledger purely eventful.

## The History API

History is served by the status listener as a new contract route, additive per the existing law. `/api/status` remains the single walk of current state, and gains exactly one additive field of its own: `started`, the daemon's current boot time — the same instant as the ledger's `start` event — which is what lets an open page notice a restart that no check transition would reveal:

```
GET /api/history?from=<rfc3339>&to=<rfc3339>
```

Omitted bounds are resolved inside the daemon's one consistent cut: an omitted `to` is the very clock reading stamped on the document as `generated`, and an omitted `from` is 90 days before the *resolved* `to` — so the parameterless default is the render window ending now, and a from-omitted historical query means the 90 days ending at its `to`. Validation runs once, on the fully resolved pair: `from` must precede `to`, and `to` must not pass the clock reading — the same rejection whichever bound caused it, so no one-sided request can reach the ledger inverted. The document:

```json
{
  "estate": "hq",
  "generated": "2026-08-10T14:59:04Z",
  "from": "2026-05-12T14:59:04Z",
  "to": "2026-08-10T14:59:04Z",
  "watching_at_from": true,
  "checks": [
    {
      "id": "web",
      "kind": "http",
      "since": "2026-08-10T02:40:12Z",
      "state_at_from": "ok",
      "state_at_to": "ok",
      "events": [
        {"ts": "2026-08-10T02:10:11Z", "kind": "http", "from": "ok", "to": "failed", "prev_since": "2026-08-02T07:30:00Z", "detail": "connection refused"},
        {"ts": "2026-08-10T02:40:12Z", "kind": "http", "from": "failed", "to": "ok", "prev_since": "2026-08-10T02:10:11Z", "detail": "200 in 84ms"}
      ]
    }
  ],
  "daemon": [
    {"ts": "2026-08-09T03:11:02Z", "event": "stop"},
    {"ts": "2026-08-09T07:30:44Z", "event": "start"}
  ]
}
```

`state_at_from` is the contract's load-bearing subtlety: the band *before* a check's first in-window event is drawn from it, and the daemon computes it — the page never reconstructs state from outside the document. Resolution: the latest event at or before `from` names it by its `to`; failing that, the earliest event after `from` whose `prev_since` is at or before `from` carries it as its `from`; failing that, a live check whose record `Since` is at or before `from` is in its current state; otherwise it is null and the check did not exist at `from`. Backward extension always caps at `prev_since` — what makes "blank before the check existed" computable rather than merely promised. The tail is a pair: `state_at_to` and `since`, the state at `to` and the instant that state began, resolved together by the same rules as `state_at_from` — a resolving event contributes its own timestamp, a look-ahead contributes its `prev_since`, and the live record contributes its `Since` only as the no-events last resort; for the default window ending at now the pair is simply the check's current state and current since. The tail band is exactly `state_at_to` from `since` forward, both null together when the check did not yet exist at `to`, so the whole strip renders from this one document; the status document's only roles at the page are refetch invalidation and the display clock. Daemon lifecycle events arrive in their own estate-scoped array, since they belong to no check; the document-level `watching_at_from` says whether the daemon was watching as the window opened — the fact the renderer cannot infer when the telling `stop` fell before `from`. Every transition event carries the `kind` the check had when it fired; the entry's top-level `kind` is the registry's convenience value, and a window spanning a kind change stays honestly attributed by the events themselves.

The document is assembled as one consistent cut: the daemon serializes the window scan with the engine's own apply loop, so no document can miss an event older than its own `generated` — the invalidation arithmetic the page depends on holds by construction. The document speaks only for the configured registry: ledger events under names no longer configured are ignored, warned about once at boot, and served to nobody — `scry prune <name>` is how the operator retires them for good. Requests with inverted bounds are rejected, and so is a `to` beyond the daemon's now — the document never claims a state for time that has not happened. There is no range cap, because the operator owns the box and the whole ledger is small.

Contract-first as always: the route is authored in `internal/api/specs/scry.yml`, `make generate` regenerates both the ogen server and the dashboard's TypeScript types, and evolution stays additive.

## The Dashboard Strip

Each check row gains a thin state band strip — the classic status-page strip, rendered from exact intervals rather than sampled buckets. Fixed 90-day window to start, matching the API default; the eye pass will tune it once it is on the page. Bands take the existing state colors from the house palette. Unwatched gaps render in a visually distinct non-state treatment — it must read as *absence*, not as health or trouble. Before a check existed, the strip is simply empty — and where unwatched time overlaps a blank span, unwatched wins: a daemon that cannot testify claims neither a state nor nonexistence.

The strip refetches under one level-triggered condition: a single dirty flag, set when a status poll shows a `last_transition` newer than the history document's `generated` or a `started` the document was not fetched under — a restart writes its gap into the ledger without producing any transition, and the boot stamp is how an open page hears about it — and set again by any failed history fetch, the missing document at first load being the same flag rather than a special case. It clears only on a successful fetch, and every successful status poll retries while it is set. While dirty, the strip caps at its document's `generated` and renders the remainder unwatched — the stale-status treatment reused, because a document the page knows is obsolete deserves exactly the honesty of a document the page failed to refresh. The estate's quiet is the page's quiet, and no second polling cadence appears. The display window slides with the page's one live clock — the same arithmetic the header already does: the right edge is the latest status document's `generated` plus its locally elapsed age, the left edge is the right edge minus the 90-day window, and every interval is clipped to those bounds, with the tail band extending to the right edge. Old incidents age out the left side as the window advances, and the two clock frames never mix. The strip inherits the page's stale honesty: while status polling is failing, bands cap at the last successful document's `generated` and the span from there to the right edge takes the unwatched treatment, growing visibly with the outage — the strip never claims time the daemon cannot vouch for. When polling recovers, that span either vanishes (a network blip; the daemon was fine all along) or a changed `started` triggers the refetch that renders the real recorded gap.

A per-check detail view — longer horizons, zoomable windows, the transition list with details — is a natural follow-on once the strip exists, and is deferred rather than designed here.

## Scenarios

**The overnight flap.** `nas-snapshot` fails at 02:10 and recovers at 02:40. The pager already told the story at 02:10 and 02:40; the strip tells it at 09:00 — a thirty-minute red band in an otherwise green quarter, with the two-per-month rhythm of previous bands visible at a glance. The operator reads duration and frequency without scrolling a notification channel backwards.

**The power loss.** The HQ host dies uncleanly at 03:12; power returns at 07:30. The last unconditional flush stamped `saved` at 03:11, so boot closes the ledger with a `stop` at 03:11 and opens with a `start` at 07:30. The strip shows four hours of unwatched gap — not four hours of confident green over an estate nobody was watching.

**The retrospective.** In March next year, the question is "when did the vpn checks start getting flaky." The answer is a window query over `2026.jsonl`, served from the daemon's one consistent cut — no index maintained, no database attended to in the meantime.

## Seam Census

- **the fence (metrics / time-series)** — *amended, not crossed.* Transition ledger recorded as the judged exception; the fence continues to refuse sampled series, per-result recording, and measurement. The census language softens "permanent" to standing judgment. Decided 2026-08-10. Revisit: never for the distinction; the fence itself remains the operator's judgment as everything is.
- **model / render** — *separate.* The daemon owns history arithmetic (`state_at_from`, gap closure); the page owns pixels (band drawing, gap treatment, window presentation). The page learns everything from the document. Revisit: never.
- **model / transport** — *separate.* Ledger lines are persistence format, the history document is contract format, and the model knows neither; the engine hands events outward the way it already hands transitions. Revisit: never.
- **contract circumvention** — *enforced.* The engine is the ledger's only writer; every reader — dashboard included — goes through `/api/history`. Nothing reads the segment files around the API. Revisit: never.
- **error by tier** — *one law with the state file.* History that exists but does not parse whole is fatal at boot; a failed append is fatal at runtime — scry does not run with history it cannot read or record. Post-boot query reads fail only as I/O fails, request-scoped. Decided 2026-08-10, round 7, deleting the split read tier and the degraded serving mode it required. Revisit: if a failure class appears that fits neither.
- **identity continuity is operator-owned** — *curation, not machinery.* History under unconfigured names is ignored and warned, never fenced by recorded lifecycle boundaries; a reused or re-kinded id inherits prior history unless the operator prunes it, and `scry prune <name>` is the explicit tool. Decided 2026-08-10, round 5, superseding the removal-event design of earlier rounds. Revisit: if id reuse ever bites in practice.
- **history reads serialize with the engine** — *one cut.* `/api/history` is served by a read-only command inside the engine loop, capturing `generated`, the ledger window, and the records together, because its document spans two stores no published copy can join consistently; the status route keeps the published-snapshot law untouched. Decided 2026-08-10, round 3. Revisit: if history request latency ever matters.
- **retention** — *keep everything.* No size- or age-based retention machinery; `prune` retires a name, not an era. Revisit: if segment size ever becomes real, which the event arithmetic says it will not.

## Deferred (and Why)

**Sqlite.** Deferred, with the path lit: several house projects carry the sqlite pattern, so if a consumer ever needs queries a lazy window-scan cannot serve, the pattern is a copy away. The ledger is the v1 call because the volume math makes a database pure overhead and the append-only file keeps scry's low-maintenance property intact.

**Per-result recording.** Refused, not deferred — that is the fence itself. Recording every probe result is sampled time-series and voids the design.

**The detail view.** Longer horizons, zoom, and the per-check transition list wait until the row strip is on the page and the eye pass has judged it. The API already serves arbitrary windows, so the detail view is render work when it comes.

**Retention machinery.** None. Keep everything; size-based retention is deferred until size is a demonstrated problem, and yearly segments make it trivial when it comes (delete old files). `scry prune <name>` is curation, not retention — it retires a name, not an era.

**Backfill.** None exists and none is possible — history begins the day the ledger lands. The strip's earliest band starts at the first recorded event, and the time before that renders unwatched — never claimed.

**Ledger liveness marks.** Rejected in design: a heartbeat appended to the ledger would make the record grow on a clock while nothing happens — the time-series shape in miniature. The state file's `saved` stamp carries the liveness bound instead.
