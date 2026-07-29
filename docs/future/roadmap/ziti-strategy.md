---
title: ziti strategy
state: inbox
created: 2026-07-29
tags: [feature]
source: docs/future/scry-spec.md (retired at v1 close-out; deferred out of v1 in planning 2026-07-24)
---

Add the `ziti` active strategy: dial a dark service by name over the openziti overlay, on the standard probe cadence — dial completes → ok, dial fails or times out → failed result with the dial error as detail. The daemon holds a single enrolled identity (config path, provisioned by hand); identity file missing or unparseable is a config-tier boot failure, controller unreachable at runtime is a failing *result*. Foreseen shape: a bare-dial strategy plus a ziti dialer option on the existing `http` strategy, keeping transport orthogonal to judgment — no new core.

## why

Deferred out of v1 so the daemon shipped with zero overlay dependencies. Lands together with the agent strategy (one overlay-dependency event instead of two). A completed ziti dial is a stronger health signal than a TCP SYN-ACK — the terminating SDK listener answering means the hosting process itself is alive.
