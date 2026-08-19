# Status API

The status listener binds `status_listen` and serves three things: the JSON status document, the recorded history document, and the embedded dashboard that renders both. It is unauthenticated in v1 and expected to sit on the LAN. It registers no report route, and the ingest listener registers no status or asset route; the two handler trees are built separately and never combined, which is what bounds a leaked ingest URL to filing reports rather than reading the estate map.

`GET /api/status` is the single walk of the status model. The dashboard is its first consumer, and any later consumer eats exactly the same document; the model itself carries no rendering behavior.

```json
{
  "estate": "hq",
  "generated": "2026-07-29T14:59:04Z",
  "started": "2026-07-29T09:12:44Z",
  "rollup": { "ok": 0, "late": 0, "failed": 2 },
  "checks": [
    {
      "id": "nas-snapshot",
      "name": "NAS nightly snapshot",
      "kind": "passive",
      "state": "failed",
      "since": "2026-07-29T14:58:59Z",
      "last_transition": "2026-07-29T14:58:59Z",
      "last_seen": "2026-07-29T14:58:59Z",
      "detail": "snapshot exited 2"
    }
  ]
}
```

`estate` is the configured display name of the monitored estate. `generated` is stamped from the injected clock at the moment the document is rendered, and every timestamp leaves as UTC regardless of the daemon's local zone. `started` is the running daemon's boot instant — the same instant as the ledger's `start` event — and a changed value is how an open page learns of a restart that no check transition would reveal. `rollup` counts each check exactly once. `checks` is the engine's published snapshot in registry order: sorting is a render decision, not a contract one.

Three fields are declared required and nullable, so a consumer reads one shape whatever the check's history: `last_transition` is null until a check's first real transition, since registration is not one; `last_seen` is null for active checks; `detail` is null until a result or report arrives. They are present as explicit nulls rather than omitted.

Evolution is additive only. Consumers must ignore unknown fields, which is how later structured detail can arrive without a breaking change.

## History

`GET /api/history?from=<rfc3339>&to=<rfc3339>` returns every configured check's recorded state across one window. Both bounds are optional: an omitted `to` is the very clock reading stamped on the document as `generated`, and an omitted `from` is 90 days before the *resolved* `to`. The parameterless form is therefore the render window ending now, and a from-omitted historical query means the 90 days ending at its `to`.

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
      "state_at_from": "ok",
      "state_at_to": "ok",
      "since": "2026-08-10T02:40:12Z",
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

`state_at_from` is the document's load-bearing subtlety: the band *before* a check's first in-window event is drawn from it, and the daemon computes it so the page never reconstructs state from outside the document. It resolves in order — the latest event at or before `from` names it by its `to`; failing that, the earliest event after `from` carries it as its `from`, but only when that event's `prev_since` is at or before the bound; failing that, a live check whose record began at or before `from` is in its current state; otherwise it is null and the check did not exist at `from`. Backward extension always caps at `prev_since`, which is what makes "blank before the check existed" computable rather than merely promised.

The tail is a pair. `state_at_to` and `since` are the state at `to` and the instant that state began, resolved together by the same rules, and they are null together when the check did not yet exist. The tail band is exactly `state_at_to` from `since` forward, so the whole strip renders from this one document.

`daemon` carries the window's lifecycle events, which belong to the estate rather than to any check, and `watching_at_from` says whether the daemon was watching as the window opened — the one fact a renderer cannot infer when the telling `stop` fell before `from`. Every transition event carries the `kind` the check had when it fired; the entry's top-level `kind` is the registry's convenience value, so a window spanning a kind change stays honestly attributed by its events.

The document speaks only for the configured registry: ledger events under names no longer configured are ignored, warned about once at boot, and served to nobody. It is assembled as one consistent cut — the window scan is serialized with the engine's own apply loop, so no document can miss an event at or before its served `to`. That, not the response's stamp, is the guarantee consumers may lean on.

`generated` and `to` are different facts and only coincide by default. `to` is the end of the coverage the document serves; `generated` is the clock reading taken when the response was rendered, after the window was resolved. With `to` omitted the two are equal, which is why the identity was easy to lean on and why an explicit `to` retires it: an explicit bound ends the testimony earlier, and everything after it is outside what the document says. A consumer holding a document and asking whether it is still current must compare against `to`, never `generated` — the dashboard does exactly that, and reads `generated` nowhere in its own logic.

Those comparisons are at-or-after, never strictly-newer. The contract renders date-times at whole-second precision, so an instant that reads equal to `to` on the wire may be newer in truth; equality is the case that must invalidate. The cost of the false positive is one refetch, and the next document's later bound clears it.

A window whose resolved bounds are inverted or empty, or whose `to` passes the daemon's own clock reading, is rejected with 400 and an `{"message": "..."}` body: the document never claims a state for time that has not happened. Validation runs once, on the fully resolved pair, so no one-sided request can reach the ledger inverted. A ledger that stops parsing after boot fails the request with 500 and the same shape — request-scoped, because the daemon proved the whole ledger readable at boot and its own appends are the only writes since. There is no range cap; the operator owns the box and the whole ledger is small.

## Contract-First Generation

`internal/api/specs/scry.yml` is the contract, and both sides of the seam are generated from it:

```sh
make generate
```

That runs `go generate ./...`, which regenerates the ogen server into `internal/api/`, and then `npm run gen:api`, which regenerates the dashboard's TypeScript types into `ui/src/api/schema.d.ts`. A change to the API is a change to the spec followed by a regenerate; the generated Go and TypeScript files are never hand-edited.

`internal/api/ogen.yml` configures the generator. It disables ogen's OpenTelemetry integration, which would otherwise instrument every request with a wall-clock stopwatch inside `internal/` — against scry's rule that only `cmd/scry` reads the wall clock — and would carry metrics machinery into a project whose specification defers metrics permanently. Disabling it also keeps the OpenTelemetry modules out of `go.mod`.

Hand-written handlers live in `internal/server`. The status handler reads the engine's published snapshot, converts each record into the contract's shape, and counts the rollup as it walks. The history handler holds no clock of its own: it passes the request's optional bounds into the engine's serialized read, where defaulting and validation both happen against the one clock reading that stamps `generated`, so a defaulted `to` and that stamp cannot diverge. An explicit `to` is echoed as given and is the document's coverage end regardless; the stamp goes on describing when the response was rendered. The kind, state, and lifecycle vocabularies convert directly between the model and the contract because the spec enumerates exactly their values; tests pin those equalities so they cannot drift apart silently.

## Exposure

`status_listen` defaults to `0.0.0.0:8420` and is expected to be reachable on the LAN. Unlike the ingest listener, it is not constrained to loopback: serving the LAN is its job. It is also the surface that names every monitored system, its current state, and now its recorded history, so it belongs on the LAN or behind a private share, never on a public address. A history read failure reports the segment and line it failed on, which is the operator's fastest path to a broken ledger and one more reason the surface stays private.
