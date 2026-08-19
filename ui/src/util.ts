const second = 1000
const minute = 60 * second
const hour = 60 * minute
const day = 24 * hour

// formatDuration renders an elapsed span compactly. spans are measured against
// the document's own generated stamp, so the page reports the daemon's arithmetic
// rather than the browser's clock.
export function formatDuration(elapsed: number): string {
  if (!Number.isFinite(elapsed) || elapsed < second) {
    return '0s'
  }
  if (elapsed < minute) {
    return `${Math.floor(elapsed / second)}s`
  }
  if (elapsed < hour) {
    return `${Math.floor(elapsed / minute)}m`
  }
  if (elapsed < day) {
    return `${Math.floor(elapsed / hour)}h ${Math.floor((elapsed % hour) / minute)}m`
  }
  return `${Math.floor(elapsed / day)}d ${Math.floor((elapsed % day) / hour)}h`
}

// formatTimestamp renders an API timestamp in the viewer's local zone. the year
// is carried only when the caller asks for it, so the page's default form stays
// the compact one every row strip and header already reads in.
export function formatTimestamp(value: string, withYear = false): string {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) {
    return value
  }
  return parsed.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    ...(withYear ? { year: 'numeric' } : {}),
    hour: '2-digit',
    minute: '2-digit',
  })
}

// outsideCurrentYear decides whether a timestamp must carry its year: the 1y
// and all presets put events in the list that "Jan 15, 03:10" cannot place, and
// an event in the viewer's own calendar year is unambiguous without it. both
// sides are read in the viewer's local zone, as the rest of the page is.
export function outsideCurrentYear(value: string, now: number): boolean {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) {
    return false
  }
  return parsed.getFullYear() !== new Date(now).getFullYear()
}

// elapsedSince measures one timestamp against another, both as API strings.
export function elapsedSince(from: string, to: string): number {
  const start = Date.parse(from)
  const end = Date.parse(to)
  if (Number.isNaN(start) || Number.isNaN(end)) {
    return Number.NaN
  }
  return end - start
}

// formatTimestampWithAge renders a timestamp with how long ago it was: the
// daemon-computed span to the reference, plus extraMs of locally elapsed time
// since the document arrived. each term stays in its own clock frame, so ages
// tick live without mixing daemon and browser clocks. an unmeasurable age
// degrades to the bare timestamp.
export function formatTimestampWithAge(
  value: string,
  reference: string,
  extraMs = 0,
  withYear = false,
): string {
  const elapsed = elapsedSince(value, reference)
  if (Number.isNaN(elapsed) || elapsed + extraMs < 0) {
    return formatTimestamp(value, withYear)
  }
  return `${formatTimestamp(value, withYear)} · ${formatDuration(elapsed + extraMs)} ago`
}
