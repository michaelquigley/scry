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

// fetchHistory reads the recorded history document. it sends no bounds: the
// daemon's defaults are the page's window, resolved inside the one cut that
// stamps the document.
export async function fetchHistory(signal: AbortSignal): Promise<HistoryDocument> {
  const response = await fetch(historyPath, {
    signal,
    headers: { accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`history request failed: ${response.status}`)
  }
  return (await response.json()) as HistoryDocument
}
