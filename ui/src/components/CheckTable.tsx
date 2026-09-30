import type { Check, CheckState, HistoryDocument } from '../api/client'
import { buildStrip, defaultPreset, uptimeOf, type DisplayWindow, type UptimeMs } from '../history'
import { checkHref } from '../route'
import { elapsedSince, formatDuration, formatUptime } from '../util'
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

// the hover says what the percentage is of, which the column has no room for.
function uptimeTitle(uptime: UptimeMs): string {
  if (!(uptime.watched > 0)) {
    return 'nothing watched in this window'
  }
  return `ok for ${formatDuration(uptime.ok)} of ${formatDuration(uptime.watched)} watched`
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
          // the strip's intervals, drawn once and summed once: the figure
          // beside the strip is the strip's own bands counted, so the two
          // cannot disagree.
          const intervals =
            history && window && recorded ? buildStrip(history, recorded, window) : null
          const uptime = intervals ? uptimeOf(intervals) : null
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
                {intervals && uptime && window ? (
                  <RouteLink
                    to={checkHref(check.id)}
                    className="strip-link"
                    label={`${check.name} history detail`}
                  >
                    <StateStrip intervals={intervals} window={window} label={check.name} />
                    <span className="strip-uptime" title={uptimeTitle(uptime)}>
                      {formatUptime(uptime)}
                    </span>
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
