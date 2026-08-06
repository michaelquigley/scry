import type { Check, CheckState } from '../api/client'
import { elapsedSince, formatDuration, formatTimestamp } from '../util'

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

export function CheckTable({ checks, generated }: { checks: Check[]; generated: string }) {
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
        {troubleFirst(checks).map((check) => (
          <tr key={check.id}>
            <td>
              <span className={`chip chip-${check.state}`}>{check.state}</span>
            </td>
            <td>
              <div className="check-name">{check.name}</div>
              <div className="check-id">
                {check.id} · {check.kind}
              </div>
            </td>
            <td className="numeric">{formatDuration(elapsedSince(check.since, generated))}</td>
            <td className="numeric">
              {check.last_transition ? formatTimestamp(check.last_transition) : '—'}
            </td>
            <td className="detail">{check.detail || '—'}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
