# scry — Work Order (v1)

Companion to [scry-spec.md](scry-spec.md). Drafted 2026-07-24 in the planning session; converged through mercurius review 2026-07-27 — six rounds, verdict `ready_to_build` (session synopsis in `.mercurius/`). The spec carries the vision and the model; this document carries the implementation shape — repo bootstrap, package layout, normative wire and state contracts, slicing, and the decisions that amended the spec during planning.

## Scope

V1 is a single Go daemon with **zero overlay dependencies**: three check kinds (`tcp` and `http` active probes, `passive` reports), two HTTP listeners (ingest on localhost behind an external zrok share; status page + JSON API on the LAN), two notifiers (Mattermost webhook, SMTP), a JSON state file, and an embedded single-page dashboard. No database, no openziti SDK, no zrok SDK.

Out of scope: the `ziti` strategy (deferred to the agent follow-on), the agent strategy and reference agent, the embedding package, the MCP surface, metrics, and all host provisioning (systemd units, `zrok reserve`, token minting, crontab edits) — the last documented as a bridge page, not built.

## Decisions from Planning (2026-07-23/24)

Recorded here as the delta the work order builds against; model-level decisions are also folded into the spec text. The mercurius arc (six rounds, 2026-07-24 → 2026-07-27) subsequently refined these into the normative sections below — every review decision is embodied in the text, with the audit trail in `.mercurius/`.

1. **Report contract** — a bare request means *ok*; an optional JSON body `{"status": "failed", "detail": "..."}` reports failure explicitly. Same `{status, detail}` shape as the agent protocol's result object.
2. **Active checks pass through `late` silently** — first failed probe → *late* (no notification), Nth consecutive → *failed* (notifies). Passive *late* notifies. Damping is visible on the page, never paged.
3. **Hardening multiple M=3 default** — passive *failed* at `late_at + M·grace`. Echoes active N=3. Both per-check configurable over global defaults.
4. **Never-seen baseline** — a passive check's window measures from when scry first learned it existed. No fourth state. State entries prune when config entries disappear; a renamed check-id is a reset.
5. **zrok stays out of the process** — ingest binds `127.0.0.1`; a reserved share (`zrok share reserved` as a systemd unit) fronts it. Revisit when the agent strategy brings overlay dialing in-process.
6. **`ziti` strategy dropped from v1** — lands with the agent follow-on; one overlay-dependency event instead of two. Foreseen shape: a ziti dialer option on the `http` strategy keeps transport orthogonal to judgment.
7. **Two listeners** — ingest (localhost, zrok-fronted, bearer tokens) and status (LAN, unauthenticated in v1) are separate `http.Server`s on separate binds. A leaked ingest URL exposes report endpoints only, never the estate map.
8. **Notification fan-out** — every announced transition goes to every configured notifier. No per-check routing in v1; additive later if needed.
9. **No agent-shape in v1** — the model, API, and dashboard are flat. The deferral's honest test, sharpened: *frozen* means the transition logic and the `CheckStrategy`/`Notifier` method signatures; *expected* means additive fields on `Result`, the persisted record, and the API JSON — the agent lands as a strategy whose children are structured detail on the agent's single result (the agent is the check; children are not checks). The engine stores and forwards `Result` opaquely, so a new field flows strategy → state → API without the state machine looking inside. Result detail is a plain string in v1.
10. **Deployment scope** — `push/build` version command and Makefile `push` target in; systemd/zrok/token/crontab provisioning out, bridged by `docs/current/deployment.md`.

## Repo Bootstrap

The repo is empty; stage 1 establishes the full house skeleton, with reckon and ranger as the exemplars:

- Module `github.com/michaelquigley/scry` (matches the origin remote), Go 1.26.
- `cmd/scry/` — cobra root command runs the daemon; `--config` and `--verbose/-v` persistent flags; `dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/michaelquigley/"))`, verbose re-inits at debug level (reckon `cmd/reckon/main.go` is the template). `version.go` wires `push/build.NewVersionCmd("scry")`.
- Config cascade, lowest to highest: compiled defaults → `~/.config/scry/config.yaml` → `./scry.yaml` → `--config` flag, merged via `dd.MergeYAMLFile`, then `Validate()` — config failures die at boot, per the census (reckon `internal/config` is the template).
- `ui/` — Vite + React 19 + TypeScript; `ui/embed.go` with `//go:embed all:dist` behind `//go:build !no_ui` (reckon's `ui/embed.go` verbatim pattern).
- Makefile targets: `generate` (ogen server + TypeScript client types, per reckon), `frontend`, `build` (depends on `frontend`), `test` (`go test ./... -count=1` + `go vet`), `clean`, `push` (`push vendor $(GOBIN)/scry scry`).
- `AGENTS.md` (arrival order: journal → `docs/current/` → this work order), `CHANGELOG.md` (in-house format, `## Unreleased` slot), `README.md`, `docs/{current,future,journal}/`, `scry.yaml.example`.
- Run `unfurl -i` on all authored markdown, unconditionally.

## Package Layout

```
cmd/scry/            main.go, version.go
internal/config/     Config structs (dd tags), cascade, validation
internal/model/      Check, Result, State, Transition — pure types + transition rules
internal/engine/     scheduler, result intake, state application, notification decisions
internal/state/      state-file load/save (atomic write)
internal/strategy/   CheckStrategy (the active-probe contract); tcp.go, http.go
internal/ingest/     ingest listener: bearer auth, report handler
internal/notify/     Notifier; mattermost.go, smtp.go; dispatch queue with retry
internal/api/        OpenAPI document (specs/scry.yml) + ogen-generated server
internal/server/     status listener: API handlers + embedded UI
ui/                  dashboard SPA
```

The contracts, Go-shaped:

```go
type Result struct {
    Status Status // StatusOK | StatusFailed
    Detail string
}

type CheckStrategy interface {
    Evaluate(ctx context.Context) Result
}

type Notifier interface {
    Notify(ctx context.Context, t Transition) error
}
```

`internal/ingest` and `internal/server` each construct their own mux; the two listeners share no handler tree. This is normative — decision 7's blast-radius claim depends on it, and a shared mux is a defect even while every endpoint test passes.

The engine treats `Result` as opaque payload: it reads `Status` for transition detection and stores/forwards everything else untouched — this opacity is what makes the agent follow-on's additive fields mechanical rather than surgery (decision 9). `CheckStrategy` is the active-probe contract; only `tcp` and `http` implement it. Passive checks implement no strategy interface — their results *are* their ingest reports, the same `{status, detail}` shape entering the engine through the ingest producer. The window arithmetic is a pure function in `internal/model` (`record + now + period/grace/M → ok | late | failed`), called by the engine on sweep ticks inside the serialized loop. Window verdicts derive *state only*: they never touch `lastSeen` or `lastResult`, so the last real report's detail survives untouched for notifications and the API. Active strategies own their dial/request and honor `ctx` timeouts. The engine injects a clock (`func() time.Time`) everywhere time is read — this is the test seam; nothing calls `time.Now()` outside `main`.

## The State Machine (normative)

Per-check runtime record: `state`, `since` (entered current state), `lastTransition` (null until the first real transition — registration is not a transition), `lastSeen` (passive), `lastResult` (status + detail), `consecutiveFails` (active).

A freshly registered check — either kind — begins complete: state *ok*, `since` = registration time, `lastTransition` null, `lastResult` absent (API `detail` null until the first result or report), `consecutiveFails` zero, and for passive checks `lastSeen` = registration time. An active check reads *ok* between registration and its first jittered probe.

**Passive.** With `lastSeen` L, period P, grace G, multiple M (default 3):

- *late* when `now > L + P + G`
- *failed* when `now > L + P + G + M·G` — the 24h/2h example: late at 26h, failed at 32h.
- A report updates `lastSeen` and `lastResult` regardless of its status — the job checked in; staleness and result are independent axes. A report with `status: failed` transitions the check to *failed* immediately (a job reporting its own failure is definitive — no damping). A report with *ok* transitions to *ok* from any state.
- **The sweep only degrades.** Window verdicts move a check to *late* or *failed* by age, never to *ok* — *ok* is entered only by an ok report. An explicit failed report holds *failed* until a later ok report arrives, regardless of how fresh the window is.
- Baseline: at boot, a configured check with no state-file entry gets `lastSeen = now`.

**Active.** Probe on interval; result *failed* increments `consecutiveFails`, *ok* zeroes it.

- `consecutiveFails >= 1` → *late*
- `consecutiveFails >= N` (default 3) → *failed*
- first *ok* result → *ok*

Thresholds are `>=`, never `==` — a persisted counter already above a lowered N hardens on the next failed probe instead of stranding the check in *late*. And *failed* is sticky: once entered, only an ok result leaves it — threshold arithmetic can harden a state, never soften one, so raising N over a persisted *failed* check never downgrades it to *late*.

**Notification pairing rule.** Announcements are paired: a recovery is announced exactly when the trouble it clears was announced.

| transition | passive | active |
|---|---|---|
| → failed | notify | notify |
| → late | notify | silent |
| late → ok | notify | silent |
| failed → ok | notify | notify |

**State file.** JSON at `state_file` (default `~/.local/state/scry/state.json`; set explicitly under systemd). Schema: `{"v": 1, "checks": {"<id>": {"kind", "state", "since", "last_seen", "last_status", "last_detail", "consecutive_fails", "last_transition"}}}`. Written atomically (temp file + rename) on every transition and every report; periodic flush (60s, if dirty) and on shutdown. Every mutation of a persisted record — including a non-transitioning active result that advances `consecutiveFails` or refreshes `lastResult` — marks the file dirty; transitions and reports remain the immediate-save points, and dirty state rides the periodic flush and shutdown save. A failed save is fatal — the same tier as a failed load: scry does not run with state it cannot persist, and dying loudly under systemd beats diverging silently. Boot reconciliation (prunes, kind resets, new baselines) is persisted before the engine enters steady state, so a crash immediately after boot shifts no window. At boot: load, prune ids absent from config, discard any entry whose `kind` no longer matches the configured strategy (a kind change under the same id is a rename in disguise — fresh registration, baseline now), baseline new ids, resume without re-firing — transitions are only detected on *change* after boot. A *missing* state file is first boot and proceeds normally; an *existing* file that is unreadable, malformed, or of an unsupported version is a fatal boot error — the state parses whole or scry does not start, matching the config rule. The deliberate remedy is deleting the file: an explicit first boot, accepting the re-fire consciously rather than silently. Pruning configured-away ids is normal operation, never an error.

## Configuration (normative example)

```yaml
status_listen: "0.0.0.0:8420"     # LAN: dashboard + JSON API
ingest_listen: "127.0.0.1:8421"   # localhost: zrok-fronted reports
state_file: "/var/lib/scry/state.json"

defaults:
  interval: 60s        # active probe cadence
  timeout: 10s         # active probe deadline
  fail_after: 3        # active: N consecutive failed probes
  harden_after: 3      # passive: M further grace-widths

notifiers:
  mattermost:
    webhook_url: "https://mm.example.com/hooks/..."
  smtp:
    host: "smtp.hq"
    port: 25
    from: "scry@hq"
    to: ["michael@quigley.com"]

checks:
  - id: nas-snapshot
    name: "NAS nightly snapshot"
    passive:
      period: 24h
      grace: 2h
      token: "9f2c..."
  - id: gitea
    name: "gitea web"
    http:
      url: "https://git.hq.quigley.com/"
      expect: [200]        # default [2xx]
      insecure: false      # self-signed escape hatch
  - id: pg-hq
    name: "postgres"
    tcp:
      address: "10.0.0.5:5432"
    interval: 30s          # per-check overrides sit beside the strategy block
```

A check declares **exactly one** of `passive` / `http` / `tcp` as an optional sub-struct; validation rejects zero or multiple at boot. This keeps the registry dd-friendly without polymorphic marshaling machinery. Per-check `interval`, `timeout`, `fail_after`, `harden_after` override the defaults block.

The `http` strategy never follows redirects — the judged status is the one the configured URL itself returns; an operator expecting a redirect lists its code (`expect: [301]`). Chasing redirects would let a dead service behind a 302-to-login proxy read green forever.

Validation also enforces parameter domains, dying at boot per the config tier: every duration (period, grace, interval, timeout) strictly positive; `harden_after >= 1`; `fail_after >= 2` — the late-passthrough rule needs at least one damped probe, and hair-trigger paging is achieved by lowering the interval, not by collapsing the damping.

Identity invariants die at boot too: every check id non-empty, unique, and a single URL-safe path segment (lowercase `[a-z0-9-]`, house slug style — it rides in `/report/<id>` and keys the state file); every passive token non-empty and unique across passive checks — a shared token would quietly void the one-check blast-radius claim.

So does everything statically decidable about a strategy: `http.url` must parse with an `http`/`https` scheme, `tcp.address` must be valid `host:port` syntax, `expect` codes must lie in 100–599. Reachability remains a strategy result per the census — only well-formedness moves to boot. A malformed URL is a broken config, not a failing service, and must never render as one.

Duration fields are ordinary `time.Duration`, bound from YAML strings like `30s` — `df/dd` supports this natively (verified against its source and tests during review).

## Ingest Contract (normative)

- `GET|POST /report/<check-id>` — GET or bodiless POST is an *ok* report (the crontab one-liner is a bare GET). A GET body is ignored outright — a GET is an ok report, period. Any other method is rejected. POST body, JSON, ≤4KB: `{"status": "ok"|"failed", "detail": "..."}`; `status` defaults to ok, `detail` truncated to 512 UTF-8 bytes at a rune boundary, unknown fields ignored. The body is exactly one JSON object followed by EOF; anything else is malformed.
- Auth: `Authorization: Bearer <token>` — the scheme matched case-insensitively (`bearer`, `Bearer`, `BEARER` all accepted; the spec's canonical curl line sends lowercase), the token compared constant-time against the check's configured token. Only passive checks have tokens; only passive checks are reportable. Unknown and non-passive ids still perform a constant-time comparison against a dummy hash, then return the same `401` as a bad token; the internet-facing surface never discloses registry membership through status-code precedence.
- Responses: `204` accepted; `401` anything other than a known passive id plus its valid token; `404` paths outside the `/report/<check-id>` shape; `400` malformed body; `405` any method other than GET/POST; `413` body over 4KB. The `204` is returned only after the engine has applied *and persisted* the report — it means durably recorded, not merely received. No response bodies beyond status codes — the reporter is `curl -fsS`, not a client.

## Status API (normative)

`GET /api/status` on the status listener:

```json
{
  "generated": "2026-07-24T09:00:00Z",
  "rollup": {"ok": 47, "late": 1, "failed": 2},
  "checks": [
    {
      "id": "nas-snapshot",
      "name": "NAS nightly snapshot",
      "kind": "passive",
      "state": "late",
      "since": "2026-07-24T02:00:00Z",
      "last_transition": "2026-07-24T02:00:00Z",
      "last_seen": "2026-07-23T00:05:00Z",
      "detail": "snapshot complete"
    }
  ]
}
```

The single walk of the model; the dashboard and any future consumer (MCP reader) eat exactly this. Sorting is a render decision — the API returns registry order; the dashboard sorts trouble-first. `last_seen` is null for active checks; `last_transition` is null until a check's first real transition. Additive evolution only: consumers ignore unknown fields, which is how the agent follow-on's structured detail arrives later without a breaking change.

House siblings (reckon, ranger, flo) generate this seam with **ogen + openapi-typescript** from a committed OpenAPI document, and scry follows the house pattern — decided in review 2026-07-27, standardization over minimalism. The OpenAPI document (`internal/api/specs/scry.yml`) is the contract; the JSON example above is illustrative. ogen generates the Go server side, `openapi-typescript` generates the dashboard's client types (the `gen:api` script, per reckon/ranger). flo in `../archive` is the reference implementation for the ogen/embed/UI wiring.

## Dashboard

Single page, no router, no charts (the metrics fence). Rollup banner (all-green, or "N late / N failed"), then the check table: state chip, name, time-in-state, last transition, detail. Trouble sorts first (failed, late, ok; then by `since` descending). Polls `/api/status` every 10s; renders a stale-data banner if a poll fails. Vite + React 19 + TS per house pattern; styling minimal and self-contained.

## Notifiers

Message content, both notifiers: check name and id, old → new state, time spent in the old state, last-result detail, timestamp. Subject/first-line form: `[scry] nas-snapshot: late → failed`.

- **Mattermost** — `POST` webhook, `{"text": "..."}` with light markdown.
- **SMTP** — stdlib `net/smtp` (frozen but sufficient for a house relay: PLAIN auth, STARTTLS). No in-house precedent exists; if the relay needs more, `wneessen/go-mail` is the fallback — decide at stage 5, not before. At stage 5, also narrow this prose to what the HQ relay actually requires, adding credential/TLS config fields only if the relay demonstrably needs them — the config example deliberately shows none.

Dispatch: the dispatcher owns an unbounded in-memory FIFO per notifier; the engine's enqueue is an append under a mutex — always instant, never blocking, regardless of delivery state. One delivery goroutine per notifier drains its queue: the dispatcher wraps each attempt in a 30s `context.WithTimeout` — deadline policy lives in the dispatcher, not in each implementation — with per-message retry and backoff (5 attempts over ~10 minutes), then drop with an error log. Every `Notify` implementation must return promptly on ctx cancellation: a hang is a bug, not a slow delivery. `net/smtp` predates context, so the SMTP notifier honors it via `DialContext` and connection deadlines derived from the ctx deadline; Mattermost gets it free through the request context. This shape is what lets both commitments hold at once — the engine never waits (census) and announced transitions are never shed to backpressure (announce-once); at sixty entities the queue's practical size is trivial. Undelivered notifications do not survive a daemon restart — accepted at this scale; the state file is authoritative, the page still shows truth.

## Scheduling

One engine goroutine owns the state map. Active checks probe from per-check goroutines on jittered tickers (initial offset random within the interval, spreading fifty probes rather than volleying them); probes run under `ctx` timeout and deliver `(checkID, Result)` into a single results channel. A result cut short by *parent* (daemon) cancellation is discarded, never delivered — shutdown says nothing about health; a probe deadline reached while the parent is still live remains a genuine failed result. Passive evaluation sends no verdicts through the channel: a sweep ticker delivers a bare *tick* every 15s, and on each tick the engine derives every passive check's window state via the pure `internal/model` function, against its own current record, inside the same serialized loop that applies reports — evaluation and application are atomic, so a window verdict can never go stale against a fresher report. Ingest reports arrive as the other producer. The engine consumes serially: apply result → detect transition → persist → enqueue notifications. No locks in the hot path beyond the channel; the API handler reads a snapshot the engine publishes (copy-on-update behind a `sync.RWMutex` or an atomic pointer swap).

## Testing

- `internal/model` + `internal/engine`: table-driven transition tests over the fake clock — the pairing rule, damping counters, hardening arithmetic, baseline stamping (fresh-record completeness, both kinds), restart-without-refire, pruning, kind-change resets (active↔passive under the same id), parameter-domain boundaries (`fail_after`/`harden_after` at their minima; a persisted counter above a lowered N across restart; a persisted *failed* staying failed under a raised N), shutdown-cancellation suppression (parent-cancel discards the in-flight result; probe-deadline expiry with a live parent stays failed), and passive catch-up (one sweep finding a check already past the failed threshold → direct ok→failed, exactly one announcement). This is the load-bearing test surface.
- `internal/config`: rejection tests — zero or multiple strategy blocks, domain violations, duplicate or malformed ids, empty or shared tokens, malformed URLs and addresses, out-of-range expect codes.
- `internal/state`: save/load round-trips — boot reconciliation persisted before steady state; baselines, prunes, and kind resets surviving a restart; a non-transitioning second active failure's counter surviving a flush-and-restart.
- `internal/ingest` + `internal/server`: `httptest` round-trips — auth (including the spec's canonical lowercase `authorization: bearer <token>` header, verbatim), malformed bodies, multibyte detail truncation at the rune boundary, unknown ids, method policing (405; GET body ignored), the status walk. Plus cross-surface isolation: the ingest handler returns 404 for `/api/status` and UI paths, the status handler returns 404 for `/report/*` — the decision-7 blast-radius guarantee as an acceptance condition, not an assertion.
- `internal/strategy`: real listeners on `127.0.0.1:0` for tcp/http; timeout paths via unroutable addresses and stalled handlers; redirect non-following (a 302ing local server judged as 302, never its destination).
- `internal/notify`: stalled-peer tests — deliberately hung local HTTP and SMTP listeners proving each attempt returns at its ctx deadline and the worker advances into retry and eventual drop.
- No external network, no overlay, no sleeps — the clock is injected everywhere.

## Slicing

Six stages, each terminus-gated to `clean` before Michael reviews, each synthesizing its behavior into `docs/current/` and a `CHANGELOG.md` entry as it lands:

1. **Skeleton + config** — repo bootstrap per above; config structs, cascade, one-of validation, registry parse; dd duration gotcha resolved. Boot dies loudly on bad config.
2. **Engine + state machine + state file** — the model types, transition rules, notification decisions as pure logic; state persistence; the fake-clock test suite. The heart of the system, landed before any I/O exists.
3. **Active strategies + scheduler** — tcp and http strategies, jittered tickers, timeout handling, passive sweep; the daemon now probes.
4. **Ingest listener** — token auth, report handling, the normative contract above; the daemon now hears.
5. **Notifiers** — Mattermost + SMTP, dispatch queue, retry; the daemon now speaks.
6. **Status API + dashboard + docs** — the committed OpenAPI contract and its generated server/client, the JSON walk, the embedded UI, `docs/current/` synthesis (model, config, contracts, deployment bridge page), README.

Stages 3–5 are independent of each other and could reorder; 1 → 2 is strict, 6 consumes everything.

## Dependencies

Go: `github.com/michaelquigley/df` (dd, dl), `github.com/michaelquigley/push` (build/version), `github.com/spf13/cobra`, `github.com/ogen-go/ogen` (generated status-API server), stdlib otherwise. UI: react 19, vite, typescript, openapi-typescript. Nothing else — no overlay SDKs, no database driver, no mail library (pending stage 5).

## Operations Bridge (documented, not built)

`docs/current/deployment.md` at stage 6 records the expected HQ arrangement: the scry systemd unit; the `zrok reserve` + `zrok share reserved` unit beside it fronting `127.0.0.1:8421`; token minting (`openssl rand -hex 16` per passive check, pasted into config); the crontab migration line per the spec; LAN exposure of `:8420`.

## Foreseen, Not Built

- **`ziti` strategy** — lands with the agent follow-on; likely a dialer option on `http` plus a bare-dial strategy, sharing the judgment code.
- **Agent strategy** — a new strategy whose result carries children as an additive structured field on `Result` (per decision 9); the state file and API grow the same field; transition logic and method signatures untouched — that's the honest test.
- **Second ingest transport** — a ziti-native listener beside the HTTP one, same stamp into the model.
- **MCP reader** — thin consumer of `/api/status`, after the API stabilizes under real use.

## Open Questions for Review

1. `net/smtp` vs. `go-mail` — decided at stage 5 against the actual relay.
