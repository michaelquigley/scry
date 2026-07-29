---
title: ziti-native ingest listener
state: inbox
created: 2026-07-29
tags: [enhancement]
source: docs/future/scry-spec.md (retired at v1 close-out)
---

A second ingest transport beside the HTTP listener: reports arriving over a ziti service instead of the zrok-fronted loopback listener, stamping into the model through the same path. The model/transport seam was built for exactly this — the status model never learns about HTTP, tokens, or the overlay, so the addition touches no model code.

## why

Speculative until overlay-native reporters exist; the zrok share covers every curl-capable host today. Kept as a card because the seam's whole justification was that this addition stays clean — if it ever isn't, that's census signal.
