import { describe, expect, test } from 'vitest'
import type { CheckHistory, HistoryDocument, StatusDocument } from './api/client'
import {
  buildStrip,
  displayWindow,
  displayWindowMs,
  markerThresholdPercent,
  nextHistoryDirty,
  placeIntervals,
  supersedes,
  type DisplayWindow,
  type HistoryState,
  type Interval,
} from './history'

const dayMs = 24 * 60 * 60 * 1000
const left = Date.parse('2026-05-13T00:00:00Z')
const right = left + displayWindowMs

// days are offsets into the display window, matching the acceptance table.
function day(offset: number): number {
  return left + offset * dayMs
}

function iso(at: number): string {
  return new Date(at).toISOString()
}

function fullWindow(vouchedThrough = right): DisplayWindow {
  return { left, right, vouchedThrough }
}

function entry(overrides: Partial<CheckHistory> = {}): CheckHistory {
  return {
    id: 'web',
    kind: 'http',
    state_at_from: null,
    state_at_to: null,
    since: null,
    events: [],
    ...overrides,
  }
}

function document(overrides: Partial<HistoryDocument> = {}): HistoryDocument {
  return {
    estate: 'test estate',
    generated: iso(right),
    from: iso(left),
    to: iso(right),
    watching_at_from: true,
    checks: [],
    daemon: [],
    ...overrides,
  }
}

// bands renders the result the way the acceptance table reads, so a failure
// prints the shape a person can compare.
function bands(intervals: Interval[]): string[] {
  return intervals.map((interval) => {
    const name = interval.kind === 'state' ? interval.state : interval.kind
    return `${name} ${offset(interval.start)}->${offset(interval.end)}`
  })
}

function offset(at: number): string {
  if (at === left) {
    return 'L'
  }
  if (at === right) {
    return 'R'
  }
  return `d${Math.round(((at - left) / dayMs) * 100) / 100}`
}

describe('the interval builder', () => {
  test('renders pre-ledger time as unwatched rather than backfilling it', () => {
    const history = document({
      watching_at_from: false,
      daemon: [{ ts: iso(day(30)), event: 'start' }],
    })
    const check = entry({
      state_at_from: 'ok',
      state_at_to: 'ok',
      since: iso(day(-200)),
    })

    expect(bands(buildStrip(history, check, fullWindow()))).toEqual([
      'unwatched L->d30',
      'ok d30->R',
    ])
  })

  test('leaves a check blank before it was registered', () => {
    const check = entry({ state_at_to: 'ok', since: iso(day(40)) })

    expect(bands(buildStrip(document(), check, fullWindow()))).toEqual([
      'blank L->d40',
      'ok d40->R',
    ])
  })

  test('overlays an outage on the state claims it interrupts', () => {
    const history = document({
      daemon: [
        { ts: iso(day(50)), event: 'stop' },
        { ts: iso(day(52)), event: 'start' },
      ],
    })
    const check = entry({
      state_at_from: 'ok',
      state_at_to: 'ok',
      since: iso(day(55)),
      events: [
        {
          ts: iso(day(49)),
          kind: 'http',
          from: 'ok',
          to: 'failed',
          prev_since: iso(day(10)),
          detail: 'connection refused',
        },
        {
          ts: iso(day(55)),
          kind: 'http',
          from: 'failed',
          to: 'ok',
          prev_since: iso(day(49)),
          detail: '200 in 84ms',
        },
      ],
    })

    expect(bands(buildStrip(history, check, fullWindow()))).toEqual([
      'ok L->d49',
      'failed d49->d50',
      'unwatched d50->d52',
      'failed d52->d55',
      'ok d55->R',
    ])
  })

  test('ages an old incident off the left edge as the window slides', () => {
    const check = entry({
      state_at_from: 'failed',
      state_at_to: 'ok',
      since: iso(day(1)),
      events: [
        {
          ts: iso(day(1)),
          kind: 'http',
          from: 'failed',
          to: 'ok',
          prev_since: iso(day(-5)),
          detail: null,
        },
      ],
    })

    expect(bands(buildStrip(document(), check, fullWindow()))).toEqual([
      'failed L->d1',
      'ok d1->R',
    ])

    // two days later the failed band has left the window entirely.
    const slid: DisplayWindow = {
      left: day(2),
      right: right + 2 * dayMs,
      vouchedThrough: right + 2 * dayMs,
    }
    const later = buildStrip(document(), check, slid)
    expect(later.every((interval) => interval.kind === 'state' && interval.state === 'ok')).toBe(true)
    expect(later[0].start).toBe(slid.left)
    expect(later[later.length - 1].end).toBe(slid.right)
  })

  test('renders what a stale page cannot vouch for as unwatched', () => {
    const check = entry({ state_at_from: 'ok', state_at_to: 'ok', since: iso(day(-10)) })
    const window = fullWindow(day(89))

    expect(bands(buildStrip(document(), check, window))).toEqual(['ok L->d89', 'unwatched d89->R'])
  })

  test('caps at a dirty document rather than claiming its stale tail', () => {
    const history = document({ generated: iso(day(88)) })
    const check = entry({ state_at_from: 'ok', state_at_to: 'ok', since: iso(day(-10)) })
    const window = displayWindow({
      status: { generated: iso(right), started: iso(day(0)), checks: [] } as unknown as StatusDocument,
      ageOffset: 0,
      stale: false,
      history,
      historyDirty: true,
    })

    expect(bands(buildStrip(history, check, window))).toEqual(['ok L->d88', 'unwatched d88->R'])
  })

  test('claims nothing before a fresh ledger opened, even where a record exists', () => {
    const history = document({
      watching_at_from: false,
      daemon: [{ ts: iso(day(42)), event: 'start' }],
    })
    const check = entry({ state_at_to: 'ok', since: iso(day(40)) })

    // unwatched wins over the blank span and over the record's own band: where
    // the daemon cannot testify the strip claims neither.
    expect(bands(buildStrip(history, check, fullWindow()))).toEqual([
      'unwatched L->d42',
      'ok d42->R',
    ])
  })

  test('claims nothing before the document it is drawn from begins', () => {
    // the status and history documents are fetched in sequence, so the
    // status-derived left edge opens before the history window does until the
    // next pulse closes the difference.
    const history = document({ from: iso(left + 20), generated: iso(right + 20) })
    const check = entry({ state_at_from: 'ok', state_at_to: 'ok', since: iso(day(-10)) })

    const intervals = buildStrip(history, check, fullWindow())
    expect(intervals[0].kind).toBe('unwatched')
    expect(intervals[0].start).toBe(left)
    expect(intervals[0].end).toBe(left + 20)
    expect(intervals[1]).toMatchObject({ kind: 'state', state: 'ok', start: left + 20, end: right })
  })

  test('ends a null-opened blank span at the first event boundary', () => {
    const check = entry({
      state_at_to: 'failed',
      since: iso(day(60)),
      events: [
        {
          ts: iso(day(60)),
          kind: 'http',
          from: 'ok',
          to: 'failed',
          prev_since: iso(day(45)),
          detail: null,
        },
      ],
    })

    expect(bands(buildStrip(document(), check, fullWindow()))).toEqual([
      'blank L->d45',
      'ok d45->d60',
      'failed d60->R',
    ])
  })
})

describe('strip placement', () => {
  const window = fullWindow()
  const minute = 60 * 1000

  function place(kind: Interval['kind'], start: number, end: number, state?: Interval['state']) {
    return placeIntervals([{ kind, state, start, end } as Interval], window)[0]
  }

  test('places every endpoint exactly proportionally', () => {
    const placements = placeIntervals(
      [
        { kind: 'state', state: 'ok', start: left, end: day(45) },
        { kind: 'state', state: 'failed', start: day(45), end: day(90) },
      ],
      window,
    )
    expect(placements[0].left).toBe(0)
    expect(placements[0].width).toBe(50)
    expect(placements[1].left).toBe(50)
    expect(placements[1].width).toBe(50)
    expect(placements[1].center).toBe(75)
  })

  test('never widens a band to make it visible', () => {
    // thirty minutes of a ninety-day window is 0.023%, and it stays 0.023%.
    const placement = place('state', day(60), day(60) + 30 * minute, 'failed')
    expect(placement.width).toBeCloseTo((30 * minute) / displayWindowMs * 100, 10)
    expect(placement.width).toBeLessThan(markerThresholdPercent)
  })

  test('marks trouble and absence a reader would otherwise miss', () => {
    expect(place('state', day(60), day(60) + 30 * minute, 'failed').marked).toBe(true)
    expect(place('state', day(60), day(60) + 30 * minute, 'late').marked).toBe(true)
    expect(place('unwatched', day(60), day(60) + 30 * minute).marked).toBe(true)
  })

  test('leaves health and nonexistence unmarked, however short', () => {
    expect(place('state', day(60), day(60) + 30 * minute, 'ok').marked).toBe(false)
    expect(place('blank', day(60), day(60) + 30 * minute).marked).toBe(false)
  })

  test('marks nothing that is already wide enough to see', () => {
    const placement = place('state', day(10), day(20), 'failed')
    expect(placement.marked).toBe(false)
    expect(placement.width).toBeGreaterThan(markerThresholdPercent)
  })

  test('places a marker at the interval it stands for, not beside it', () => {
    const placement = place('state', day(60), day(60) + 30 * minute, 'failed')
    expect(placement.center).toBeGreaterThan(placement.left)
    expect(placement.center).toBeLessThan(placement.left + placement.width)
  })

  test('draws nothing at all when the window has no span', () => {
    expect(placeIntervals([{ kind: 'blank', start: left, end: right }], { left, right: left, vouchedThrough: left })).toEqual([])
  })
})

describe('the display window', () => {
  const status = { generated: iso(day(80)), started: iso(day(0)), checks: [] } as unknown as StatusDocument

  test('slides with the page pulse', () => {
    const window = displayWindow({ status, ageOffset: 5_000, stale: false, history: null, historyDirty: false })
    expect(window.right).toBe(day(80) + 5_000)
    expect(window.left).toBe(window.right - displayWindowMs)
    expect(window.vouchedThrough).toBe(window.right)
  })

  test('caps at the earlier of a stale status and a dirty history', () => {
    const history = document({ generated: iso(day(70)) })
    const window = displayWindow({ status, ageOffset: 60_000, stale: true, history, historyDirty: true })
    expect(window.vouchedThrough).toBe(day(70))
  })
})

describe('out-of-order responses', () => {
  const held = document({ generated: iso(day(80)) })

  test('the first document to arrive is always taken', () => {
    expect(supersedes(null, held)).toBe(true)
  })

  test('a newer document replaces the one held', () => {
    expect(supersedes(held, document({ generated: iso(day(81)) }))).toBe(true)
  })

  test('a response that lost the race is ignored', () => {
    expect(supersedes(held, document({ generated: iso(day(79)) }))).toBe(false)
  })

  test('an equal watermark is not stale, so a quiet page cannot stick dirty', () => {
    expect(supersedes(held, document({ generated: iso(day(80)) }))).toBe(true)
  })
})

describe('the dirty flag', () => {
  const started = iso(day(0))
  const held: HistoryState = {
    document: document({ generated: iso(day(80)) }),
    startedUnder: started,
    dirty: false,
  }

  function status(overrides: Partial<StatusDocument> = {}): StatusDocument {
    return {
      estate: 'test estate',
      generated: iso(day(80)),
      started,
      rollup: { ok: 1, late: 0, failed: 0 },
      checks: [],
      ...overrides,
    } as StatusDocument
  }

  test('is set at first load, when no document exists yet', () => {
    expect(nextHistoryDirty({ document: null, startedUnder: null, dirty: false }, status(), 'none')).toBe(true)
  })

  test('is set by a transition newer than the held document', () => {
    const polled = status({
      checks: [
        {
          id: 'web',
          name: 'web',
          kind: 'http',
          state: 'failed',
          since: iso(day(81)),
          last_transition: iso(day(81)),
          last_seen: null,
          detail: 'down',
        },
      ],
    })
    expect(nextHistoryDirty(held, polled, 'none')).toBe(true)
  })

  test('ignores a transition the held document already covers', () => {
    const polled = status({
      checks: [
        {
          id: 'web',
          name: 'web',
          kind: 'http',
          state: 'failed',
          since: iso(day(79)),
          last_transition: iso(day(79)),
          last_seen: null,
          detail: 'down',
        },
      ],
    })
    expect(nextHistoryDirty(held, polled, 'none')).toBe(false)
  })

  test('is set by a registration, which carries no transition to notice it by', () => {
    const polled = status({
      checks: [
        {
          id: 'new',
          name: 'new',
          kind: 'http',
          state: 'ok',
          since: iso(day(81)),
          last_transition: null,
          last_seen: null,
          detail: null,
        },
      ],
    })
    expect(nextHistoryDirty(held, polled, 'none')).toBe(true)
  })

  test('is set by a restart no transition would reveal', () => {
    expect(nextHistoryDirty(held, status({ started: iso(day(79)) }), 'none')).toBe(true)
  })

  test('is set by a failed fetch and stays set across quiet polls', () => {
    const failed = nextHistoryDirty(held, status(), 'failed')
    expect(failed).toBe(true)
    expect(nextHistoryDirty({ ...held, dirty: failed }, status(), 'none')).toBe(true)
  })

  test('clears only on a fetch that lands', () => {
    expect(nextHistoryDirty({ ...held, dirty: true }, status(), 'succeeded')).toBe(false)
  })
})
