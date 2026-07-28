# Engine and State

Scry's transport-free core contains the runtime model, exact transition rules, paired notification decisions, one serialized state owner, and the durable JSON state file. Probe implementations live outside this core and submit opaque results through the scheduler; no HTTP, notifier, or rendering type participates in model decisions.

The stage-3 binary is a long-running daemon. It reconciles state before startup succeeds, runs the engine and scheduler together, and stops both on SIGINT or SIGTERM.

## Model

Three check kinds enter one state machine:

- `http` and `tcp` are active checks. Their strategies produce `Result{Status, Detail}` values through the scheduler.
- `passive` checks receive the same result shape from reports in stage 4. The silence between reports is judged by pure window arithmetic, not by a probe interface.

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

One engine loop owns the mutable record map. Callers submit active results, passive reports, sweep ticks, and flush requests through commands; application, transition detection, and persistence happen serially. Readers use a separately published deep copy in registry order, so neither the eventual API nor another caller can mutate engine state.

The clock is mandatory at construction. The engine reads it once per input or sweep and passes that time into pure model functions. No package under `internal/` reads wall-clock time; `cmd/scry` is the only place that wires `time.Now`.

## Persistence

The state file is a versioned JSON object:

```json
{
  "v": 1,
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

Writes create a mode-0600 temporary file beside the destination, sync it, rename it over the destination, and sync the directory. A transition and every passive report save immediately. Non-transitioning active results mark state dirty; explicit flush, shutdown, and the scheduler's 60-second periodic flush persist them. Any save failure is fatal.

## Boot Reconciliation

Construction loads and validates the whole file, then builds a new map from the configured registry:

- configured ids with the same kind resume their record unchanged;
- ids absent from configuration are pruned;
- a kind change under an existing id receives a fresh baseline;
- new ids receive a fresh baseline.

The reconciled map is saved before construction succeeds, including on an unchanged boot. This both fixes baselines durably before steady state and proves at startup that the configured state path is writable. No transition is emitted during reconciliation, so a restart neither re-fires existing trouble nor invents recovery.
