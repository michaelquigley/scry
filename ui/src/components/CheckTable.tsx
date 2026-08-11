import { Fragment } from 'react'
import type { Check, CheckState, HistoryDocument } from '../api/client'
import { buildStrip, type DisplayWindow } from '../history'
import { elapsedSince, formatDuration, formatTimestampWithAge } from '../util'
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
}: {
  checks: Check[]
  generated: string
  // locally elapsed ms since this document arrived; added to every
  // daemon-computed span so ages tick live between polls.
  ageOffset: number
  history: HistoryDocument | null
  window: DisplayWindow | null
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
                  <td colSpan={5}>
                    <StateStrip
                      intervals={buildStrip(history, recorded, window)}
                      window={window}
                      label={check.name}
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
