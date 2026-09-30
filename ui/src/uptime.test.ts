import { describe, expect, it } from 'vitest'
import fixture from '../../internal/api/specs/fixtures/history-uptime.json?raw'
import type { HistoryDocument } from './api/client'
import { buildStrip, uptimeOf, type Interval } from './history'

// the fixture the daemon's suite reads too: one history document whose uptime
// figures were worked by hand. the page's walk over the document's own window
// must reach the same figures from the document's other fields, which is what
// pins the two walks — Go and TypeScript — to each other across the contract.
function loadFixture(): HistoryDocument {
  return JSON.parse(fixture) as HistoryDocument
}

describe('uptimeOf', () => {
  it('reaches the shared fixture figures over the document window', () => {
    const document = loadFixture()
    expect(document.checks).toHaveLength(5)
    const from = Date.parse(document.from)
    const to = Date.parse(document.to)
    for (const entry of document.checks) {
      const counted = uptimeOf(buildStrip(document, entry, { left: from, right: to, vouchedThrough: to }))
      expect(
        {
          watched: counted.watched / 1000,
          ok: counted.ok / 1000,
          late: counted.late / 1000,
          failed: counted.failed / 1000,
        },
        entry.id,
      ).toEqual(entry.uptime)
    }
  })

  it('counts only state bands, so watched is the sum of the three states', () => {
    const intervals: Interval[] = [
      { kind: 'blank', start: 0, end: 1000 },
      { kind: 'state', state: 'ok', start: 1000, end: 4000 },
      { kind: 'unwatched', start: 4000, end: 5000 },
      { kind: 'state', state: 'failed', start: 5000, end: 5500 },
      { kind: 'state', state: 'late', start: 5500, end: 6000 },
    ]
    expect(uptimeOf(intervals)).toEqual({ watched: 4000, ok: 3000, late: 500, failed: 500 })
  })

  it('counts nothing over an empty strip', () => {
    expect(uptimeOf([])).toEqual({ watched: 0, ok: 0, late: 0, failed: 0 })
  })
})
