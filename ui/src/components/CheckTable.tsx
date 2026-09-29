import type { Check, CheckState, HistoryDocument } from '../api/client'
import { buildStrip, defaultPreset, type DisplayWindow } from '../history'
import { checkHref } from '../route'
import { elapsedSince, formatDuration } from '../util'
import { RouteLink } from './RouteLink'
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
          {/* the row strip's window is the default preset's, so the column is
              named in the vocabulary the check's page offers its windows in. */}
          <th scope="col">{defaultPreset} history</th>
          <th scope="col">in state</th>
          <th scope="col">detail</th>
        </tr>
      </thead>
      <tbody>
        {troubleFirst(checks).map((check) => {
          const recorded = history?.checks.find((entry) => entry.id === check.id)
          return (
            <tr key={check.id}>
              <td>
                <span className={`chip chip-${check.state}`}>{check.state}</span>
              </td>
              <td>
                <RouteLink to={checkHref(check.id)} className="check-name">
                  {check.name}
                </RouteLink>
                <div className="check-id">
                  {check.id} · {check.kind}
                </div>
              </td>
              <td className="history">
                {/* the strip is the way in to the check's detail, and the name
                    beside it is the same link in words. */}
                {history && window && recorded ? (
                  <RouteLink
                    to={checkHref(check.id)}
                    className="strip-link"
                    label={`${check.name} history detail`}
                  >
                    <StateStrip
                      intervals={buildStrip(history, recorded, window)}
                      window={window}
                      label={check.name}
                    />
                  </RouteLink>
                ) : null}
              </td>
              <td className="numeric">
                {formatDuration(elapsedSince(check.since, generated) + ageOffset)}
              </td>
              {/* the table carries one line of the daemon's detail and says so
                  with an ellipsis; the whole string is on hover and on the
                  check's page, which the line links to. */}
              <td className="detail" title={check.detail || undefined}>
                {check.detail ? (
                  <RouteLink to={checkHref(check.id)} className="detail-link">
                    {check.detail}
                  </RouteLink>
                ) : (
                  '—'
                )}
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
