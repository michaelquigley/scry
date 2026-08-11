# State History — Work Order

Realizes `docs/future/state-history-spec.md`: the append-only transition ledger, daemon lifecycle events with gap closure, the `/api/history` contract route, and the dashboard's per-check state strip. Five terminus-gated stages, each landable and reviewable on its own. This document is grounded in the code as of the drafting date; where the spec left mechanism open, the concrete choice is recorded here, and where implementation pressure produced a contract-visible refinement, it is called out in the deltas section rather than folded in silently.

## Ground Truth

The integration points as they stand:

- `internal/model/model.go` — `Transition` already carries everything a ledger line needs: `CheckID`, `Kind`, `From`, `To`, `At`, and `Result` (whose `Detail` is the line's detail). No model changes are required.
- `internal/engine/engine.go` — one serialized command loop; transitions are detected in `handleActive`, `handleReport`, and `handleSweep`, each of which calls `persist()` before `enqueue()`. `handleFlush` currently early-returns when clean. `New` loads, reconciles, persists, publishes. `Run`'s `ctx.Done()` arm persists dirty state at shutdown. The `Repository` interface (`Load`/`Save`) is the whole-state persistence seam.
- `internal/state/store.go` — the versioned strict-bound JSON state file; `diskFile` is `{v, checks}` with optional record fields as pointers. The atomic write discipline (temp, chmod, write, sync, rename, dir sync) lives in `Save`.
- `internal/engine/scheduler.go` — `dirtyFlushInterval` of 60 seconds already ticks `Flush` unconditionally; the conditionality lives in the engine's handler, not the ticker.
- `internal/config/config.go` — `defaultStatePath()` resolves `$XDG_STATE_HOME`/`~/.local/state`; `Validate` rejects blank paths. `NewConfig` is the compiled bottom layer.
- `internal/api/specs/scry.yml` — the contract; `make generate` regenerates the ogen server and `ui/src/api/schema.d.ts`. The only path today is `/status`.
- `internal/server/` — `newHandler` wires `statusHandler` into the ogen server under `/api`; `statusHandler` holds the `Reader` (engine snapshot), clock, and estate name. `describeCheck` is the record-to-contract walk.
- `cmd/scry/main.go` — `runDaemon` constructs store, engine, scheduler, ingest, handler, server; the only `time.Now` wiring in the tree.
- `ui/src/` — `App.tsx` owns the 10-second poll, the stale posture, `receivedAt`/`ageOffset`, and the one-second pulse. `CheckTable.tsx` renders the five-column table with trouble-first sort. `client.ts` types come from the generated schema. There is no UI test runner today; stage 4 introduces vitest, dev-only.

## Deltas from the Spec

Grounding surfaced four refinements the spec did not originally name. They were adjudicated sound across mercurius rounds 1 through 5 and remain recorded here as the audit trail of where they entered (the spec has since absorbed `watching_at_from` and the rejection semantics into its own prose):

1. **`watching_at_from` joins the history document.** The spec's `daemon` array only carries in-window lifecycle events. When the daemon was already down at `from` (its `stop` fell before the window), the renderer cannot know the window opens inside a gap. The document gains one required boolean, computed in the same walk-back that resolves `state_at_from`.
2. **A 400 response and small `error` schema join the contract.** The spec says inverted bounds are rejected; the contract needs a shape for the rejection. One `error` object (`message`), carried by the 400 and (per round 4) the 500 on `/history`.
3. **The `Repository` seam carries the `saved` stamp explicitly.** `Save(Snapshot)` becomes `Save(Snapshot, time.Time)` and `Load()` becomes `Load() (Snapshot, time.Time, error)` — the stamp travels through the seam rather than the store reading a clock, preserving the only-`main`-wires-`time.Now` law. Zero time on load means an old-format file with no stamp.
4. **The ledger is required at engine construction, not nil-tolerated.** `New` rejects a nil ledger the way it rejects a nil repository. Tests construct real stores over `t.TempDir()`. This strengthens the spec's fatal tier: a production engine cannot silently run without history.

One clarification of spec language with no contract effect: bound resolution splits between owners. The history store resolves what events alone can say at each bound (latest transition at-or-before the bound by its `to`; else earliest after it by its `from`, only when that event's `prev_since` is at or before the bound); the handler applies the live-record fallback (current state when `Since` is at or before the bound; else null) from the records carried in the view's cut. The daemon still owns all of it; the page still learns everything from the document.

## Stage 1 — the ledger store

New package `internal/history`, sibling to `internal/state` and matching its posture: strict binding, explicit validation, sync discipline, no clock.

**`store.go`.** One `Event` type covers all three line shapes, with per-type validation rather than three structs:

```go
const lineVersion = 1

type EventType string // "transition" | "start" | "stop"

type Event struct {
    Version   int         `dd:"v,+required"`
    TS        time.Time   `dd:"ts,+required"`
    Type      EventType   `dd:"event,+required"`
    Check     string      `dd:"check"`
    Kind      model.Kind  `dd:"kind"`
    From      model.State `dd:"from"`
    To        model.State `dd:"to"`
    PrevSince *time.Time  `dd:"prev_since"`
    Detail    *string     `dd:"detail"`
}
```

Validation: a `transition` requires non-empty `Check`, valid `Kind`, valid distinct `From`/`To`, and a non-zero `PrevSince` (carried from `Transition.PreviousSince` — the existence boundary no state claim extends backward past); `start`/`stop` require every transition field zero-valued — the flat struct cannot distinguish absent from explicitly empty, so zero is the enforceable invariant and the one validation claims; every event requires a non-zero `TS` and `lineVersion`. `Detail` is a pointer, omitted when the result detail is empty, per the state file's absence convention.

`Store` is constructed over the history directory. Its surface:

- `Boot(at, lastSaved time.Time, configured map[string]struct{}) error` — `MkdirAll` the directory; strict-parse every segment whole (every line binds and validates, or boot fails, naming the file and line — one law with the state file, no degraded history mode; the documented recovery is repair, or discard the whole history directory — never a single segment, whose absence would read as a quiet year and let the closure arithmetic bridge the hole as watched). If the ledger's newest event exists and is not a `stop`, append the missing `stop` at `max(lastSaved, newest event's TS)` — one rule for every case: `saved` and the ledger's own last line are both liveness evidence, and a zero `lastSaved` (pre-stamp state file, or a crash before the first stamped save — logged) degenerates to the newest event. The boot sequence never emits two `start`s without a `stop` between them. Then append `start` at `at`. The same validating parse collects the ledger's id set and yields the orphan census: each id seen in the ledger but absent from `configured` is warned once (`ignoring history for unconfigured check '<id>'; 'scry prune <id>' removes it`) and otherwise ignored — complete by construction, because the whole ledger parses or boot fails.
- `AppendTransition(model.Transition) error`, `AppendStart(at) error`, `AppendStop(at) error` — each marshals one line (`dd.UnbindJSON` + newline) and lands it with the append discipline below.
- `Window(from, to time.Time) (Window, error)` where `Window` carries `Events` (all types, in `[from, to]`, ascending), `StateAtFrom map[string]model.State` and `TailAtTo` mapping each id to a small `{State, Since}` pair (one shared resolution at each bound — a resolving event contributes its own `TS`, a look-ahead its `PrevSince`; the from-side keeps only the state, the to-side keeps the state and its start; ids absent when events cannot say), and `WatchingAtFrom bool` (true only when the latest lifecycle event at-or-before `from` is a `start`; no lifecycle evidence at all — including the span before the ledger's first `start` — is false, so pre-ledger time renders unwatched rather than backfilled).
- `Prune(name string) (int, error)` — the offline curation operation behind `scry prune`: strict-read each segment, drop every transition event under the name (lifecycle events are estate-scoped and untouched), rewrite the segment through the same temp-sync-rename discipline, delete a segment left empty, and return the count removed. It never runs inside the daemon — the CLI constructs a store of its own, and the command documents plainly that the daemon must be stopped, the same unguarded-file property the state file already has. `Prune` strict-reads, so it refuses a malformed ledger — it is a curation tool for a healthy one, never a corruption remedy.

**Append discipline.** An event's segment is `<year of TS>.jsonl`. Open per append with `O_CREATE|O_WRONLY|O_APPEND` mode 0600, one `Write` of the complete line, `Sync`, close; sync the directory when the open created the segment. Per-append open trades a cached descriptor for statelessness across year rollover, at a cost that is nothing at dozens of events per day. Year rollover therefore needs no special case: the event's own timestamp names its file.

**Concurrency.** The engine serializes appends, and history reads join the same serialization: the API's window query runs as a read-only engine command (stages 2 and 3), so the store needs no lock at all — appends and reads interleave only at command granularity, and a torn or missing tail is impossible by construction.

**Reads.** `Window` reads every segment, ascending, strict-binding each line; the walk-back, the rule-two look-ahead (the earliest qualifying transition after `from` may fall past `to`, even in a later year — sometimes the only evidence a quiet check existed at `from`), and the in-range collection happen in one pass, with `Events` returning only `[from, to]`. Event order is append order: segments in name order, lines in file order, and equal timestamps keep that order as a stable tiebreak — a conservative-closure `stop` followed by a `start` at the same instant never inverts. Reading the whole ledger to serve a window is a deliberate simplicity: the arithmetic in the spec says the whole ledger stays small, and the moment that stops being true is the sqlite trigger, not an optimization trigger.

**Tests.** Append/read round-trip including detail-omitted; boot on an empty directory (start only); boot closing an unclean ledger at `max(saved, newest event)` — the quiet crash landing on `saved`, and the zero-stamp cases including the crash between a first `start` and the first stamped save (never two `start`s without a `stop`); boot failing on a malformed segment, oldest included; the orphan warning emitted once per unconfigured id, including one whose events live only in the oldest segment; `Prune` dropping exactly one name's transition events and nothing else, atomically, reporting the count, deleting an emptied segment, and refusing a malformed ledger; year rollover landing events in per-year files; `Window` resolution table (rule one, rule two with the `prev_since` cap both admitting and refusing, absent, and a cross-year case whose only resolving transition falls after `to`); `WatchingAtFrom` across stop/start placements and false before the first lifecycle event; a stop-then-start pair at one instant staying ordered; malformed line failing `Window`.

## Stage 2 — the saved stamp and engine integration

**`internal/state/store.go`.** `diskFile` gains `Saved *time.Time dd:"saved"`. `Save(snapshot, at)` writes it; `Load` returns it (zero when absent, so pre-stamp files load unchanged — no version bump, the field is additive-optional). Old binaries reading a stamped file fail strict binding; upgrades are forward-only and that failure is loud, which is the house posture.

**`internal/engine/engine.go`.** The `Repository` interface takes the new signatures. A new consumer-side seam:

```go
type Ledger interface {
    Boot(at, lastSaved time.Time, configured map[string]struct{}) error
    AppendTransition(model.Transition) error
    AppendStop(at time.Time) error
    Window(from, to time.Time) (history.Window, error)
}
```

(`AppendStart` stays store-internal to `Boot`; the engine never starts a ledger mid-run.)

`New(checks, repository, ledger, clock, transitions)` requires the ledger. Construction order: load (capturing `lastSaved`), reconcile records, `ledger.Boot(at, lastSaved, ids)` with the configured id set, then the reconciliation persist. Boot-before-persist means a history failure stops the daemon before it has rewritten the state file, and both paths are proven writable before steady state either way.

Each transition-producing handler persists first and appends after — `handleActive` and `handleReport` persist then append their single transition, `handleSweep` persists once then appends each transition — and an append error is fatal exactly as a persist error is. The persist-leads ordering is the spec's crash rule, chosen for simplicity: a crash between the two loses at most the final transition from the ledger — in the worst case (a sticky `failed` with no further transitions) the strip's tail disagrees with the row until the check next transitions — and in exchange, no boot reconciliation machinery exists at all.

`handleFlush` drops its `!dirty` early return: every flush persists, which is what keeps `saved` a live bound on daemon death. The clock stays read-once-per-command: each handler captures `at := engine.clock()` at its top and threads it through both the model application and the persist, rather than `persist` taking a second reading.

`Run`'s shutdown arm persists (now unconditionally — the final `saved` stamp) and then appends `stop` at the same captured time; a failed stop append surfaces as the run error.

The engine also gains a read-only `commandHistory`: `HistoryView(ctx, from, to *time.Time)` submits through the loop and returns `HistoryView{From, To, Generated, Window, Checks}` — the command's single clock read stamps `Generated`, resolves an omitted `to` to that same instant and an omitted `from` to the *resolved* `to` minus the 90-day default (the constant lives here now), validates the fully resolved pair — `from` before `to`, `to` not beyond the reading — replying any failure as a user error, and captures the ledger's `Window` and a registry-ordered records copy as one cut. The default window's right edge and the document's watermark are one value by construction — no transition can fall between them. This is the history document's consistency boundary: a concurrently applied transition is either wholly present (event, records, and a `generated` that postdates it) or wholly absent — no interleaving can produce a document missing an event older than its own `generated`. A `Window` error inside the command is a reply, never a fatal loop exit — request-scoped, as any post-boot read failure is.

**`internal/config/config.go`.** `HistoryDir string dd:"history_dir"` with `defaultHistoryPath()` mirroring `defaultStatePath()` (`$XDG_STATE_HOME/scry/history`, else `~/.local/state/scry/history`); `Validate` rejects blank, matching `state_file`.

**`cmd/scry/main.go`.** Construct `history.NewStore(cfg.HistoryDir)`, pass it to `engine.New`, and keep the store reference for stage 3's handler wiring. The debug config line gains `history_dir`. A new `scry prune <name>` cobra subcommand (`cmd/scry/prune.go`) resolves config the normal way, constructs its own stores, and retires the name from both truths: the ledger via `Prune`, and the state file via load-delete-save — preserving the loaded `saved` stamp, since prune makes no liveness claim, and needing no new store method. It prints the counts in the house lowercase style and states plainly that it runs against a stopped daemon; a still-configured id resumes as a fresh baseline at the next boot, exactly the reset the tool promises.

**Tests.** A recording fake ledger asserts: exactly one append per transition including notification-silent ones (active `ok`→`late`); persist-before-append call order; the single sweep persist before its per-transition appends; fatal on append error; `Boot` receiving the loaded stamp and the configured id set; shutdown appending stop after the final persist. State store round-trips `saved` and loads pre-stamp files as zero. Flush-when-clean advances `saved` (fake repository observes the second save). `HistoryView` returning a coherent cut — a transition applied around the command is wholly present or wholly absent, both interleavings — replying rather than exiting on a `Window` error, and resolving default bounds in-command (a transition landing after the handler's dispatch but before the command still falls inside the default window); one-sided bounds resolving as specified — from-only and to-only, an omitted `from` anchoring to the resolved `to` — with the resolved pair validated in-command (a future `from` with omitted `to` rejected). The prune command removing the named state entry while preserving `saved`, so a pruned-then-reused same-kind id resumes as a fresh baseline and renders nothing pre-prune. Config default, override, and blank-rejection.

## Stage 3 — the history contract

**`internal/api/specs/scry.yml`.** New path `/history`, `operationId: getHistory`, optional `from`/`to` query parameters (`date-time`), a 200 returning the `history` schema, a 400 returning `error`, and a 500 returning `error` — the request-scoped malformed-ledger failure, declared where consumers can see it:

- `history`: required `estate`, `generated`, `from`, `to`, `watching_at_from`, `checks`, `daemon`. `checks` is an array of `check_history`; `daemon` an array of `lifecycle_event`.
- `check_history`: required `id`, `kind` (the registry's value — per-event kind stays the authoritative attribution), `state_at_from` (nullable `state`), `state_at_to` (nullable `state`; the state at `to`, the tail band's state), `since` (nullable date-time; when `state_at_to`'s state began, resolved with it as a pair — null exactly when `state_at_to` is null), `events` (array of `transition_event`).
- `transition_event`: required `ts`, `kind` (the check's kind when the event fired, straight off the ledger line), `from`, `to`, `prev_since` (date-time; when the from-state began — backward claims cap here), `detail` (nullable string) — `from`/`to` reuse the existing `state` schema.
- `lifecycle_event`: required `ts`, `event` (enum `start`, `stop`).
- `error`: required `message`.

The `status` schema gains one required additive field: `started` (`date-time`), the daemon's current boot time. `statusHandler` renders it from a new `Started()` accessor on the engine — the construction-time clock reading that also stamps the ledger's `start` event — threaded through `NewHandler` by `main`. This is what makes a restart observable to an open page (stage 4's refetch condition).

`make generate` regenerates both sides; the TypeScript types arrive for stage 4 free.

**`internal/server/history.go`.** A `historyHandler` alongside `statusHandler`, holding the clock, the estate name, and a server-side seam over the engine's serialized read (`type HistoryReader interface { HistoryView(ctx context.Context, from, to *time.Time) (engine.HistoryView, error) }`) — it needs no snapshot `Reader`; the records arrive inside the view's cut. `newHandler` and `NewHandler` gain the parameter; `main` passes the engine.

`GetHistory`: pass the request's optional bounds straight into `HistoryView` — defaulting happens inside the serialized command, against the same clock read that stamps `generated`, so the default window's edge and the watermark cannot diverge. All bound validation lives in the command, on the fully resolved pair; the handler translates the command's user-error reply into the 400 shape — one gate, not two half-gates. Build the document from the view's resolved bounds: every check of the view's records in registry order — `state_at_from` from the resolution at `from` (event rules, else the current state when the record's `Since` is at or before `from`, else null); `state_at_to` and `since` as the pair resolved at `to` by the same rules, the live record supplying (current state, `Since`) only when no event resolves and its `Since` is at or before `to`, both null when nothing does; its in-window transition events ascending. Ledger events under ids absent from the registry never enter the document — it speaks only for configured checks, and boot already warned about the orphans. `daemon` is the in-window lifecycle events; `watching_at_from` from the window. All timestamps leave UTC. A `Window` error returns as a 500 — the request fails, the daemon does not.

**Tests.** Bounds defaulting under a fixed clock, including from-only and to-only requests (an omitted `from` anchors to the resolved `to`); 400 on inverted resolved bounds — a future `from` with omitted `to` included — and on a future `to`; a resolution table covering all four `state_at_from` rules, including a mid-window-born check whose rule-two claim is refused because its first event's `prev_since` falls after `from`; `state_at_to` and `since` resolved as a pair at a historical `to` preceding the latest transition — an eventless window resolved by a look-ahead's `prev_since` included — the same-state kind change not erasing watched time, and the default window where both bounds agree; events under unconfigured ids excluded from the document; silent transitions present in events; per-event kind preserved across a kind change; lifecycle events and `watching_at_from` passthrough; a `Window` read failure returning 500; and the enum-equality test extended so the contract's `lifecycle_event` and reused `state` vocabularies stay pinned to the model, in the pattern of `TestContractEnumerationsMatchTheModel`.

## Stage 4 — the dashboard strip

**`ui/src/api/client.ts`.** `fetchHistory(signal)` against `/api/history` with no parameters — the server's defaults are the page's window — typed from the regenerated schema.

**`ui/src/history.ts`.** A pure function from document to render intervals, kept out of the components so the logic is inspectable in one place: walk each check from `from` in its `state_at_from` (null opens a blank span), splitting at each event; a null-opened blank span ends at the first event's `prev_since` (the ok baseline runs from there to the event), and the tail band is the document's `state_at_to`/`since` pair — backward claims always cap at the existence boundary. Derive unwatched spans from `watching_at_from` toggled through the daemon events; the unwatched overlay wins over state claims and blank alike — where the daemon cannot testify, the strip claims neither a state nor nonexistence — and blank-before-existence wins only within watched time, where not-yet-registered is actually knowable. The builder takes the display bounds as inputs — derived from the status document per the StateStrip section below — clips every interval to them, and extends the final interval to the right edge. The tail band is the document's own pair — `state_at_to` from `since` to the right edge, which subsumes the last-event floor by construction and is also what gives an eventless mid-window-born check its band. The builder reads the history document alone; the status document's only roles are refetch invalidation and the display clock, so two independently captured documents can never disagree inside the strip.

The acceptance table is the builder's contract and lands as executable vitest assertions — a dev-dependency and one script line; nothing shipped changes, and the embedded bundle keeps its zero-remote-request property. Days are offsets into the display window `[L, R]`:

| scenario | given | expected bands |
| --- | --- | --- |
| pre-ledger blankness | first `start` at d30; check live since long before `L`; no events; `state_at_from` ok; `watching_at_from` false | unwatched L→d30, ok d30→R |
| mid-window registration | check born at d40 (`since` d40); no events; `state_at_from` null; `state_at_to` ok | blank L→d40, ok d40→R |
| outage overlay | ok→failed at d49 (`prev_since` d10); `stop` d50, `start` d52; failed→ok at d55 (`prev_since` d49) | ok L→d49, failed d49→d50, unwatched d50→d52, failed d52→d55, ok d55→R |
| quiet-window aging | failed→ok at L+1; no later events; window slides with the pulse | failed L→L+1 (clipped), ok L+1→R; once the left edge passes L+1 the failed band is gone |
| stale status | status polling failing since d89; pulse keeps advancing R | bands as computed …→d89, unwatched d89→R, the span growing until polling recovers |
| dirty history | restart refetch failed (`historyDirty` set); status polls fresh; history document `generated` d88 | bands as computed …→d88, unwatched d88→R until a refetch lands |
| fresh-ledger adoption | record `since` d40; no events; `state_at_from` null; ledger's first `start` d42; `state_at_to` ok | unwatched L→d42, ok d42→R |

**`ui/src/components/StateStrip.tsx`.** Renders one check's intervals across the fixed window. Requirements over mechanism: proportional placement; the existing state palette for bands; a visually distinct non-state treatment for unwatched gaps (reads as absence, not health or trouble); nothing rendered before the check existed; and exact proportional endpoints always, with short non-ok bands and gaps kept discoverable by a marker overlaid at the true position rather than a widened band — a thirty-minute overnight failure is 0.02% of ninety days and must not vanish, but an instrument does not round up. The display bounds come from the status document, not the history document: right = the latest status `generated` plus `ageOffset` (the page's live now, the header's own arithmetic), left = right minus the 90-day constant. Every interval clips to those bounds and the tail band extends to the right edge, so the window slides with the pulse and old incidents age out the left side. While status is stale or history is dirty, bands cap at the last instant the page can vouch for — the stale status document's `generated` or the dirty history document's `generated`, whichever is earlier — and the remainder to the right edge renders unwatched: the strip never claims time the daemon cannot vouch for, whether the page cannot reach the daemon or knows its document is obsolete.

**`ui/src/App.tsx` / `CheckTable.tsx`.** App owns the history document beside the status document, invalidated by one level-triggered `historyDirty` flag: set when a status poll delivers a `last_transition` newer than the history document's `generated` or a `started` differing from the one the document was fetched under (App remembers that stamp beside the document — a restart's gap arrives without any transition), and set by any failed history fetch, the missing document at first load included. It clears only on a successful fetch; every successful status poll retries while set — no second cadence, no edge to lose. The quiet estate refetches never. The dirty decision itself is a pure helper beside the interval builder — (current flag, status document, fetch outcome) in, next flag out — so App carries only the wiring. A failed history fetch keeps the last document, same stale posture as status. `CheckTable` renders the strip as a slim full-width sub-row beneath each check's row as the starting point — placement, height, and treatment are explicitly the eye pass's to adjust, per the design conversation.

**`ui/src/style.css`.** Strip and gap styles from the existing palette tokens.

Vitest arrives as the UI's first test runner, dev-only, carrying two pure suites: the acceptance table for the interval builder, and the dirty-flag helper's lifecycle — initial load, transition invalidation, restart invalidation, repeated failure, successful clearing. The CI test job gains `npm --prefix ui run test` beside the existing frontend build, and `npm run build`'s typecheck and the terminus gate carry the rest.

## Stage 5 — synthesis and close-out

Fold built behavior into `docs/current/`: the ledger, lifecycle events, and gap arithmetic into `engine-and-state.md` (persistence section grows the `saved` stamp, the unconditional flush, and the append ordering); `history_dir` and the `prune` command surface (with its stopped-daemon requirement) into `configuration.md`; the `/history` route and the status document's `started` field into `status-api.md`; the strip and its refetch discipline into `dashboard.md`; and `history_dir: /var/lib/scry/history` into `deployment.md`'s systemd example beside `state_file` — the same boot-fatal reasoning the doc already applies there, extended to the new path. Amend `seam-census.md` per the spec's own census section: the fence rewritten to guard sampled series and their graphs with the transition ledger recorded as the judged exception and "permanent" softened to standing judgment; the error-tier line extended to history's one-law rule (reads and writes fatal at boot and runtime, post-boot query reads request-scoped I/O); and the new entries carried over — operator-owned identity continuity, and the serialized history read as the recorded exception to the published-snapshot law. A separate `docs/current/state-history.md` only if the folds bloat their hosts — default is fold.

`CHANGELOG.md` gains a FEATURE entry under `## Unreleased`. The deferred detail view (longer horizons, zoom, transition list) is re-synthesized as a fresh roadmap card in `inbox`. Then the spec and this work order are removed per the close-out convention — `docs/current/` and the code carry the value, git history keeps the documents. The `state-history.md` roadmap card's disposition is Michael's call at close-out.

## Critical Files

| area | files |
| --- | --- |
| ledger | `internal/history/store.go` (new), `internal/history/store_test.go` (new) |
| state stamp | `internal/state/store.go` |
| engine | `internal/engine/engine.go` |
| config | `internal/config/config.go` |
| wiring | `cmd/scry/main.go`, `cmd/scry/prune.go` (new) |
| contract | `internal/api/specs/scry.yml` (then `make generate`) |
| server | `internal/server/history.go` (new), `internal/server/handler.go` |
| dashboard | `ui/src/api/client.ts`, `ui/src/history.ts` (new), `ui/src/components/StateStrip.tsx` (new), `ui/src/components/CheckTable.tsx`, `ui/src/App.tsx`, `ui/src/style.css` |
| docs | `docs/current/engine-and-state.md`, `configuration.md`, `status-api.md`, `dashboard.md`, `deployment.md`, `seam-census.md`, `CHANGELOG.md` |

## Dependencies and Migration

No new Go modules — the ledger is stdlib plus `df/dd`, like the state store. No new npm packages — the strip is hand-rolled markup, keeping the zero-remote-request property untouched.

Migration is a single quiet boot: a pre-stamp state file loads with a zero `saved` (an empty ledger needs no closure; a crash before the first stamped save closes at the newest ledger event on the next boot), the history directory is created, `start` is appended, and history begins. No state file version bump; no backfill exists by design. Rollback to an old binary after a stamped save fails loudly on the unknown `saved` key — forward-only, consistent with how the estate is actually operated.

## Stage Dependencies

Stages land in order: 2 needs 1's store, 3 needs 2's wiring and 1's `Window`, 4 needs 3's generated types. Each stage leaves the daemon fully runnable — stage 1 is a new package nothing calls yet, stage 2 writes a ledger nothing reads yet, stage 3 serves a document nothing renders yet — so review and landing stay per-stage with no dark period.
