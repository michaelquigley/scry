---
title: incident centered history windows
state: inbox
created: 2026-08-19
tags: [enhancement, spike]
milestone: v0.1.x
log:
  - stamp: 2026-08-19
    note: docs/journal/2026-08-19.md
---

Decide whether the detail panel's strip should ever show a *past* window — and if so, build the interaction: panning, brushing a span, or centering the strip on a row clicked in the transition list. Today every preset is now-anchored, so the panel is a zoom on the recent horizon and the past lives in the list alone.

The truth machinery already reaches: a window around a past span renders honestly when clipped to its own explicitly-bounded document, since the bands within `[from, to]` speak for exactly that span. The one piece that does not carry over is the tail — a historical window's right edge cannot be vouched by the current status document, because a restart in the seconds after the bound would leave the current state compatible with the historical tail while the gap stays unknowable. So a past window must render its own right edge as the document's `to` and claim nothing beyond it.

## why

Deferred from the per-check-history-detail-view design as scope and interaction, not truth. The open question is whether a past window is ever *wanted* on a strip, given the list already answers "what happened in that span" in the daemon's own words — which covers the retrospective use the detail view was built for. That is an eye-pass judgment: look at the shipped panel first and see whether reaching for a past window is a real reflex or a feature nobody asks for.

Answer that before building anything. If the answer is no, close this card — the deferral becomes a settled limit rather than a backlog item.
