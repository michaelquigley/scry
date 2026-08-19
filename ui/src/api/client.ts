import type { components } from './schema'

export type StatusDocument = components['schemas']['status']
export type Rollup = components['schemas']['rollup']
export type Check = components['schemas']['check']
export type CheckState = components['schemas']['state']

export type HistoryDocument = components['schemas']['history']
export type CheckHistory = components['schemas']['check_history']
export type TransitionEvent = components['schemas']['transition_event']
export type LifecycleEvent = components['schemas']['lifecycle_event']

const statusPath = '/api/status'
const historyPath = '/api/history'

// fetchStatus reads the single walk of the status model. the document is the
// only thing the dashboard knows about scry.
export async function fetchStatus(signal: AbortSignal): Promise<StatusDocument> {
  const response = await fetch(statusPath, {
    signal,
    headers: { accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`status request failed: ${response.status}`)
  }
  return (await response.json()) as StatusDocument
}

// fetchHistory reads the recorded history document over an explicit window.
// both bounds are RFC3339 UTC strings; either one omitted asks the daemon for
// its own default on that side, which is what a parameterless call means.
export async function fetchHistory(
  signal: AbortSignal,
  from?: string,
  to?: string,
): Promise<HistoryDocument> {
  const query = new URLSearchParams()
  if (from !== undefined) {
    query.set('from', from)
  }
  if (to !== undefined) {
    query.set('to', to)
  }
  const search = query.toString()
  const response = await fetch(search === '' ? historyPath : `${historyPath}?${search}`, {
    signal,
    headers: { accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`history request failed: ${response.status}`)
  }
  return (await response.json()) as HistoryDocument
}
