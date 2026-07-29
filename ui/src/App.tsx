import { useEffect, useState } from 'react'
import { fetchStatus, type StatusDocument } from './api/client'
import { CheckTable } from './components/CheckTable'
import { RollupBanner } from './components/RollupBanner'
import { formatTimestamp } from './util'

const pollInterval = 10_000

export default function App() {
  const [status, setStatus] = useState<StatusDocument | null>(null)
  const [stale, setStale] = useState(false)
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    const controller = new AbortController()

    // a failed poll never discards the last good document: the page keeps
    // showing the estate it last knew and says plainly that it is stale.
    const poll = async () => {
      try {
        const document = await fetchStatus(controller.signal)
        setStatus(document)
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
    return () => {
      controller.abort()
      window.clearInterval(timer)
    }
  }, [])

  return (
    <main>
      <header>
        <h1>scry</h1>
        {status ? <span className="generated">as of {formatTimestamp(status.generated)}</span> : null}
      </header>

      {stale ? (
        <p className="stale" role="status">
          stale — the last poll failed; showing the most recent status received
        </p>
      ) : null}

      {status ? (
        <>
          <RollupBanner rollup={status.rollup} />
          <CheckTable checks={status.checks} generated={status.generated} />
        </>
      ) : (
        <p className="placeholder">{loaded ? 'no status available' : 'loading'}</p>
      )}
    </main>
  )
}
