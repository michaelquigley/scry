# Engine and State

Scry's transport-free core contains the runtime model, exact transition rules, paired notification decisions, one serialized state owner, and the durable JSON state file. Probe implementations live outside this core and submit opaque results through the scheduler; no HTTP, notifier, or rendering type participates in model decisions.

The daemon reconciles state before startup succeeds, then runs the engine, scheduler, isolated ingest listener, and notification dispatcher together. SIGINT, SIGTERM, or a fatal component error cancels every component and waits for shutdown.

## Model

Three check kinds enter one state machine:

- `http` and `tcp` are active checks. Their strategies produce `Result{Status, Detail}` values through the scheduler.
- `passive` checks receive the same result shape from authenticated ingest reports. The silence between reports is judged by pure window arithmetic, not by a probe interface.

Every new check receives a complete baseline at the injected current time: state `ok`, `since` set to registration, no `lastTransition`, no `lastResult`, and zero consecutive failures. Passive checks additionally receive `lastSeen` at registration. Registration is not a transition.

The engine treats result detail as opaque. It reads status to select a transition, stores the complete result, and copies that result onto any transition handed outward.

## Transition Rules

An active failed result increments `consecutiveFails`. The first failure enters `late`; a count greater than or equal to `failAfter` enters `failed`. An ok result resets the counter and enters `ok`. Once failed, a check remains failed across further failures even if configuration raises the threshold; only an ok result recovers it.

A passive report always refreshes `lastSeen` and `lastResult`. A failed report enters `failed` immediately; an ok report enters `ok` from any state. Window evaluation uses the last real `lastSeen`:

- `late` when `now` is strictly after `lastSeen + period + grace`;
- `failed` when `now` is strictly after `lastSeen + period + grace + hardenAfter·grace`.

A sweep only degrades. It can move `ok` directly to `failed` after downtime or advance `late` to `failed`, but it never moves a record back toward ok and never changes `lastSeen` or `lastResult`.

Notification decisions are carried on transitions:

| transition | passive | active |
| --- | --- | --- |
| to `failed` | announce | announce |
| to `late` | announce | silent |
| `late` to `ok` | announce | silent |
| `failed` to `ok` | announce | announce |

## Ownership and Clock

One engine loop owns the mutable record map. Callers submit active results, passive reports, sweep ticks, and flush requests through commands; application, transition detection, and persistence happen serially. Readers use a separately published deep copy in registry order, so neither the status API nor another caller can mutate engine state.

When a transition carries `announce: true`, the engine persists the resulting state before appending a copied transition to the notification dispatcher. Silent transitions never enter the dispatcher. Enqueue does not perform transport work, so a stalled or retrying notifier cannot delay engine commands.

The clock is mandatory at construction. The engine reads it once per input or sweep and passes that time into pure model functions. No package under `internal/` reads wall-clock time; `cmd/scry` is the only place that wires `time.Now`.

## Persistence

The state file is a versioned JSON object:

```json
{
  "v": 1,
  "saved": "2026-07-27T18:04:00-04:00",
  "checks": {
    "web": {
      "kind": "http",
      "state": "late",
      "since": "2026-07-27T18:01:00-04:00",
      "last_status": "failed",
      "last_detail": "connection refused",
      "consecutive_fails": 2,
      "last_transition": "2026-07-27T18:01:00-04:00"
    }
  }
}
```

Optional history fields are omitted until they exist: active checks have no `last_seen`; a fresh check has no last result or transition. The file binds strictly through `df/dd`: malformed or trailing JSON, duplicate or unknown keys, unsupported versions, invalid enum values, and incomplete records all fail the load. A missing file is first boot.

`saved` is stamped on every write and is the file's liveness mark: it records the last instant the daemon is known to have been alive, which is what bounds the unwatched gap after an unclean death (see [the ledger](#history-ledger) below). It is additive and optional, so a file written before the stamp existed still loads — with no stamp, and therefore no liveness claim. The stamp travels into the store as an argument rather than being read from a clock there, which is how the injected-clock rule survives a timestamped file.

Writes create a mode-0600 temporary file beside the destination, sync it, rename it over the destination, and sync the directory. A transition and every passive report save immediately. Non-transitioning active results mark state dirty; explicit flush and shutdown persist them, and the scheduler's 60-second periodic flush persists *unconditionally* — a quiet estate still advances `saved`, because that stamp is the bound on how much time a crash can leave unaccounted for. Any save failure is fatal.

## History Ledger

The state file holds one record per check and forgets the one before it. History extends scry's memory backwards without changing what scry is: every transition is additionally appended to a durable, append-only JSONL ledger under `history_dir`, one event per line, segmented by year (`2026.jsonl`). Nothing is sampled and nothing grows while the estate is quiet — the ledger is empty exactly as often as nothing happens.

Three event types share the line format, and each line carries its own version because a segment accretes across binary upgrades:

```json
{"v":1,"ts":"2026-08-10T14:12:03Z","event":"transition","check":"web","kind":"http","from":"ok","to":"late","prev_since":"2026-08-02T07:30:00Z","detail":"connection refused"}
{"v":1,"ts":"2026-08-10T13:00:00Z","event":"start"}
{"v":1,"ts":"2026-08-11T02:00:00Z","event":"stop"}
```

Every transition is recorded, including the notification-silent ones: an active check's entry into `late` is silent for the pager and real for history, because the announce decision belongs to notification, not to memory. `from` is what lets a reader resolve a check's state at an arbitrary past instant without walking from the beginning. `prev_since` — when the from-state began — is the existence boundary: no state claim ever extends backward past it, which is how a check born mid-window stays blank before its registration without registration becoming an event. `detail` is omitted when empty, like the state file's optional fields.

The engine is the ledger's only writer, appending inside the same serialized apply that persists the transition. The state save leads and the append follows, so a crash between the two can lose at most the final transition from the ledger; the row and the pager carry the operational truth throughout, and in exchange no boot reconciliation machinery exists at all. Each append opens the segment named by the event's own year, writes one complete line, and syncs; creating a segment syncs the directory too. Year rollover therefore needs no special case.

Registration produces no event, and neither does removal. History under a name no longer configured is simply ignored — held in the files, warned about once at boot, and served to nobody. A kind change under an existing id keeps appending with the new kind, and per-event `kind` attributes the older bands correctly. Identity continuity is curation, not machinery: `scry prune <check-id>` is how an operator retires a name for good. Retention is keep-everything.

### Lifecycle and gaps

Transitions alone cannot make a strip honest. If the daemon is down for four hours, the last band would silently stretch across the outage — the ledger cannot distinguish "green all night" from "nobody was watching". So the ledger also records the daemon's own lifecycle: a `start` on every boot, a `stop` on clean shutdown. The span between a `stop` and the next `start` is an unwatched gap, and no state is claimed across it. The same applies before the ledger's first `start`: scry may well have been running before history landed, but the ledger cannot testify to it, so pre-ledger time renders unwatched rather than backfilled.

An unclean death writes no `stop`, which is what the state file's `saved` stamp is for. At boot, when the ledger's newest event is not a `stop`, the daemon appends the missing `stop` back-dated to the later of `saved` and that newest event — both are liveness evidence, and whichever is later marks the last instant anything testified the daemon was alive — and then appends its `start`. The gap is bounded to within about a minute of the actual death using only the daemon's own recorded arithmetic, with no wall-clock guessing. When no `saved` stamp exists (a pre-stamp upgrade boot, or a crash before the first stamped save), closure falls back to the newest ledger event and logs that it did. The boot sequence never produces two `start`s without a `stop` between them.

A periodic liveness mark appended to the ledger itself was considered and rejected: a record that grows on a clock while nothing happens is the time-series shape the fence refuses. The state file already breathes, and letting its breath carry the liveness bound keeps the ledger purely eventful.

### One law with the state file

Scry does not run with history it cannot read or record. At boot, before steady state, the daemon parses the whole ledger — every segment, strict `df/dd` binding per line — taking the orphan census as it goes, then performs its boot appends, which proves the history path writable exactly as the reconciliation save proves the state path. A segment that does not parse fails boot loudly, naming the file and the line. A failed append is fatal at runtime. There is no degraded history mode.

Recovery is repairing the file or discarding the *whole* history directory; a fresh ledger's first `start` opens a new vouching boundary and everything before it honestly renders unwatched. Deleting a single segment is not a recovery: a missing year is indistinguishable from a quiet one, and the closure arithmetic would bridge the hole as watched. After a successful boot the daemon's own appends are the only writes, so a query-time read can fail only the way any I/O fails, and it fails the request rather than the daemon.

## Boot Reconciliation

Construction loads and validates the whole file, then builds a new map from the configured registry:

- configured ids with the same kind resume their record unchanged;
- ids absent from configuration are pruned;
- a kind change under an existing id receives a fresh baseline;
- new ids receive a fresh baseline.

History boots between the reconciliation and its save: the ledger is parsed whole, its gap is closed, and its `start` is appended before the state file is rewritten. A history the daemon cannot read or write therefore stops it before it has touched the state file, and both paths are proven writable before steady state either way.

The reconciled map is saved before construction succeeds, including on an unchanged boot. This both fixes baselines durably before steady state and proves at startup that the configured state path is writable. No transition is emitted during reconciliation, so a restart neither re-fires existing trouble nor invents recovery.
