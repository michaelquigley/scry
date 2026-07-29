# Status API

The status listener binds `status_listen` and serves two things: the JSON status document and the embedded dashboard that renders it. It is unauthenticated in v1 and expected to sit on the LAN. It registers no report route, and the ingest listener registers no status or asset route; the two handler trees are built separately and never combined, which is what bounds a leaked ingest URL to filing reports rather than reading the estate map.

`GET /api/status` is the single walk of the status model. The dashboard is its first consumer, and any later consumer eats exactly the same document; the model itself carries no rendering behavior.

```json
{
  "generated": "2026-07-29T14:59:04Z",
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

`generated` is stamped from the injected clock at the moment the document is rendered, and every timestamp leaves as UTC regardless of the daemon's local zone. `rollup` counts each check exactly once. `checks` is the engine's published snapshot in registry order: sorting is a render decision, not a contract one.

Three fields are declared required and nullable, so a consumer reads one shape whatever the check's history: `last_transition` is null until a check's first real transition, since registration is not one; `last_seen` is null for active checks; `detail` is null until a result or report arrives. They are present as explicit nulls rather than omitted.

Evolution is additive only. Consumers must ignore unknown fields, which is how later structured detail can arrive without a breaking change.

## Contract-First Generation

`internal/api/specs/scry.yml` is the contract, and both sides of the seam are generated from it:

```sh
make generate
```

That runs `go generate ./...`, which regenerates the ogen server into `internal/api/`, and then `npm run gen:api`, which regenerates the dashboard's TypeScript types into `ui/src/api/schema.d.ts`. A change to the API is a change to the spec followed by a regenerate; the generated Go and TypeScript files are never hand-edited.

`internal/api/ogen.yml` configures the generator. It disables ogen's OpenTelemetry integration, which would otherwise instrument every request with a wall-clock stopwatch inside `internal/` — against scry's rule that only `cmd/scry` reads the wall clock — and would carry metrics machinery into a project whose specification defers metrics permanently. Disabling it also keeps the OpenTelemetry modules out of `go.mod`.

Hand-written handlers live in `internal/server`. The status handler reads the engine's published snapshot, converts each record into the contract's shape, and counts the rollup as it walks. The kind and state vocabularies convert directly between the model and the contract because the spec enumerates exactly the model's values; a test pins that equality so the two cannot drift apart silently.

## Exposure

`status_listen` defaults to `0.0.0.0:8420` and is expected to be reachable on the LAN. Unlike the ingest listener, it is not constrained to loopback: serving the LAN is its job. It is also the surface that names every monitored system and its current state, so it belongs on the LAN or behind a private share, never on a public address.
