import type { components } from './schema'

export type StatusDocument = components['schemas']['status']
export type Rollup = components['schemas']['rollup']
export type Check = components['schemas']['check']
export type CheckState = components['schemas']['state']

const statusPath = '/api/status'

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
