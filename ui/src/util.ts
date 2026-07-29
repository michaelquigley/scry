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

// formatTimestamp renders an API timestamp in the viewer's local zone.
export function formatTimestamp(value: string): string {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) {
    return value
  }
  return parsed.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
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
