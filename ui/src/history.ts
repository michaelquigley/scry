import type { CheckHistory, CheckState, HistoryDocument, StatusDocument } from './api/client'

// the render window, matching the daemon's own default. the strip slides with
// the page's live clock, so old incidents age out the left side.
export const displayWindowMs = 90 * 24 * 60 * 60 * 1000

export type IntervalKind = 'state' | 'blank' | 'unwatched'

// one rendered band. blank is time before the check existed; unwatched is time
// no daemon can testify to. they are different claims and read differently.
export interface Interval {
  kind: IntervalKind
  state?: CheckState
  start: number
  end: number
}

export interface DisplayWindow {
  left: number
  right: number
  // the last instant the page can vouch for. everything after it renders
  // unwatched, whether the page cannot reach the daemon or already knows its
  // document is obsolete.
  vouchedThrough: number
}

// a band narrower than this reads as nothing at any sane strip width. rather
// than widen it — an instrument does not round up — the band keeps its exact
// proportional geometry and gets a fixed-width marker at its true position.
export const markerThresholdPercent = 0.4

// one interval placed across the strip, in percentages of the display window.
// left and width are exact; marked says the band is too short to see and needs
// a marker drawn at center instead.
export interface Placement {
  interval: Interval
  left: number
  width: number
  center: number
  marked: boolean
}

// the page's own history state: the last document it has, the daemon boot it
// was fetched under, and whether it knows the document is behind.
export interface HistoryState {
  document: HistoryDocument | null
  startedUnder: string | null
  dirty: boolean
}

export type HistoryFetch = 'none' | 'succeeded' | 'failed'

interface Span {
  start: number
  end: number
}

// displayWindow derives the strip's bounds from the status document alone: the
// right edge is the daemon's own stamp plus how long the page has held it, the
// same arithmetic the header does, so the two clock frames never mix.
export function displayWindow(input: {
  status: StatusDocument
  ageOffset: number
  stale: boolean
  history: HistoryDocument | null
  historyDirty: boolean
}): DisplayWindow {
  const generated = Date.parse(input.status.generated)
  const right = generated + Math.max(0, input.ageOffset)
  let vouchedThrough = right
  if (input.stale) {
    vouchedThrough = Math.min(vouchedThrough, generated)
  }
  if (input.historyDirty && input.history) {
    vouchedThrough = Math.min(vouchedThrough, Date.parse(input.history.generated))
  }
  return { left: right - displayWindowMs, right, vouchedThrough }
}

// buildStrip renders one check as bands across the display window. the history
// document is the only source: state claims come from its resolved bounds and
// its events, and the daemon's own lifecycle decides where no claim may stand.
export function buildStrip(
  document: HistoryDocument,
  entry: CheckHistory,
  window: DisplayWindow,
): Interval[] {
  const bands = stateBands(entry, Date.parse(document.from), window.right)
  const gaps = unwatchedSpans(document, window)

  const kept: Interval[] = []
  for (const band of bands) {
    let remaining = [band]
    for (const gap of gaps) {
      remaining = remaining.flatMap((interval) => subtract(interval, gap))
    }
    kept.push(...remaining)
  }
  for (const gap of gaps) {
    kept.push({ kind: 'unwatched', start: gap.start, end: gap.end })
  }

  return kept
    .map((interval) => clip(interval, window))
    .filter((interval): interval is Interval => interval !== null)
    .sort((left, right) => left.start - right.start)
}

// stateBands walks the check from the window's start, splitting at each event.
// a check with no state at the start opens blank, and that blankness ends at
// the first event's prev_since: the boundary the state being left began at,
// which is as far back as any claim may reach.
function stateBands(entry: CheckHistory, from: number, right: number): Interval[] {
  const bands: Interval[] = []
  let cursor = from
  let current: CheckState | null = entry.state_at_from ?? null

  for (const event of entry.events) {
    const at = Date.parse(event.ts)
    if (current === null) {
      const born = Math.max(cursor, Date.parse(event.prev_since))
      if (born > cursor) {
        bands.push({ kind: 'blank', start: cursor, end: born })
      }
      bands.push({ kind: 'state', state: event.from, start: born, end: at })
    } else {
      bands.push({ kind: 'state', state: current, start: cursor, end: at })
    }
    cursor = at
    current = event.to
  }

  // the tail is the document's own pair, which subsumes the last event's floor
  // and is also what gives an eventless mid-window registration its band.
  if (entry.state_at_to !== null && entry.since !== null) {
    const since = Math.max(cursor, Date.parse(entry.since))
    if (since > cursor) {
      bands.push({ kind: 'blank', start: cursor, end: since })
    }
    bands.push({ kind: 'state', state: entry.state_at_to, start: since, end: right })
  } else {
    bands.push({ kind: 'blank', start: cursor, end: right })
  }
  return bands
}

// unwatchedSpans derives where no state may be claimed: the document's own
// opening flag toggled through its lifecycle events, bracketed by whatever the
// page cannot vouch for at either edge.
function unwatchedSpans(document: HistoryDocument, window: DisplayWindow): Span[] {
  const spans: Span[] = []
  let watching = document.watching_at_from
  const documentFrom = Date.parse(document.from)
  let cursor = documentFrom

  // the two documents are fetched in sequence, so their default windows do not
  // align: the status-derived left edge can open before the history document's
  // own window does. that prefix is outside the document's testimony, exactly
  // as the span past vouchedThrough is at the other end.
  if (documentFrom > window.left) {
    spans.push({ start: window.left, end: documentFrom })
  }

  for (const event of document.daemon) {
    const at = Date.parse(event.ts)
    if (event.event === 'stop' && watching) {
      cursor = at
      watching = false
    } else if (event.event === 'start' && !watching) {
      spans.push({ start: cursor, end: at })
      watching = true
    }
  }
  if (!watching) {
    spans.push({ start: cursor, end: window.right })
  }
  if (window.vouchedThrough < window.right) {
    spans.push({ start: window.vouchedThrough, end: window.right })
  }
  return spans
}

// subtract removes a gap from one interval; the gap always wins, because where
// the daemon cannot testify the strip claims neither a state nor nonexistence.
function subtract(interval: Interval, gap: Span): Interval[] {
  if (gap.end <= interval.start || gap.start >= interval.end) {
    return [interval]
  }
  const remaining: Interval[] = []
  if (gap.start > interval.start) {
    remaining.push({ ...interval, start: interval.start, end: gap.start })
  }
  if (gap.end < interval.end) {
    remaining.push({ ...interval, start: gap.end, end: interval.end })
  }
  return remaining
}

function clip(interval: Interval, window: DisplayWindow): Interval | null {
  const start = Math.max(interval.start, window.left)
  const end = Math.min(interval.end, window.right)
  if (!(end > start)) {
    return null
  }
  return { ...interval, start, end }
}

// placeIntervals maps intervals onto the strip. every endpoint is exactly
// proportional — nothing is widened to be visible — and an interval a reader
// must not miss is marked instead, so a thirty-minute overnight failure stays
// findable at 0.02% of a ninety-day window without the geometry lying about
// how long it lasted.
export function placeIntervals(intervals: Interval[], window: DisplayWindow): Placement[] {
  const span = window.right - window.left
  if (!(span > 0)) {
    return []
  }
  return intervals.map((interval) => {
    const left = ((interval.start - window.left) / span) * 100
    const width = ((interval.end - interval.start) / span) * 100
    return {
      interval,
      left,
      width,
      center: left + width / 2,
      marked: mustNotBeMissed(interval) && width < markerThresholdPercent,
    }
  })
}

// mustNotBeMissed reports whether an interval is one a reader has to be able to
// find: trouble and absence stay discoverable at any duration, health does not
// need to.
function mustNotBeMissed(interval: Interval): boolean {
  return interval.kind === 'unwatched' || (interval.kind === 'state' && interval.state !== 'ok')
}

// supersedes reports whether an arriving document may replace the one the page
// already holds. the poll timer does not await its predecessor, so two fetches
// can be in flight and land out of order; each document's own watermark orders
// them without a request counter to keep. an equal watermark is not stale — the
// two responses describe the same instant, and refusing it would leave the page
// dirty forever, refetching on every poll.
export function supersedes(held: HistoryDocument | null, arriving: HistoryDocument): boolean {
  if (held === null) {
    return true
  }
  return Date.parse(arriving.generated) >= Date.parse(held.generated)
}

// nextHistoryDirty is the strip's whole refetch discipline: one level-triggered
// flag, set by anything that makes the held document obsolete, cleared only by
// a fetch that lands. every successful status poll retries while it is set, so
// there is no second cadence and no edge to lose.
export function nextHistoryDirty(
  state: HistoryState,
  status: StatusDocument | null,
  fetch: HistoryFetch,
): boolean {
  if (fetch === 'succeeded') {
    return false
  }
  if (fetch === 'failed') {
    return true
  }
  if (state.document === null) {
    return true
  }
  if (status === null) {
    return state.dirty
  }
  // a restart writes its gap into the ledger without producing any transition,
  // so the boot stamp is the only thing that reveals it.
  if (state.startedUnder !== null && status.started !== state.startedUnder) {
    return true
  }
  const generated = Date.parse(state.document.generated)
  for (const check of status.checks) {
    if (check.last_transition && Date.parse(check.last_transition) > generated) {
      return true
    }
    // registration is not a transition, so a check born after the held
    // document was generated carries no last_transition to notice it by. today
    // that only happens at a boot the stamp above already caught; this does
    // not depend on that staying true.
    if (Date.parse(check.since) > generated) {
      return true
    }
  }
  return state.dirty
}
