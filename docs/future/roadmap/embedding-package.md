---
title: embedding package
state: inbox
created: 2026-07-29
tags: [feature]
source: docs/future/scry-spec.md (retired at v1 close-out)
---

A small Go package letting any long-running application register check functions in-process and answer the scry agent protocol over a zrok private share. This is reef's observation story: reef links the package, registers capacity/ingest/integrity checks, stands up a share, and becomes observable without scry learning anything about reef's internals. The protocol is settled on the agent-strategy card; the package is a conforming implementer, bound by the no-exec rule like every other.

## why

Follows the reference agent; reef is the first consumer. Alive things answer questions; ephemeral things leave notes — this is the "alive" half for the practice's own applications.
