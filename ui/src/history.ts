import type {
  CheckHistory,
  CheckState,
  HistoryDocument,
  LifecycleEvent,
  StatusDocument,
  TransitionEvent,
} from './api/client'

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
// was fetched under, whether it knows the document is behind, and when the
// document arrived. the arrival travels with the document and is what its
// ages count from — a status poll landing later resets nothing, so a page
// holding one document through many quiet polls shows ages that keep counting
// forward rather than snapping back to zero.
export interface HistoryState {
  document: HistoryDocument | null
  startedUnder: string | null
  dirty: boolean
  arrival: number | null
}

export type HistoryFetch = 'none' | 'succeeded' | 'failed'

interface Span {
  start: number
  end: number
}

// displayWindow derives the strip's bounds from the status document alone: the
// right edge is the daemon's own stamp plus how long the page has held it, the
// same arithmetic the header does, so the two clock frames never mix.
//
// staleCap is the page's one vouching cap, computed once per render and shared
// by every window the page derives: the latest good status document's generated
// stamp while polling is failing, and null otherwise, where each surface vouches
// to its own moving right edge and the cap is a no-op.
export function displayWindow(input: {
  status: StatusDocument
  ageOffset: number
  staleCap: number | null
  history: HistoryDocument | null
  historyDirty: boolean
}): DisplayWindow {
  const right = Date.parse(input.status.generated) + Math.max(0, input.ageOffset)
  let vouchedThrough = right
  if (input.staleCap !== null) {
    vouchedThrough = Math.min(vouchedThrough, input.staleCap)
  }
  // the document's testimony ends at the window it served, not at the stamp it
  // was rendered under: with explicit bounds the daemon reads its clock again
  // after resolving the window, so generated postdates the coverage.
  if (input.historyDirty && input.history) {
    vouchedThrough = Math.min(vouchedThrough, Date.parse(input.history.to))
  }
  return { left: right - displayWindowMs, right, vouchedThrough }
}

// vouchingCap selects the page's one shared cap: while polling is failing the
// latest good status document is the last testimony any surface holds, so its
// own stamp is the furthest instant any of them may speak for. with polling
// good there is no cap — each surface vouches to its own moving right edge, and
// the tail beyond a document's coverage is what the level-triggered flag
// vouches for. computed once per render and handed to every window the page
// derives, so the row strip and the panel can never cap differently.
export function vouchingCap(status: StatusDocument | null, stale: boolean): number | null {
  if (!stale || status === null) {
    return null
  }
  return Date.parse(status.generated)
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

  // a prefix the document does not cover is outside its testimony, exactly as
  // the span past vouchedThrough is at the other end. the page no longer builds
  // one: the strip's fetch sends the display's own bounds, so the document opens
  // exactly where the display does and the left edge only ever advances into
  // covered territory. the clause stands for any document that does not span the
  // window handed to it.
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
// can be in flight and land out of order; the end of the coverage each document
// serves orders them without a request counter to keep. an equal watermark is
// not stale — the two responses cover the same span, and refusing it would leave
// the page dirty forever, refetching on every poll.
export function supersedes(held: HistoryDocument | null, arriving: HistoryDocument): boolean {
  if (held === null) {
    return true
  }
  return Date.parse(arriving.to) >= Date.parse(held.to)
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
  // the document's testimony ends at the window it served, never at the stamp
  // it was rendered under. every comparison against that bound is at-or-after:
  // the wire carries whole-second stamps, so an instant reading equal to the
  // bound may be newer in truth, and equality is the case that must dirty. the
  // false positive costs one poll interval of the honest cap, which the next
  // poll's later bound clears.
  const to = Date.parse(state.document.to)
  // the same truncation can fold a restart into the bound's own second, where
  // the boot is the document's own and the inequality above sees nothing.
  if (Date.parse(status.started) >= to) {
    return true
  }
  for (const check of status.checks) {
    if (check.last_transition && Date.parse(check.last_transition) >= to) {
      return true
    }
    // registration is not a transition, so a check born after the held
    // document's coverage ended carries no last_transition to notice it by.
    // today that only happens at a boot the stamps above already caught; this
    // does not depend on that staying true.
    if (Date.parse(check.since) >= to) {
      return true
    }
  }
  return state.dirty
}

// the panel's window presets. spans are fixed millisecond constants — the same
// arithmetic displayWindowMs runs, day = 24h and 1y = 365 days, never calendar
// math — so a window is the same length whatever month it opens in.
export type Preset = '1d' | '14d' | '30d' | '90d' | '180d' | '1y' | 'all'

// the order the panel offers them in, and the one the row strip already draws.
export const presets: Preset[] = ['1d', '14d', '30d', '90d', '180d', '1y', 'all']
export const defaultPreset: Preset = '90d'

const dayMs = 24 * 60 * 60 * 1000

// all is the one preset with a fixed left bound rather than a span: the daemon
// serves whatever the ledger holds, and the years before its first start render
// unwatched, which is the claim they make.
export const allWindowFrom = Date.parse('2000-01-01T00:00:00Z')

export const presetSpans: Record<Preset, number | null> = {
  '1d': dayMs,
  '14d': 14 * dayMs,
  '30d': 30 * dayMs,
  '90d': displayWindowMs,
  '180d': 180 * dayMs,
  '1y': 365 * dayMs,
  all: null,
}

// what the panel draws from: one document, the window over it, and the instant
// that document arrived. the three travel together because the list's ages are
// only truthful against the document they were computed from.
export interface DetailSource {
  document: HistoryDocument
  window: DisplayWindow
  arrival: number
}

// detailWindow is the display-window law applied to the panel's own document:
// the right edge is the coverage the document served plus how long the page has
// held it, and the left edge is the preset's span back from there. every preset
// is now-anchored, so no window centers on a past instant the document cannot
// vouch for.
export function detailWindow(
  document: HistoryDocument,
  preset: Preset,
  ageOffset: number,
  staleCap: number | null,
  dirty: boolean,
): DisplayWindow {
  const to = Date.parse(document.to)
  const right = to + Math.max(0, ageOffset)
  const span = presetSpans[preset]
  let vouchedThrough = right
  if (dirty) {
    vouchedThrough = Math.min(vouchedThrough, to)
  }
  // the same shared cap the row strip takes. while polling is failing the
  // latest good status document is the last testimony either surface has, so a
  // panel whose document has sat held for days still vouches the recent window
  // the page holds testimony for rather than hatching it.
  if (staleCap !== null) {
    vouchedThrough = Math.min(vouchedThrough, staleCap)
  }
  return { left: span === null ? allWindowFrom : right - span, right, vouchedThrough }
}

// detailSource decides which document the panel renders from. at the default
// preset it is the row strip's — document, window, and arrival as one unit — so
// the panel and the row beneath it are the same cut and cannot disagree. every
// other preset renders the panel's own held document against its own arrival.
export function detailSource(input: {
  preset: Preset
  strip: HistoryState
  stripWindow: DisplayWindow | null
  panel: HistoryState
  now: number
  staleCap: number | null
}): DetailSource | null {
  if (input.preset === defaultPreset) {
    const { document, arrival } = input.strip
    if (document === null || arrival === null || input.stripWindow === null) {
      return null
    }
    return { document, window: input.stripWindow, arrival }
  }
  const { document, arrival, dirty } = input.panel
  if (document === null || arrival === null) {
    return null
  }
  return {
    document,
    window: detailWindow(document, input.preset, input.now - arrival, input.staleCap, dirty),
    arrival,
  }
}

// detailLands is the panel's two-condition landing rule. the watermark orders
// responses to the same window but cannot say which window a response answered,
// so a preset change mid-flight would otherwise let a request for an abandoned
// window land. the echoed bounds must carry the shape the page asks for now,
// and the echoed coverage must reach the newest status the page holds — which
// is what rejects a response to an abandoned visit to the same preset, where
// the shape matches and only the older coverage gives it away.
export function detailLands(
  arriving: HistoryDocument,
  preset: Preset,
  status: StatusDocument | null,
): boolean {
  const from = Date.parse(arriving.from)
  const to = Date.parse(arriving.to)
  const span = presetSpans[preset]
  if (span === null) {
    if (from !== allWindowFrom) {
      return false
    }
  } else if (to - from !== span) {
    return false
  }
  if (status !== null && to < Date.parse(status.generated)) {
    return false
  }
  return true
}

// requestBounds resolves the explicit window the panel asks the daemon for: the
// coverage end it is dispatched under, and the preset's span back from there —
// or all's fixed bound, which has no span. it is one half of a two-halved rule:
// detailLands checks an arriving response against the same preset table, so a
// response to bounds this produced must be one that rule accepts. the two are
// tested against each other, not only apart.
export function requestBounds(preset: Preset, to: string): { from: string; to: string } {
  const span = presetSpans[preset]
  const opens = span === null ? allWindowFrom : Date.parse(to) - span
  return { from: new Date(opens).toISOString(), to }
}

// one row of the transition list. daemon rows are estate-scoped and carry no
// check; they earn their place because a gap between two of a check's events is
// often nobody watching, and these rows say so in the ledger's own notation.
export type EventRow =
  | { kind: 'transition'; ts: number; event: TransitionEvent }
  | { kind: 'daemon'; ts: number; event: LifecycleEvent }

// eventRows builds the list, newest first: the check's transitions interleaved
// in time with the estate's lifecycle events, both filtered to the panel's
// display window so the list slides with the strip and the two surfaces
// describe one span. the window is closed at both ends — with whole-second wire
// stamps, a row landing exactly on a bound is a common case, not an edge.
//
// the upper bound is the window's right edge and not its vouchedThrough, where
// the strip's own builder caps. the asymmetry is deliberate and costs nothing:
// every row here comes out of the document, the daemon resolved the document
// inside [from, to], and both caps that shorten vouchedThrough sit at or after
// that to — the document's own to while dirty, and a later status stamp while
// stale. no row can land in the unvouched suffix, so capping the list there
// would only split the one span the strip and the list are meant to share.
export function eventRows(
  document: HistoryDocument,
  entry: CheckHistory,
  window: DisplayWindow,
): EventRow[] {
  const rows: EventRow[] = []
  for (const event of document.daemon) {
    const ts = Date.parse(event.ts)
    if (ts >= window.left && ts <= window.right) {
      rows.push({ kind: 'daemon', ts, event })
    }
  }
  for (const event of entry.events) {
    const ts = Date.parse(event.ts)
    if (ts >= window.left && ts <= window.right) {
      rows.push({ kind: 'transition', ts, event })
    }
  }
  // rows sharing a wire timestamp break ties in a fixed display order. the
  // contract truncates to whole seconds, so the ledger's sub-second order is
  // unrecoverable: this is a presentation rule that keeps the list identical
  // between renders, not a claim about which happened first. the sort is
  // stable, so equal rows of one kind keep the ledger's own order.
  return rows.sort((left, right) => right.ts - left.ts || rowRank(left) - rowRank(right))
}

function rowRank(row: EventRow): number {
  return row.kind === 'daemon' ? 0 : 1
}

// checkTransitions counts the rows the empty message speaks for. the message is
// the check's own claim, so only its transitions count: a window carrying
// nothing but the estate's lifecycle rows renders them and still says the check
// did not transition.
export function checkTransitions(rows: EventRow[]): number {
  return rows.filter((row) => row.kind === 'transition').length
}

// panelFetches reports whether the panel has a second document to fetch at all.
// the default preset reuses the strip's, and a closed panel has nothing to
// render — but neither fact may touch the dirty level, only the request.
export function panelFetches(open: boolean, preset: Preset): boolean {
  return open && preset !== defaultPreset
}

// what one successful status poll decides, before it dispatches anything.
export interface PollPlan {
  stripDirty: boolean
  panelDirty: boolean
  fetchStrip: boolean
  fetchPanel: boolean
}

// pollPlan is the loop's whole scheduling decision, taken in one block against
// one status document: both dirty levels are evaluated against that document
// and both flags are settled before either request is dispatched, so neither
// surface may extend obsolete state during the other's in-flight fetch and no
// panel request goes out under a status stamp the landing rule would reject.
// the panel's level is maintained whether or not the panel is open — a closed
// panel may hold an obsolete document but may not let it age clean — and only
// the request is gated on the panel.
export function pollPlan(input: {
  strip: HistoryState
  panel: HistoryState
  status: StatusDocument
  panelOpen: boolean
  preset: Preset
}): PollPlan {
  const stripDirty = nextHistoryDirty(input.strip, input.status, 'none')
  const panelDirty = nextHistoryDirty(input.panel, input.status, 'none')
  return {
    stripDirty,
    panelDirty,
    fetchStrip: stripDirty,
    fetchPanel: panelDirty && panelFetches(input.panelOpen, input.preset),
  }
}
