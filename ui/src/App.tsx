import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchHistory, fetchStatus, type StatusDocument } from './api/client'
import { CheckTable } from './components/CheckTable'
import { RollupBanner } from './components/RollupBanner'
import {
  defaultPreset,
  detailLands,
  detailSource,
  displayWindow,
  displayWindowMs,
  nextHistoryDirty,
  panelFetches,
  pollPlan,
  requestBounds,
  supersedes,
  vouchingCap,
  type HistoryState,
  type Preset,
} from './history'
import { formatDuration, formatTimestamp } from './util'

const pollInterval = 10_000
const agePulse = 1_000

const emptyHistory: HistoryState = {
  document: null,
  startedUnder: null,
  dirty: true,
  arrival: null,
}

export default function App() {
  const [status, setStatus] = useState<StatusDocument | null>(null)
  const [stale, setStale] = useState(false)
  const [loaded, setLoaded] = useState(false)
  // the history document, the daemon boot it was fetched under, whether the
  // page knows it is behind, and when it arrived. the ref is what the poll loop
  // reads; the state is what renders.
  const [history, setHistory] = useState<HistoryState>(emptyHistory)
  const historyRef = useRef<HistoryState>(emptyHistory)
  // the panel's own document, for every preset but the one that reuses the
  // strip's. it carries the same four fields, because its list ages count from
  // its own arrival, not the strip's and not the status document's.
  const [detail, setDetail] = useState<HistoryState>(emptyHistory)
  const detailRef = useRef<HistoryState>(emptyHistory)
  // the newest status document, which is what an arriving history document is
  // judged against — not whichever poll happened to dispatch the fetch.
  const statusRef = useRef<StatusDocument | null>(null)
  // when the current status document arrived, in the browser's clock frame.
  const [receivedAt, setReceivedAt] = useState<number | null>(null)
  // a heartbeat so every age on the page counts live between polls.
  const [now, setNow] = useState(() => Date.now())
  // which check's panel is open, and over which window. both are mirrored into
  // refs because the poll loop is created once and cannot see later state.
  const [openCheck, setOpenCheck] = useState<string | null>(null)
  const openRef = useRef<string | null>(null)
  const [preset, setPreset] = useState<Preset>(defaultPreset)
  const presetRef = useRef<Preset>(defaultPreset)
  const abortRef = useRef<AbortController | null>(null)

  const commitHistory = useCallback((next: HistoryState) => {
    historyRef.current = next
    setHistory(next)
  }, [])

  const commitDetail = useCallback((next: HistoryState) => {
    detailRef.current = next
    setDetail(next)
  }, [])

  // refreshDetail is the panel's own fetch. the dirty level is maintained on
  // every successful poll whether or not the panel is open — a closed panel may
  // hold an obsolete document but may not let it age clean — and only the fetch
  // is gated here, because a closed panel has nothing to render and the default
  // preset has no second document to fetch.
  const refreshDetail = useCallback(async () => {
    const controller = abortRef.current
    const dispatchedUnder = statusRef.current
    const chosen = presetRef.current
    if (controller === null || dispatchedUnder === null) {
      return
    }
    if (!panelFetches(openRef.current !== null, chosen)) {
      return
    }
    if (!detailRef.current.dirty) {
      return
    }
    // the flag is committed before the request, exactly as the strip's is.
    commitDetail({ ...detailRef.current, dirty: true })
    const bounds = requestBounds(chosen, dispatchedUnder.generated)
    try {
      const recorded = await fetchHistory(controller.signal, bounds.from, bounds.to)
      // the two landing conditions, both judged against what the page asks for
      // now rather than what this request was dispatched under. a mismatch of
      // either kind is ignored outright: the held document stands and so does
      // the dirty flag, which the next poll retries.
      if (!detailLands(recorded, presetRef.current, statusRef.current)) {
        return
      }
      if (!supersedes(detailRef.current.document, recorded)) {
        return
      }
      const arrived: HistoryState = {
        document: recorded,
        startedUnder: dispatchedUnder.started,
        dirty: false,
        arrival: Date.now(),
      }
      commitDetail({ ...arrived, dirty: nextHistoryDirty(arrived, statusRef.current, 'none') })
    } catch {
      // a failed fetch commits nothing. the flag was committed dirty before the
      // request, and only a document that lands and revalidates ever clears it,
      // so a failure of this request leaves the level exactly where it belongs:
      // still set if nothing landed meanwhile, and clear if a newer response
      // did — which this older failure has no standing to overturn.
    }
  }, [commitDetail])

  useEffect(() => {
    const controller = new AbortController()
    abortRef.current = controller

    // the strip's fetch. it needs no landing rule: its display window is
    // anchored to the status document's own stamp, so a response older than the
    // newest status only shortens the history-vouched coverage, and the span
    // beyond it is vouched by the status testimony.
    const refreshStrip = async (dispatchedUnder: StatusDocument) => {
      try {
        // the strip asks for its own display window rather than the daemon's
        // default, so the two documents describe the same span to the
        // millisecond and nothing is left over at the left edge. to never
        // passes the daemon's clock in the normal flow, because the status poll
        // precedes this request inside one loop iteration; a backwards host
        // clock between the two reads surfaces as a 400, which is a failed
        // fetch, which the dirty flag already retries.
        const to = dispatchedUnder.generated
        const from = new Date(Date.parse(to) - displayWindowMs).toISOString()
        const recorded = await fetchHistory(controller.signal, from, to)
        // the poll timer does not await its predecessor, so two fetches can be
        // in flight and land out of order. the end of the coverage each
        // document serves orders them: a response covering less than the one
        // already held is superseded, and ignoring it leaves the newer document
        // and the dirty flag alone.
        if (supersedes(historyRef.current.document, recorded)) {
          // the flag is level-triggered, so it is decided against the newest
          // status the page has — not against the poll that dispatched this
          // fetch. an arriving document that already fails that test is kept
          // and stays dirty rather than clearing on a stale decision.
          const arrived: HistoryState = {
            document: recorded,
            startedUnder: dispatchedUnder.started,
            dirty: false,
            arrival: Date.now(),
          }
          commitHistory({ ...arrived, dirty: nextHistoryDirty(arrived, statusRef.current, 'none') })
        }
      } catch {
        // as above: the level was set before dispatch and a failure disturbs
        // nothing. dirtying the held document here would let a slow request's
        // failure hatch the vouched tail of a newer one that already landed.
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

        // one pre-request block. both dirty levels are evaluated against this
        // poll's document and both flags committed before either request is
        // dispatched, so neither surface may extend obsolete state during the
        // other's in-flight fetch — and no panel request is dispatched from an
        // obsolete status stamp, which the landing rule would reject and which
        // would leave the panel flapping behind the strip.
        const plan = pollPlan({
          strip: historyRef.current,
          panel: detailRef.current,
          status: document,
          panelOpen: openRef.current !== null,
          preset: presetRef.current,
        })
        if (plan.stripDirty && !historyRef.current.dirty) {
          commitHistory({ ...historyRef.current, dirty: true })
        }
        if (plan.panelDirty && !detailRef.current.dirty) {
          commitDetail({ ...detailRef.current, dirty: true })
        }
        const dispatched: Promise<void>[] = []
        if (plan.fetchStrip) {
          dispatched.push(refreshStrip(document))
        }
        if (plan.fetchPanel) {
          dispatched.push(refreshDetail())
        }
        await Promise.all(dispatched)
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
      abortRef.current = null
      window.clearInterval(timer)
      window.clearInterval(pulse)
    }
  }, [commitDetail, commitHistory, refreshDetail])

  // opening a panel whose flag went dirty during closure kicks the fetch at
  // once, exactly as a preset change does.
  useEffect(() => {
    openRef.current = openCheck
    presetRef.current = preset
    void refreshDetail()
  }, [openCheck, preset, refreshDetail])

  const ageOffset = receivedAt === null ? 0 : Math.max(0, now - receivedAt)
  // the page's one vouching cap, computed once and handed to every window it
  // derives, so no two surfaces can cap differently.
  const staleCap = vouchingCap(status, stale)
  const estate = status?.estate ?? 'scry'

  useEffect(() => {
    document.title = estate
  }, [estate])

  const strip = status
    ? displayWindow({
        status,
        ageOffset,
        staleCap,
        history: history.document,
        historyDirty: history.dirty,
      })
    : null

  const source =
    openCheck === null
      ? null
      : detailSource({ preset, strip: history, stripWindow: strip, panel: detail, now, staleCap })

  const choosePreset = (next: Preset) => {
    if (next === preset) {
      return
    }
    // the ref moves first, and synchronously. the landing rule judges an
    // arriving response against what the page asks for *now*, so that answer
    // may never lag the abandonment it is judging: a response resolving between
    // this click and the effect below would otherwise be read against the
    // preset just left, land clean, and leave the new window rendering from a
    // document that never covered it — with the flag clear, so nothing refetches.
    presetRef.current = next
    // a preset change abandons the held document and dirties the flag. the
    // response already in the air for the old window cannot land: its echoed
    // bounds no longer carry the shape the page asks for.
    commitDetail(emptyHistory)
    setPreset(next)
  }

  const toggleCheck = (id: string) => {
    const next = openCheck === id ? null : id
    // open state gates only the dispatch, never a landing, so a stale ref here
    // is harmless — but both refs moving with the click is one rule instead of
    // two cases to reason about.
    openRef.current = next
    setOpenCheck(next)
  }

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
            openCheck={openCheck}
            onToggle={toggleCheck}
            source={source}
            preset={preset}
            onPreset={choosePreset}
            now={now}
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
