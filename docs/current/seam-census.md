# Seam Census and Fences

The design decisions v1 was reviewed against, recorded as built. The originating spec and work order were retired at the v1 close-out (git history keeps them); this page and the code carry the settled law. Terminus judges changes against these seams — the scry rubric in `terminus-canon` encodes the blocking set.

## The census

- **model / transport — separate, unconditional.** Nothing in `internal/model` or `internal/engine` knows HTTP, tokens, wire formats, or the overlay. Ingest stamps reports into the model from outside; the zrok share fronting the ingest listener lives outside the process entirely. Revisit: never.
- **model / render — separate.** The JSON status API (`internal/api/specs/scry.yml`, generated with ogen) is the single walk of the model; the dashboard and any future consumer (the MCP reader card) eat exactly that document. Sorting is a render decision; evolution is additive only. Revisit: never.
- **contract circumvention.** `CheckStrategy` is the active-probe contract (`tcp`, `http`); `Notifier` is the delivery contract; nothing reaches around either. Passive checks implement no strategy interface — their results are their ingest reports, and window arithmetic is a pure model function the engine calls on sweep ticks. The future agent protocol's answer surface carries the same weight, plus the no-exec rule: an agent answers declarative checks only, never commands — an exec surface is the one change that voids the design. Revisit: never.
- **error by tier.** Configuration failures die at boot, and so does a state file that exists but does not parse whole (missing is first boot); a failed state save is fatal at runtime — scry does not run with state it cannot persist. Strategy failures are *results*, never errors. Notifier failures log and retry (dispatcher-owned deadlines and unbounded per-notifier FIFOs) without ever blocking the engine. Revisit: if a failure class appears that fits none of the tiers.
- **the engine owns time through an injected clock.** No wall-clock reads under `internal/`; `cmd/scry` wires `time.Now` once. Tickers pace, the injected clock decides.
- **listener isolation, two locks.** Ingest (loopback, bearer tokens, `/report/*` only) and status (LAN, API + dashboard) are separate listeners with disjoint handler trees; config validates the authored ingest address and the server verifies the actual bound address. Cross-surface 404s are tested in both directions. A leaked share URL discloses nothing — not states, and (since the uniform-401 amendment) not registry membership either.

## The fences

- **Metrics and time-series: permanent.** scry is a status system — green/late/failed and when it last changed. The moment it grows time-series storage and graphs it becomes a bad Prometheus, and the low-maintenance property dies. Instrumentation machinery is refused even in generated code (ogen's otel integration is disabled).
- **Auto-discovery: permanent**, with one bounded exception already designed: dynamic enumeration of a registered agent's children. The registry stays hand-curated — at this scale, curation is cheap and drift is visible.
- **Digests, quiet hours, escalation: indefinite.** Transition-only notification with paired announcements should not need them; if it does, that is check-hygiene signal before it is feature signal.
