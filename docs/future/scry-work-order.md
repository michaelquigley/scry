# scry — Work Order (v1)

Companion to [scry-spec.md](scry-spec.md). Drafted 2026-07-24 in the planning session; pre-mercurius. The spec carries the vision and the model; this document carries the implementation shape — repo bootstrap, package layout, normative wire and state contracts, slicing, and the decisions that amended the spec during planning.

## Scope

V1 is a single Go daemon with **zero overlay dependencies**: three strategies (`tcp`, `http`, `passive`), two HTTP listeners (ingest on localhost behind an external zrok share; status page + JSON API on the LAN), two notifiers (Mattermost webhook, SMTP), a JSON state file, and an embedded single-page dashboard. No database, no openziti SDK, no zrok SDK.

Out of scope: the `ziti` strategy (deferred to the agent follow-on), the agent strategy and reference agent, the embedding package, the MCP surface, metrics, and all host provisioning (systemd units, `zrok reserve`, token minting, crontab edits) — the last documented as a bridge page, not built.

## Decisions from Planning (2026-07-23/24)

Recorded here as the delta the work order builds against; model-level decisions are also folded into the spec text.

1. **Report contract** — a bare request means *ok*; an optional JSON body `{"status": "failed", "detail": "..."}` reports failure explicitly. Same `{status, detail}` shape as the agent protocol's result object.
2. **Active checks pass through `late` silently** — first failed probe → *late* (no notification), Nth consecutive → *failed* (notifies). Passive *late* notifies. Damping is visible on the page, never paged.
3. **Hardening multiple M=3 default** — passive *failed* at `late_at + M·grace`. Echoes active N=3. Both per-check configurable over global defaults.
4. **Never-seen baseline** — a passive check's window measures from when scry first learned it existed. No fourth state. State entries prune when config entries disappear; a renamed check-id is a reset.
5. **zrok stays out of the process** — ingest binds `127.0.0.1`; a reserved share (`zrok share reserved` as a systemd unit) fronts it. Revisit when the agent strategy brings overlay dialing in-process.
6. **`ziti` strategy dropped from v1** — lands with the agent follow-on; one overlay-dependency event instead of two. Foreseen shape: a ziti dialer option on the `http` strategy keeps transport orthogonal to judgment.
7. **Two listeners** — ingest (localhost, zrok-fronted, bearer tokens) and status (LAN, unauthenticated in v1) are separate `http.Server`s on separate binds. A leaked ingest URL exposes report endpoints only, never the estate map.
8. **Notification fan-out** — every announced transition goes to every configured notifier. No per-check routing in v1; additive later if needed.
9. **No agent-shape in v1** — the model, API, and dashboard are flat. The deferral's honest test, sharpened: *core* = engine + `CheckStrategy` + `Notifier` contracts, which must not move; the agent lands as a strategy whose children are structured detail on the agent's single result (the agent is the check; children are not checks). Result detail is a plain string in v1; the follow-on extends the JSON additively.
10. **Deployment scope** — `push/build` version command and Makefile `push` target in; systemd/zrok/token/crontab provisioning out, bridged by `docs/current/deployment.md`.

## Repo Bootstrap

The repo is empty; stage 1 establishes the full house skeleton, with reckon and ranger as the exemplars:

- Module `github.com/michaelquigley/scry` (matches the origin remote), Go 1.26.
- `cmd/scry/` — cobra root command runs the daemon; `--config` and `--verbose/-v` persistent flags; `dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/michaelquigley/"))`, verbose re-inits at debug level (reckon `cmd/reckon/main.go` is the template). `version.go` wires `push/build.NewVersionCmd("scry")`.
- Config cascade, lowest to highest: compiled defaults → `~/.config/scry/config.yaml` → `./scry.yaml` → `--config` flag, merged via `dd.MergeYAMLFile`, then `Validate()` — config failures die at boot, per the census (reckon `internal/config` is the template).
- `ui/` — Vite + React 19 + TypeScript; `ui/embed.go` with `//go:embed all:dist` behind `//go:build !no_ui` (reckon's `ui/embed.go` verbatim pattern).
- Makefile targets: `frontend`, `build` (depends on `frontend`), `test` (`go test ./... -count=1` + `go vet`), `clean`, `push` (`push vendor $(GOBIN)/scry scry`).
- `AGENTS.md` (arrival order: journal → `docs/current/` → this work order), `CHANGELOG.md` (in-house format, `## Unreleased` slot), `README.md`, `docs/{current,future,journal}/`, `scry.yaml.example`.
- Run `unfurl -i` on all authored markdown, unconditionally.

## Package Layout

```
cmd/scry/            main.go, version.go
internal/config/     Config structs (dd tags), cascade, validation
internal/model/      Check, Result, State, Transition — pure types + transition rules
internal/engine/     scheduler, result intake, state application, notification decisions
internal/state/      state-file load/save (atomic write)
internal/strategy/   CheckStrategy; tcp.go, http.go, passive.go
internal/ingest/     ingest listener: bearer auth, report handler
internal/notify/     Notifier; mattermost.go, smtp.go; dispatch queue with retry
internal/server/     status listener: JSON API + embedded UI
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
    Notify(t Transition) error
}
```

`passive.Evaluate` judges the window against last-seen and the injected clock; it never blocks on the network. Active strategies own their dial/request and honor `ctx` timeouts. The engine injects a clock (`func() time.Time`) everywhere time is read — this is the test seam; nothing calls `time.Now()` outside `main`.

## The State Machine (normative)

Per-check runtime record: `state`, `since` (entered current state), `lastTransition`, `lastSeen` (passive), `lastResult` (status + detail), `consecutiveFails` (active).

**Passive.** With `lastSeen` L, period P, grace G, multiple M (default 3):

- *late* when `now > L + P + G`
- *failed* when `now > L + P + G + M·G` — the 24h/2h example: late at 26h, failed at 32h.
- A report updates `lastSeen` and `lastResult` regardless of its status — the job checked in; staleness and result are independent axes. A report with `status: failed` transitions the check to *failed* immediately (a job reporting its own failure is definitive — no damping). A report with *ok* transitions to *ok* from any state.
- Baseline: at boot, a configured check with no state-file entry gets `lastSeen = now`.

**Active.** Probe on interval; result *failed* increments `consecutiveFails`, *ok* zeroes it.

- `consecutiveFails == 1` → *late*
- `consecutiveFails == N` (default 3) → *failed*
- first *ok* result → *ok*

**Notification pairing rule.** Announcements are paired: a recovery is announced exactly when the trouble it clears was announced.

| transition | passive | active |
|---|---|---|
| → failed | notify | notify |
| → late | notify | silent |
| late → ok | notify | silent |
| failed → ok | notify | notify |

**State file.** JSON at `state_file` (default `~/.local/state/scry/state.json`; set explicitly under systemd). Schema: `{"v": 1, "checks": {"<id>": {"state", "since", "last_seen", "last_status", "last_detail", "consecutive_fails", "last_transition"}}}`. Written atomically (temp file + rename) on every transition and every report; periodic flush (60s, if dirty) and on shutdown. At boot: load, prune ids absent from config, baseline new ids, resume without re-firing — transitions are only detected on *change* after boot.

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

Gotcha to verify at stage 1: `dd` handling of `time.Duration` fields from YAML strings — if unsupported, a small duration wrapper type with the dd binding, decided against `df/dd`'s actual surface, not assumed.

## Ingest Contract (normative)

- `GET|POST /report/<check-id>` — GET or bodiless POST is an *ok* report (the crontab one-liner is a bare GET). POST body, JSON, ≤4KB: `{"status": "ok"|"failed", "detail": "..."}`; `status` defaults to ok, `detail` truncated to 512 chars, unknown fields ignored.
- Auth: `Authorization: Bearer <token>`, compared constant-time against the check's configured token. Only passive checks have tokens; only passive checks are reportable.
- Responses: `204` accepted; `401` missing/bad token; `404` unknown or non-passive check-id; `400` malformed body. No response bodies beyond status codes — the reporter is `curl -fsS`, not a client.

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

The single walk of the model; the dashboard and any future consumer (MCP reader) eat exactly this. Sorting is a render decision — the API returns registry order; the dashboard sorts trouble-first. `last_seen` is null for active checks. Additive evolution only: consumers ignore unknown fields, which is how the agent follow-on's structured detail arrives later without a breaking change.

House siblings (reckon, ranger, flo) generate this seam with ogen + openapi-typescript from a committed OpenAPI document. Proposed for scry: **hand-rolled** — one GET endpoint with a fixed shape does not earn the generator toolchain; the committed contract is the JSON example above plus a TypeScript type in the UI. Cheap to reverse in review if the house-pattern consistency argument should win.

## Dashboard

Single page, no router, no charts (the metrics fence). Rollup banner (all-green, or "N late / N failed"), then the check table: state chip, name, time-in-state, last transition, detail. Trouble sorts first (failed, late, ok; then by `since` descending). Polls `/api/status` every 10s; renders a stale-data banner if a poll fails. Vite + React 19 + TS per house pattern; styling minimal and self-contained.

## Notifiers

Message content, both notifiers: check name and id, old → new state, time spent in the old state, last-result detail, timestamp. Subject/first-line form: `[scry] nas-snapshot: late → failed`.

- **Mattermost** — `POST` webhook, `{"text": "..."}` with light markdown.
- **SMTP** — stdlib `net/smtp` (frozen but sufficient for a house relay: PLAIN auth, STARTTLS). No in-house precedent exists; if the relay needs more, `wneessen/go-mail` is the fallback — decide at stage 5, not before.

Dispatch: transitions enqueue onto a buffered channel consumed by one dispatch goroutine per notifier; per-message retry with backoff (5 attempts over ~10 minutes), then drop with an error log. Notifier failures never block the engine, per the census. Undelivered notifications do not survive a daemon restart — accepted at this scale; the state file is authoritative, the page still shows truth.

## Scheduling

One engine goroutine owns the state map. Active checks probe from per-check goroutines on jittered tickers (initial offset random within the interval, spreading fifty probes rather than volleying them); probes run under `ctx` timeout and deliver `(checkID, Result)` into a single results channel. The passive evaluator sweeps every 15s and delivers window verdicts into the same channel. Ingest reports arrive as a third producer. The engine consumes serially: apply result → detect transition → persist → enqueue notifications. No locks in the hot path beyond the channel; the API handler reads a snapshot the engine publishes (copy-on-update behind a `sync.RWMutex` or an atomic pointer swap).

## Testing

- `internal/model` + `internal/engine`: table-driven transition tests over the fake clock — the pairing rule, damping counters, hardening arithmetic, baseline stamping, restart-without-refire, pruning. This is the load-bearing test surface.
- `internal/ingest` + `internal/server`: `httptest` round-trips — auth, malformed bodies, unknown ids, the status walk.
- `internal/strategy`: real listeners on `127.0.0.1:0` for tcp/http; timeout paths via unroutable addresses and stalled handlers.
- No external network, no overlay, no sleeps — the clock is injected everywhere.

## Slicing

Six stages, each terminus-gated to `clean` before Michael reviews, each synthesizing its behavior into `docs/current/` and a `CHANGELOG.md` entry as it lands:

1. **Skeleton + config** — repo bootstrap per above; config structs, cascade, one-of validation, registry parse; dd duration gotcha resolved. Boot dies loudly on bad config.
2. **Engine + state machine + state file** — the model types, transition rules, notification decisions as pure logic; state persistence; the fake-clock test suite. The heart of the system, landed before any I/O exists.
3. **Active strategies + scheduler** — tcp and http strategies, jittered tickers, timeout handling, passive sweep; the daemon now probes.
4. **Ingest listener** — token auth, report handling, the normative contract above; the daemon now hears.
5. **Notifiers** — Mattermost + SMTP, dispatch queue, retry; the daemon now speaks.
6. **Status API + dashboard + docs** — the JSON walk, the embedded UI, `docs/current/` synthesis (model, config, contracts, deployment bridge page), README.

Stages 3–5 are independent of each other and could reorder; 1 → 2 is strict, 6 consumes everything.

## Dependencies

Go: `github.com/michaelquigley/df` (dd, dl), `github.com/michaelquigley/push` (build/version), `github.com/spf13/cobra`, stdlib otherwise. UI: react 19, vite, typescript. Nothing else — no overlay SDKs, no database driver, no mail library (pending stage 5), no HTTP framework.

## Operations Bridge (documented, not built)

`docs/current/deployment.md` at stage 6 records the expected HQ arrangement: the scry systemd unit; the `zrok reserve` + `zrok share reserved` unit beside it fronting `127.0.0.1:8421`; token minting (`openssl rand -hex 16` per passive check, pasted into config); the crontab migration line per the spec; LAN exposure of `:8420`.

## Foreseen, Not Built

- **`ziti` strategy** — lands with the agent follow-on; likely a dialer option on `http` plus a bare-dial strategy, sharing the judgment code.
- **Agent strategy** — a new strategy whose result detail is structured (children as detail, per decision 9); API grows an additive field; engine and contracts untouched — that's the honest test.
- **Second ingest transport** — a ziti-native listener beside the HTTP one, same stamp into the model.
- **MCP reader** — thin consumer of `/api/status`, after the API stabilizes under real use.

## Open Questions for Review

1. `dd` duration parsing — verified at stage 1; wrapper type if needed.
2. Hand-rolled status API vs. house ogen pattern — proposed hand-rolled; one-word veto flips it.
3. `net/smtp` vs. `go-mail` — decided at stage 5 against the actual relay.
