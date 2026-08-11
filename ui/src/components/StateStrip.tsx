import { placeIntervals, type DisplayWindow, type Interval, type Placement } from '../history'
import { formatDuration, formatTimestamp } from '../util'

function bandClass(interval: Interval): string {
  if (interval.kind === 'state') {
    return `strip-band strip-${interval.state}`
  }
  return `strip-band strip-${interval.kind}`
}

function markerClass(interval: Interval): string {
  const name = interval.kind === 'state' ? interval.state : interval.kind
  return `strip-marker strip-marker-${name}`
}

function describe(interval: Interval): string {
  const span = formatDuration(interval.end - interval.start)
  const at = formatTimestamp(new Date(interval.start).toISOString())
  if (interval.kind === 'state') {
    return `${interval.state} · ${span} · from ${at}`
  }
  if (interval.kind === 'unwatched') {
    return `unwatched · ${span} · from ${at}`
  }
  return `not yet registered · ${span} · from ${at}`
}

function key(placement: Placement, prefix: string): string {
  const interval = placement.interval
  return `${prefix}-${interval.kind}-${interval.state ?? ''}-${interval.start}`
}

// StateStrip draws the placed geometry and nothing else; where a band sits and
// whether it needs a marker are decided by placeIntervals, where they are
// tested.
export function StateStrip({
  intervals,
  window,
  label,
}: {
  intervals: Interval[]
  window: DisplayWindow
  label: string
}) {
  const placements = placeIntervals(intervals, window)
  if (placements.length === 0) {
    return null
  }
  return (
    <div className="strip" role="img" aria-label={`${label} state history`}>
      {placements.map((placement) => (
        <span
          key={key(placement, 'band')}
          className={bandClass(placement.interval)}
          style={{ left: `${placement.left}%`, width: `${placement.width}%` }}
          title={describe(placement.interval)}
        />
      ))}
      {placements
        .filter((placement) => placement.marked)
        .map((placement) => (
          <span
            key={key(placement, 'marker')}
            className={markerClass(placement.interval)}
            style={{ left: `${placement.center}%` }}
            title={describe(placement.interval)}
          />
        ))}
    </div>
  )
}
