import { Fragment } from 'react'
import type { Check, CheckState, HistoryDocument } from '../api/client'
import { buildStrip, type DetailSource, type DisplayWindow, type Preset } from '../history'
import { elapsedSince, formatDuration, formatTimestampWithAge } from '../util'
import { DetailPanel } from './DetailPanel'
import { StateStrip } from './StateStrip'

// the API returns registry order; sorting is the render's decision. trouble
// first, then by id within each state group, so the page answers itself
// without a scroll and holds a stable, scannable order between polls.
const stateOrder: Record<CheckState, number> = { failed: 0, late: 1, ok: 2 }

function troubleFirst(checks: Check[]): Check[] {
  return [...checks].sort((left, right) => {
    const byState = stateOrder[left.state] - stateOrder[right.state]
    if (byState !== 0) {
      return byState
    }
    return left.id.localeCompare(right.id)
  })
}

export function CheckTable({
  checks,
  generated,
  ageOffset,
  history,
  window,
  openCheck,
  onToggle,
  source,
  preset,
  onPreset,
  now,
}: {
  checks: Check[]
  generated: string
  // locally elapsed ms since this document arrived; added to every
  // daemon-computed span so ages tick live between polls.
  ageOffset: number
  history: HistoryDocument | null
  window: DisplayWindow | null
  openCheck: string | null
  onToggle: (id: string) => void
  // what the open panel draws from: document, window, and arrival as one unit.
  // null while the page has nothing to render it from yet.
  source: DetailSource | null
  preset: Preset
  onPreset: (preset: Preset) => void
  now: number
}) {
  if (checks.length === 0) {
    return null
  }
  return (
    <table className="checks">
      <thead>
        <tr>
          <th scope="col">state</th>
          <th scope="col">check</th>
          <th scope="col">in state</th>
          <th scope="col">last transition</th>
          <th scope="col">detail</th>
        </tr>
      </thead>
      <tbody>
        {troubleFirst(checks).map((check) => {
          const recorded = history?.checks.find((entry) => entry.id === check.id)
          const open = openCheck === check.id
          // the panel is per-check but its document is per-estate, so the entry
          // it draws comes from whichever document the source rule chose.
          const panelEntry = open
            ? source?.document.checks.find((entry) => entry.id === check.id)
            : undefined
          return (
            <Fragment key={check.id}>
              <tr>
                <td>
                  <span className={`chip chip-${check.state}`}>{check.state}</span>
                </td>
                <td>
                  <div className="check-name">{check.name}</div>
                  <div className="check-id">
                    {check.id} · {check.kind}
                  </div>
                </td>
                <td className="numeric">
                  {formatDuration(elapsedSince(check.since, generated) + ageOffset)}
                </td>
                <td className="numeric">
                  {check.last_transition
                    ? formatTimestampWithAge(check.last_transition, generated, ageOffset)
                    : '—'}
                </td>
                <td className="detail">{check.detail || '—'}</td>
              </tr>
              {history && window && recorded ? (
                <tr className="check-strip-row">
                  {/* the empty cell under state carries the same padding as
                      every sibling, so the strip's left edge lands on the check
                      column's without a rule or an offset. */}
                  <td />
                  <td colSpan={4}>
                    <button
                      type="button"
                      className="strip-toggle"
                      aria-expanded={open}
                      onClick={() => onToggle(check.id)}
                    >
                      <StateStrip
                        intervals={buildStrip(history, recorded, window)}
                        window={window}
                        label={check.name}
                      />
                    </button>
                  </td>
                </tr>
              ) : null}
              {open && source && panelEntry ? (
                <tr className="check-detail-row">
                  <td />
                  <td colSpan={4}>
                    <DetailPanel
                      source={source}
                      entry={panelEntry}
                      label={check.name}
                      preset={preset}
                      onPreset={onPreset}
                      now={now}
                    />
                  </td>
                </tr>
              ) : null}
            </Fragment>
          )
        })}
      </tbody>
    </table>
  )
}
