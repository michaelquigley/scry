---
title: per-check history detail view
state: horizon
created: 2026-08-11
tags: [feature]
milestone: v0.1.x
source: docs/future/state-history-spec.md (retired at close-out)
---

A per-check detail view over the recorded history: longer horizons than the row strip's fixed 90 days, a zoomable window, and the transition list with each event's detail text. `GET /api/history` already serves arbitrary windows and carries per-event `kind` and `detail`, so this is render work — no daemon change is expected beyond what the strip already reads.

## why

Deferred from the state-history design rather than dropped: the call was to get the row strip onto the page first and let the eye pass judge it before designing a second surface. The strip answers "how often, how long, and was anyone watching" at a glance; the detail view is for the follow-up question — what actually happened, in order, with the daemon's own detail strings.

## background

Open at close-out: the strip's window and gap treatment are deliberately loose, and the detail view's shape should be decided after the strip has been looked at in a browser. The one known rough edge is the left-edge marker tick that stands in for the few milliseconds between the status and history documents' windows, which may want a different treatment once the strip is seen at real width.
