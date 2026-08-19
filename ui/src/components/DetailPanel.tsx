import type { CheckHistory } from '../api/client'
import {
  buildStrip,
  checkTransitions,
  eventRows,
  presets,
  type DetailSource,
  type EventRow,
  type Preset,
} from '../history'
import { elapsedSince, formatDuration, formatTimestampWithAge, outsideCurrentYear } from '../util'
import { StateStrip } from './StateStrip'

// DetailPanel is a dumb drawer of what the pure functions return: the strip
// over the chosen window, the presets that choose it, and the list scoped to
// the same span. no arithmetic with a branch in it lives here.
export function DetailPanel({
  source,
  entry,
  label,
  preset,
  onPreset,
  now,
}: {
  source: DetailSource
  entry: CheckHistory
  label: string
  preset: Preset
  onPreset: (preset: Preset) => void
  // the page's live pulse, in the browser's clock frame. it decides how long
  // this document has been held and whether a row needs its year.
  now: number
}) {
  const rows = eventRows(source.document, entry, source.window)
  // the age every row counts from is this document's own: the daemon's span to
  // the coverage it served, plus how long the page has held it.
  const held = Math.max(0, now - source.arrival)
  // the message is pinned to the check's own rows. it claims nothing about the
  // daemon, only that the check did not transition, so a window holding only
  // estate rows renders them and the message beneath.
  const transitions = checkTransitions(rows)

  const when = (ts: string) =>
    formatTimestampWithAge(ts, source.document.to, held, outsideCurrentYear(ts, now))

  return (
    <div className="detail-panel">
      <div className="detail-strip">
        <StateStrip
          intervals={buildStrip(source.document, entry, source.window)}
          window={source.window}
          label={`${label} ${preset}`}
        />
      </div>

      <div className="detail-presets" role="group" aria-label="window">
        {presets.map((option) => (
          <button
            key={option}
            type="button"
            className="detail-preset"
            aria-pressed={option === preset}
            onClick={() => onPreset(option)}
          >
            {option}
          </button>
        ))}
      </div>

      <ol className="detail-events">
        {rows.map((row, index) => (
          <li key={rowKey(row, index)} className={`detail-event detail-event-${row.kind}`}>
            <span className="detail-when">{when(row.event.ts)}</span>
            {row.kind === 'daemon' ? (
              <span className="detail-daemon">
                daemon {row.event.event === 'start' ? 'started' : 'stopped'}
              </span>
            ) : (
              <>
                <span className="detail-change">
                  <span className={`chip chip-${row.event.from}`}>{row.event.from}</span>
                  <span className="detail-arrow" aria-hidden="true">
                    &rarr;
                  </span>
                  <span className={`chip chip-${row.event.to}`}>{row.event.to}</span>
                </span>
                <span className="detail-kind">{row.event.kind}</span>
                {/* the duration the check held the state it left, straight off
                    the daemon's own pair. */}
                <span className="detail-for">
                  {formatDuration(elapsedSince(row.event.prev_since, row.event.ts))}
                </span>
                <span className="detail-detail">{row.event.detail || '—'}</span>
              </>
            )}
          </li>
        ))}
      </ol>

      {transitions === 0 ? (
        <p className="detail-empty">no transitions in this window</p>
      ) : null}
    </div>
  )
}

// the ledger can record two events inside one wire second, so the index is what
// keeps the key unique; eventRows already fixed the order it counts against.
function rowKey(row: EventRow, index: number): string {
  return `${row.kind}-${row.ts}-${index}`
}
