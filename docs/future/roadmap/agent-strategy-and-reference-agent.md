---
title: agent strategy and reference agent
state: inbox
created: 2026-07-29
tags: [epic]
source: docs/future/scry-spec.md (retired at v1 close-out; full text in git history)
---

Implement the scry agent follow-on: the daemon-side `agent` check strategy that dials a zrok private share and asks an agent its configured questions, and the reference agent — a small binary for hosts answering disk/unit/process checks from local config. The protocol below is settled; implement it, don't renegotiate it. This is the honest test of the strategy contract: the engine, `CheckStrategy`, and `Notifier` method signatures must not move — additive fields on `Result`, the persisted record, and the API are the intended extension point, and the engine already stores and forwards `Result` opaquely to make that mechanical.

## the protocol (settled in the v1 spec)

A scry agent is anything that answers this protocol over a zrok private share. The agent binds *outbound* into the overlay and listens on nothing — no inbound port, no firewall rule; the host's network posture is unchanged by becoming monitored. The daemon, holding an authorized identity, dials and asks; the agent answers and holds no state.

JSON over the share, versioned envelope from the first byte, no negotiation, no streaming:

```json
{"v": 1, "checks": ["disk-root", "unit-postgres"]}
```

An empty or absent `checks` array means *all*.

```json
{
  "v": 1,
  "results": [
    {"name": "disk-root", "status": "ok", "detail": "38% used"},
    {"name": "unit-postgres", "status": "failed", "detail": "inactive (dead)"}
  ]
}
```

`status` is `ok` | `failed`. That is the whole protocol.

**The no-exec rule is load-bearing and binds every implementer:** an agent answers a fixed, configuration-defined set of declarative checks and nothing else. There is no "run this command," and there never will be — an exec surface is the one change that voids the design (see `docs/current/seam-census.md`).

**The agent is the check; children are not checks.** One config entry, one share address, one registry entry, one state machine. The checks an agent reports enumerate dynamically as structured detail on the agent's single result and roll up into the agent's status: any child failed → agent failed; agent unreachable → agent failed, children stale. Notifications compose the failing child's name and detail into the agent's transition message. Curation at the boundary (what exists, where it lives); discovery within it (what the agent currently watches).

## why

The only v1 deferral requiring a second deployable, deferred precisely so it would land against a settled spec. It also carries the overlay dependency event (see the ziti-strategy card — the two land together) and is the prerequisite for the embedding package.
