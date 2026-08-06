import { useEffect, useState } from 'react'
import { fetchStatus, type StatusDocument } from './api/client'
import { CheckTable } from './components/CheckTable'
import { RollupBanner } from './components/RollupBanner'
import { formatDuration, formatTimestamp } from './util'

const pollInterval = 10_000
const agePulse = 1_000

export default function App() {
  const [status, setStatus] = useState<StatusDocument | null>(null)
  const [stale, setStale] = useState(false)
  const [loaded, setLoaded] = useState(false)
  // when the current document arrived, in the browser's clock frame; ages are
  // the daemon's own spans plus how long this document has been on screen.
  const [receivedAt, setReceivedAt] = useState<number | null>(null)
  // a heartbeat so every age on the page counts live between polls.
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const controller = new AbortController()

    // a failed poll never discards the last good document: the page keeps
    // showing the estate it last knew and says plainly that it is stale.
    const poll = async () => {
      try {
        const document = await fetchStatus(controller.signal)
        setStatus(document)
        setReceivedAt(Date.now())
        setStale(false)
      } catch {
        if (controller.signal.aborted) {
          return
        }
        setStale(true)
      } finally {
        if (!controller.signal.aborted) {
          setLoaded(true)
        }
      }
    }

    void poll()
    const timer = window.setInterval(() => void poll(), pollInterval)
    const pulse = window.setInterval(() => setNow(Date.now()), agePulse)
    return () => {
      controller.abort()
      window.clearInterval(timer)
      window.clearInterval(pulse)
    }
  }, [])

  const ageOffset = receivedAt === null ? 0 : Math.max(0, now - receivedAt)
  const estate = status?.estate ?? 'scry'

  useEffect(() => {
    document.title = estate
  }, [estate])

  return (
    <main>
      <header>
        <h1>{estate}</h1>
        {status ? (
          <span className="generated">
            as of {formatTimestamp(status.generated)} · {formatDuration(ageOffset)} ago
          </span>
        ) : null}
      </header>

      {stale ? (
        <p className="stale" role="status">
          stale — the last poll failed; showing the most recent status received
        </p>
      ) : null}

      {status ? (
        <>
          <RollupBanner rollup={status.rollup} />
          <CheckTable checks={status.checks} generated={status.generated} ageOffset={ageOffset} />
        </>
      ) : (
        <p className="placeholder">{loaded ? 'no status available' : 'loading'}</p>
      )}
    </main>
  )
}
