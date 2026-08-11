import { useEffect, useRef, useState } from 'react'
import { fetchHistory, fetchStatus, type StatusDocument } from './api/client'
import { CheckTable } from './components/CheckTable'
import { RollupBanner } from './components/RollupBanner'
import { displayWindow, nextHistoryDirty, supersedes, type HistoryState } from './history'
import { formatDuration, formatTimestamp } from './util'

const pollInterval = 10_000
const agePulse = 1_000

const emptyHistory: HistoryState = { document: null, startedUnder: null, dirty: true }

export default function App() {
  const [status, setStatus] = useState<StatusDocument | null>(null)
  const [stale, setStale] = useState(false)
  const [loaded, setLoaded] = useState(false)
  // the history document, the daemon boot it was fetched under, and whether the
  // page knows it is behind. the ref is what the poll loop reads; the state is
  // what renders.
  const [history, setHistory] = useState<HistoryState>(emptyHistory)
  const historyRef = useRef<HistoryState>(emptyHistory)
  // the newest status document, which is what an arriving history document is
  // judged against — not whichever poll happened to dispatch the fetch.
  const statusRef = useRef<StatusDocument | null>(null)
  // when the current document arrived, in the browser's clock frame; ages are
  // the daemon's own spans plus how long this document has been on screen.
  const [receivedAt, setReceivedAt] = useState<number | null>(null)
  // a heartbeat so every age on the page counts live between polls.
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const controller = new AbortController()

    const commit = (next: HistoryState) => {
      historyRef.current = next
      setHistory(next)
    }

    // history rides the status cadence: every successful poll retries while the
    // dirty flag is set, so a quiet estate refetches never and no second timer
    // exists.
    const refresh = async (document: StatusDocument) => {
      if (!nextHistoryDirty(historyRef.current, document, 'none')) {
        return
      }
      // the flag is committed before the request, not after it: the page has
      // already decided the held document is obsolete, and it must not paint
      // confident state across that span while the refetch is in flight.
      commit({ ...historyRef.current, dirty: true })
      try {
        const recorded = await fetchHistory(controller.signal)
        // the poll timer does not await its predecessor, so two fetches can be
        // in flight and land out of order. the document's own watermark orders
        // them: a response older than the one already held is superseded, and
        // ignoring it leaves the newer document and the dirty flag alone.
        if (supersedes(historyRef.current.document, recorded)) {
          // the flag is level-triggered, so it is decided against the newest
          // status the page has — not against the poll that dispatched this
          // fetch. an arriving document that already fails that test is kept
          // and stays dirty rather than clearing on a stale decision.
          const arrived: HistoryState = {
            document: recorded,
            startedUnder: document.started,
            dirty: false,
          }
          commit({ ...arrived, dirty: nextHistoryDirty(arrived, statusRef.current, 'none') })
        }
      } catch {
        if (!controller.signal.aborted) {
          // a failed fetch keeps the last document, the same stale posture the
          // status document has.
          commit({ ...historyRef.current, dirty: true })
        }
      }
    }

    // a failed poll never discards the last good document: the page keeps
    // showing the estate it last knew and says plainly that it is stale.
    const poll = async () => {
      try {
        const document = await fetchStatus(controller.signal)
        statusRef.current = document
        setStatus(document)
        setReceivedAt(Date.now())
        setStale(false)
        await refresh(document)
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

  const strip = status
    ? displayWindow({
        status,
        ageOffset,
        stale,
        history: history.document,
        historyDirty: history.dirty,
      })
    : null

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
          <CheckTable
            checks={status.checks}
            generated={status.generated}
            ageOffset={ageOffset}
            history={history.document}
            window={strip}
          />
        </>
      ) : (
        <p className="placeholder">{loaded ? 'no status available' : 'loading'}</p>
      )}

      <footer className="app-footer">
        <div className="app-footer-brand">
          <img className="app-footer-scry-logo" src="/scry-logo.svg" alt="scry" />
          <span className="app-footer-separator" aria-hidden="true">
            &middot;
          </span>
          <img className="app-footer-metawoo-logo" src="/metawoo-logo.svg" alt="metawoo" />
        </div>
        <a
          className="app-footer-link"
          href="https://github.com/michaelquigley/scry"
          target="_blank"
          rel="noreferrer"
        >
          github.com/michaelquigley/scry
        </a>
      </footer>
    </main>
  )
}
