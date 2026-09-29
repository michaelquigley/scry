import type { Check } from '../api/client'
import type { DetailSource, Preset } from '../history'
import { estateHref } from '../route'
import { elapsedSince, formatDuration, formatTimestampWithAge } from '../util'
import { DetailPanel } from './DetailPanel'
import { RouteLink } from './RouteLink'

// CheckPage is one check's detail: what the estate's row says about it now,
// and beneath that the recorded history the row's strip is a glimpse of. the id
// comes from the address bar, so it may name nothing the daemon has configured;
// the status document is what says so.
export function CheckPage({
  id,
  check,
  generated,
  ageOffset,
  source,
  preset,
  onPreset,
  now,
}: {
  id: string
  check: Check | undefined
  generated: string
  // locally elapsed ms since the status document arrived.
  ageOffset: number
  // what the history draws from: document, window, and arrival as one unit.
  // null while the page has nothing to render it from yet.
  source: DetailSource | null
  preset: Preset
  onPreset: (preset: Preset) => void
  now: number
}) {
  const entry = source?.document.checks.find((recorded) => recorded.id === id)
  return (
    <section className="check-page">
      <RouteLink to={estateHref} className="check-back">
        <span aria-hidden="true">&larr;</span> all checks
      </RouteLink>

      {check ? (
        <>
          <div className="check-head">
            <span className={`chip chip-${check.state}`}>{check.state}</span>
            <div>
              <h2 className="check-title">{check.name}</h2>
              <div className="check-id">
                {check.id} · {check.kind}
              </div>
            </div>
          </div>

          <dl className="check-facts">
            <div>
              <dt>in state</dt>
              <dd>{formatDuration(elapsedSince(check.since, generated) + ageOffset)}</dd>
            </div>
            <div>
              <dt>last transition</dt>
              <dd>
                {check.last_transition
                  ? formatTimestampWithAge(check.last_transition, generated, ageOffset)
                  : '—'}
              </dd>
            </div>
            <div className="check-facts-detail">
              <dt>detail</dt>
              <dd>{check.detail || '—'}</dd>
            </div>
          </dl>

          {source && entry ? (
            <DetailPanel
              source={source}
              entry={entry}
              label={check.name}
              preset={preset}
              onPreset={onPreset}
              now={now}
            />
          ) : (
            <p className="placeholder">loading history</p>
          )}
        </>
      ) : (
        <p className="placeholder">no check '{id}'</p>
      )}
    </section>
  )
}
