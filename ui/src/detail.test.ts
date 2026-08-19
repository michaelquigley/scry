import { describe, expect, test } from 'vitest'
import type { CheckHistory, HistoryDocument, StatusDocument } from './api/client'
import {
  allWindowFrom,
  defaultPreset,
  detailLands,
  detailSource,
  detailWindow,
  displayWindowMs,
  checkTransitions,
  eventRows,
  panelFetches,
  pollPlan,
  presetSpans,
  presets,
  requestBounds,
  vouchingCap,
  type DisplayWindow,
  type EventRow,
  type HistoryState,
  type Preset,
} from './history'
import { formatTimestamp, outsideCurrentYear } from './util'

const dayMs = 24 * 60 * 60 * 1000
const anchor = Date.parse('2026-08-19T12:00:00Z')

function iso(at: number): string {
  return new Date(at).toISOString()
}

function document(overrides: Partial<HistoryDocument> = {}): HistoryDocument {
  return {
    estate: 'test estate',
    generated: iso(anchor + 40),
    from: iso(anchor - displayWindowMs),
    to: iso(anchor),
    watching_at_from: true,
    checks: [],
    daemon: [],
    ...overrides,
  }
}

function entry(overrides: Partial<CheckHistory> = {}): CheckHistory {
  return {
    id: 'web',
    kind: 'http',
    state_at_from: 'ok',
    state_at_to: 'ok',
    since: iso(anchor - dayMs),
    events: [],
    ...overrides,
  }
}

function transition(at: number, overrides: Partial<CheckHistory['events'][number]> = {}) {
  return {
    ts: iso(at),
    kind: 'http' as const,
    from: 'ok' as const,
    to: 'failed' as const,
    prev_since: iso(at - dayMs),
    detail: 'connection refused',
    ...overrides,
  }
}

function status(overrides: Partial<StatusDocument> = {}): StatusDocument {
  return {
    estate: 'test estate',
    generated: iso(anchor),
    started: iso(anchor - 400 * dayMs),
    rollup: { ok: 1, late: 0, failed: 0 },
    checks: [],
    ...overrides,
  } as StatusDocument
}

function held(overrides: Partial<HistoryState> = {}): HistoryState {
  return { document: document(), startedUnder: iso(anchor - 400 * dayMs), dirty: false, arrival: anchor, ...overrides }
}

describe('the preset table', () => {
  test('offers the spans the page speaks in, defaulting to the row strip', () => {
    expect(presets).toEqual(['1d', '14d', '30d', '90d', '180d', '1y', 'all'])
    expect(defaultPreset).toBe('90d')
  })

  test('measures every span as a fixed constant, never calendar math', () => {
    expect(presetSpans['1d']).toBe(dayMs)
    expect(presetSpans['14d']).toBe(14 * dayMs)
    expect(presetSpans['30d']).toBe(30 * dayMs)
    expect(presetSpans['180d']).toBe(180 * dayMs)
    expect(presetSpans['1y']).toBe(365 * dayMs)
  })

  test('matches the row strip exactly at the default preset', () => {
    expect(presetSpans[defaultPreset]).toBe(displayWindowMs)
  })

  test('gives all a fixed left bound rather than a span', () => {
    expect(presetSpans.all).toBeNull()
    expect(allWindowFrom).toBe(Date.parse('2000-01-01T00:00:00Z'))
  })
})

describe('the detail window', () => {
  test('anchors the right edge on the coverage served plus the age held', () => {
    const window = detailWindow(document(), '1d', 5_000, null, false)
    expect(window.right).toBe(anchor + 5_000)
    expect(window.left).toBe(window.right - dayMs)
    expect(window.vouchedThrough).toBe(window.right)
  })

  test('takes the left edge from the preset, never from the document', () => {
    for (const preset of ['1d', '14d', '30d', '90d', '180d', '1y'] as Preset[]) {
      const window = detailWindow(document(), preset, 0, null, false)
      expect(window.right - window.left).toBe(presetSpans[preset])
    }
  })

  test('opens all at the fixed bound, whatever the document covers', () => {
    const window = detailWindow(document(), 'all', 0, null, false)
    expect(window.left).toBe(allWindowFrom)
    expect(window.right).toBe(anchor)
  })

  test('vouches to its own right edge while clean and polling is good', () => {
    // the document sat held for days and nothing dirtied it: the status poll is
    // positive testimony that no transition landed, so the tail stands.
    const stale = document({ to: iso(anchor - 3 * dayMs), generated: iso(anchor - 3 * dayMs + 40) })
    const window = detailWindow(stale, '90d', 3 * dayMs, null, false)
    expect(window.vouchedThrough).toBe(window.right)
  })

  test('caps a stale page at the latest good status, not at an old document', () => {
    // an old clean document with a newer status document: the cap is the
    // status's generated, which still vouches the recent window, rather than
    // hatching everything past the document's own to.
    const old = document({ to: iso(anchor - 3 * dayMs), generated: iso(anchor - 3 * dayMs + 40) })
    const window = detailWindow(old, '90d', 3 * dayMs + 60_000, anchor, false)
    expect(window.vouchedThrough).toBe(anchor)
    expect(window.right).toBeGreaterThan(anchor)
  })

  test('takes the earlier of the stale cap and a dirty document', () => {
    const old = document({ to: iso(anchor - 3 * dayMs), generated: iso(anchor - 3 * dayMs + 40) })
    const window = detailWindow(old, '90d', 3 * dayMs + 60_000, anchor, true)
    expect(window.vouchedThrough).toBe(anchor - 3 * dayMs)
  })
})

describe('the source rule', () => {
  const stripWindow: DisplayWindow = {
    left: anchor - displayWindowMs,
    right: anchor,
    vouchedThrough: anchor,
  }

  test('renders the strip document, window, and arrival as one unit at 90d', () => {
    const strip = held({ arrival: anchor - 90_000 })
    const panel = held({ document: document({ to: iso(anchor - dayMs) }), arrival: anchor })
    const source = detailSource({
      preset: '90d',
      strip,
      stripWindow,
      panel,
      now: anchor,
      staleCap: null,
    })
    expect(source?.document).toBe(strip.document)
    expect(source?.window).toBe(stripWindow)
    expect(source?.arrival).toBe(strip.arrival)
  })

  test('renders the panel document and its own arrival at every other preset', () => {
    const strip = held({ arrival: anchor - 90_000 })
    const panelDocument = document({ to: iso(anchor - dayMs) })
    const panel = held({ document: panelDocument, arrival: anchor - 1_000 })
    const source = detailSource({
      preset: '1d',
      strip,
      stripWindow,
      panel,
      now: anchor,
      staleCap: null,
    })
    expect(source?.document).toBe(panelDocument)
    expect(source?.arrival).toBe(anchor - 1_000)
    expect(source?.window.right).toBe(Date.parse(panelDocument.to) + 1_000)
  })

  test('keeps its arrival pairing across quiet status polls', () => {
    // a later status receipt resets nothing the history document's ages count
    // from: the same held document keeps counting forward.
    const strip = held({ arrival: anchor - 600_000 })
    const first = detailSource({ preset: '90d', strip, stripWindow, panel: held(), now: anchor, staleCap: null })
    const later = detailSource({
      preset: '90d',
      strip,
      stripWindow,
      panel: held(),
      now: anchor + 600_000,
      staleCap: null,
    })
    expect(first?.arrival).toBe(anchor - 600_000)
    expect(later?.arrival).toBe(anchor - 600_000)
  })

  test('renders nothing until the document it would draw from exists', () => {
    const empty: HistoryState = { document: null, startedUnder: null, dirty: true, arrival: null }
    expect(detailSource({ preset: '90d', strip: empty, stripWindow: null, panel: empty, now: anchor, staleCap: null })).toBeNull()
    expect(detailSource({ preset: '1d', strip: held(), stripWindow, panel: empty, now: anchor, staleCap: null })).toBeNull()
  })
})

describe('the panel landing rule', () => {
  function response(preset: Preset, to: number): HistoryDocument {
    const span = presetSpans[preset]
    return document({
      to: iso(to),
      from: iso(span === null ? allWindowFrom : to - span),
      generated: iso(to + 40),
    })
  }

  test('lands a response carrying the selected preset shape', () => {
    expect(detailLands(response('1d', anchor), '1d', status())).toBe(true)
    expect(detailLands(response('all', anchor), 'all', status())).toBe(true)
  })

  test('ignores a late response to an abandoned preset', () => {
    // the page switched to 1d while a 30d request was in the air.
    expect(detailLands(response('30d', anchor), '1d', status())).toBe(false)
  })

  test('ignores an all response once a span preset is selected', () => {
    expect(detailLands(response('all', anchor), '90d', status())).toBe(false)
  })

  test('ignores a span response once all is selected', () => {
    expect(detailLands(response('1d', anchor), 'all', status())).toBe(false)
  })

  test('rejects the A to B to A response, whose shape alone would pass', () => {
    // a newer poll landed between the two dispatches, so the abandoned visit's
    // response carries the older bound and cannot clear the flag the fresh
    // fetch's failure left behind.
    const newest = status({ generated: iso(anchor + 10_000) })
    expect(detailLands(response('1d', anchor), '1d', newest)).toBe(false)
  })

  test('lands the same-wire-second A to B to A response, which no wire can tell apart', () => {
    // no poll landed between the dispatches, so the abandoned response covers
    // exactly what a fresh fetch would return.
    expect(detailLands(response('1d', anchor), '1d', status())).toBe(true)
  })

  test('lands a response reaching past the newest status', () => {
    const newest = status({ generated: iso(anchor - 10_000) })
    expect(detailLands(response('1d', anchor), '1d', newest)).toBe(true)
  })
})

describe('the transition list', () => {
  const window: DisplayWindow = { left: anchor - 30 * dayMs, right: anchor, vouchedThrough: anchor }

  function shape(rows: EventRow[]): string[] {
    return rows.map((row) =>
      row.kind === 'daemon'
        ? `daemon ${row.event.event} ${offset(row.ts)}`
        : `${row.event.from}->${row.event.to} ${offset(row.ts)}`,
    )
  }

  function offset(at: number): string {
    return `d${Math.round(((at - anchor) / dayMs) * 100) / 100}`
  }

  test('interleaves the check and the estate, newest first', () => {
    const history = document({
      daemon: [
        { ts: iso(anchor - 12 * dayMs), event: 'stop' },
        { ts: iso(anchor - 11 * dayMs), event: 'start' },
      ],
    })
    const check = entry({
      events: [
        transition(anchor - 20 * dayMs),
        transition(anchor - 5 * dayMs, { from: 'failed', to: 'ok' }),
      ],
    })

    expect(shape(eventRows(history, check, window))).toEqual([
      'failed->ok d-5',
      'daemon start d-11',
      'daemon stop d-12',
      'ok->failed d-20',
    ])
  })

  test('breaks a shared wire timestamp with daemon rows before check rows', () => {
    const at = anchor - 7 * dayMs
    const history = document({ daemon: [{ ts: iso(at), event: 'start' }] })
    const check = entry({ events: [transition(at)] })

    expect(shape(eventRows(history, check, window))).toEqual([
      'daemon start d-7',
      'ok->failed d-7',
    ])
  })

  test('keeps a row sitting exactly on either bound', () => {
    const history = document({ daemon: [{ ts: iso(window.left), event: 'start' }] })
    const check = entry({ events: [transition(window.right)] })

    expect(shape(eventRows(history, check, window))).toEqual([
      'ok->failed d0',
      'daemon start d-30',
    ])
  })

  test('drops a row that has aged past the left edge, with no refetch', () => {
    const check = entry({ events: [transition(anchor - 31 * dayMs)] })
    expect(eventRows(document(), check, window)).toEqual([])
  })

  test('drops a row beyond the right edge', () => {
    const check = entry({ events: [transition(anchor + dayMs)] })
    expect(eventRows(document(), check, window)).toEqual([])
  })

  test('renders nothing when the window has slid past everything held', () => {
    const history = document({ daemon: [{ ts: iso(anchor - 200 * dayMs), event: 'start' }] })
    const check = entry({ events: [transition(anchor - 180 * dayMs)] })
    expect(eventRows(history, check, window)).toEqual([])
  })

  test('renders the estate rows of a window the check never transitioned in', () => {
    // the empty message is the check's own claim, so a daemon-only window still
    // renders its rows; the panel puts the message beneath them.
    const history = document({ daemon: [{ ts: iso(anchor - 3 * dayMs), event: 'start' }] })
    const rows = eventRows(history, entry(), window)
    expect(shape(rows)).toEqual(['daemon start d-3'])
    expect(rows.filter((row) => row.kind === 'transition')).toEqual([])
  })
})

describe("the list's timestamp form", () => {
  const inYear = '2026-01-15T03:10:00Z'
  const outOfYear = '2025-01-15T03:10:00Z'

  test('adds the year only outside the viewer’s current calendar year', () => {
    expect(outsideCurrentYear(outOfYear, anchor)).toBe(true)
    expect(outsideCurrentYear(inYear, anchor)).toBe(false)
  })

  test('renders a same-year event exactly as the row strip does', () => {
    expect(formatTimestamp(inYear, false)).toBe(formatTimestamp(inYear))
    expect(formatTimestamp(inYear, true)).not.toBe(formatTimestamp(inYear))
  })

  test('leaves an unparseable value alone rather than guessing a year', () => {
    expect(outsideCurrentYear('not a date', anchor)).toBe(false)
  })
})

describe("the panel's flag lifecycle and the loop's scheduling", () => {
  const startedAt = iso(anchor - 400 * dayMs)

  function polled(overrides: Partial<StatusDocument> = {}): StatusDocument {
    return status({ generated: iso(anchor + 10_000), ...overrides })
  }

  function transitioned(at: number): StatusDocument {
    return polled({
      checks: [
        {
          id: 'web',
          name: 'web',
          kind: 'http',
          state: 'failed',
          since: iso(at),
          last_transition: iso(at),
          last_seen: null,
          detail: 'down',
        },
      ],
    } as Partial<StatusDocument>)
  }

  test('evaluates both levels against one document and settles both flags', () => {
    const plan = pollPlan({
      strip: held(),
      panel: held(),
      status: transitioned(anchor + 5_000),
      panelOpen: true,
      preset: '1d',
    })
    // a poll revealing a transition never leaves one surface clean behind the
    // other's in-flight request: both flags are dirty and both fetches go out.
    expect(plan).toEqual({ stripDirty: true, panelDirty: true, fetchStrip: true, fetchPanel: true })
  })

  test('dirties a closed panel, and fetches nothing for it', () => {
    const plan = pollPlan({
      strip: held(),
      panel: held(),
      status: transitioned(anchor + 5_000),
      panelOpen: false,
      preset: '1d',
    })
    expect(plan.panelDirty).toBe(true)
    expect(plan.fetchPanel).toBe(false)
  })

  test('dirties a closed panel on a restart no transition would reveal', () => {
    const plan = pollPlan({
      strip: held(),
      panel: held(),
      status: polled({ started: iso(anchor - dayMs) }),
      panelOpen: false,
      preset: '1d',
    })
    expect(plan.panelDirty).toBe(true)
    expect(plan.fetchPanel).toBe(false)
  })

  test('fetches on reopen for the level maintained through closure', () => {
    // open, close, a transition lands, reopen: the flag the closed panel kept
    // is what the reopen's fetch runs from.
    const document = transitioned(anchor + 5_000)
    const closed = pollPlan({ strip: held(), panel: held(), status: document, panelOpen: false, preset: '1d' })
    const carried = held({ dirty: closed.panelDirty })
    const reopened = pollPlan({ strip: held(), panel: carried, status: document, panelOpen: true, preset: '1d' })
    expect(reopened.fetchPanel).toBe(true)
  })

  test('keeps the level through closure while polling is stale', () => {
    // the stale cap changes what the surfaces may vouch for, never whether the
    // held document is behind.
    const document = transitioned(anchor + 5_000)
    const closed = pollPlan({ strip: held(), panel: held(), status: document, panelOpen: false, preset: 'all' })
    expect(closed.panelDirty).toBe(true)
    const carried = held({ dirty: closed.panelDirty })
    expect(pollPlan({ strip: held(), panel: carried, status: document, panelOpen: true, preset: 'all' }).fetchPanel).toBe(true)
  })

  test('fetches no second document at the default preset, open or not', () => {
    const document = transitioned(anchor + 5_000)
    expect(pollPlan({ strip: held(), panel: held(), status: document, panelOpen: true, preset: defaultPreset }).fetchPanel).toBe(false)
    expect(panelFetches(true, defaultPreset)).toBe(false)
    expect(panelFetches(false, '1d')).toBe(false)
    expect(panelFetches(true, '1d')).toBe(true)
  })

  test('leaves a quiet estate refetching never, on either surface', () => {
    const quiet = status({ started: startedAt })
    const plan = pollPlan({ strip: held(), panel: held(), status: quiet, panelOpen: true, preset: '1d' })
    expect(plan).toEqual({ stripDirty: false, panelDirty: false, fetchStrip: false, fetchPanel: false })
  })
})

describe('the shared vouching cap', () => {
  test('is absent while polling is good, so each surface vouches to its own edge', () => {
    expect(vouchingCap(status(), false)).toBeNull()
  })

  test('is the latest good status stamp while polling is failing', () => {
    expect(vouchingCap(status(), true)).toBe(anchor)
  })

  test('is absent before any status has landed, however stale the page says it is', () => {
    expect(vouchingCap(null, true)).toBeNull()
  })

  test('caps both surfaces at the same instant, from the one selection', () => {
    // the coupling the cap exists for: one value, two windows.
    const cap = vouchingCap(status(), true)
    const old = document({ to: iso(anchor - 3 * dayMs), generated: iso(anchor - 3 * dayMs + 40) })
    expect(detailWindow(old, '90d', 3 * dayMs + 60_000, cap, false).vouchedThrough).toBe(anchor)
  })
})

describe('the empty message predicate', () => {
  const window: DisplayWindow = { left: anchor - 30 * dayMs, right: anchor, vouchedThrough: anchor }

  test('counts the check own transitions and nothing else', () => {
    const history = document({ daemon: [{ ts: iso(anchor - 3 * dayMs), event: 'start' }] })
    // a daemon-only window: the estate spoke, the check did not.
    expect(checkTransitions(eventRows(history, entry(), window))).toBe(0)
  })

  test('counts a check transition sharing its instant with a daemon row', () => {
    const at = anchor - 7 * dayMs
    const history = document({ daemon: [{ ts: iso(at), event: 'start' }] })
    const check = entry({ events: [transition(at)] })
    expect(checkTransitions(eventRows(history, check, window))).toBe(1)
  })

  test('counts nothing for an empty window', () => {
    expect(checkTransitions([])).toBe(0)
  })
})

describe('the request bounds and the landing rule as one', () => {
  const dispatchedUnder = iso(anchor)

  test('opens a span preset exactly its span back from the coverage end', () => {
    const bounds = requestBounds('30d', dispatchedUnder)
    expect(bounds.to).toBe(dispatchedUnder)
    expect(Date.parse(bounds.to) - Date.parse(bounds.from)).toBe(presetSpans['30d'])
  })

  test('opens all at the fixed bound, ignoring any span', () => {
    expect(Date.parse(requestBounds('all', dispatchedUnder).from)).toBe(allWindowFrom)
  })

  test('sends whole-second bounds, which the wire round-trips exactly', () => {
    // the contract truncates to whole seconds; bounds derived from a status
    // stamp are already whole, and the spans are whole-day constants, so the
    // echoed pair parses back to what was sent and the shape check is exact.
    for (const preset of presets) {
      const bounds = requestBounds(preset, dispatchedUnder)
      expect(Date.parse(bounds.from) % 1000).toBe(0)
      expect(Date.parse(bounds.to) % 1000).toBe(0)
    }
  })

  test('produces bounds the landing rule accepts, for every preset', () => {
    // the two halves of one rule: a response echoing what requestBounds asked
    // for must be one detailLands takes. a drift between the tables would
    // reject every panel response forever, and nothing else would say so.
    for (const preset of presets) {
      const bounds = requestBounds(preset, dispatchedUnder)
      const echoed = document({ from: bounds.from, to: bounds.to, generated: iso(anchor + 40) })
      expect(detailLands(echoed, preset, status())).toBe(true)
    }
  })

  test('produces bounds no other preset will accept', () => {
    for (const preset of presets) {
      const bounds = requestBounds(preset, dispatchedUnder)
      const echoed = document({ from: bounds.from, to: bounds.to, generated: iso(anchor + 40) })
      for (const other of presets.filter((candidate) => candidate !== preset)) {
        expect(detailLands(echoed, other, status())).toBe(false)
      }
    }
  })
})
