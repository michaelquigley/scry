import type { Rollup } from '../api/client'

// RollupBanner answers the page's one question before anything else is read.
export function RollupBanner({ rollup }: { rollup: Rollup }) {
  const total = rollup.ok + rollup.late + rollup.failed
  if (total === 0) {
    return <div className="rollup rollup-empty">no checks configured</div>
  }

  const trouble = rollup.late + rollup.failed
  if (trouble === 0) {
    return (
      <div className="rollup rollup-ok">
        all clear
        <span className="rollup-detail">
          {total} {total === 1 ? 'check' : 'checks'} ok
        </span>
      </div>
    )
  }

  return (
    <div className={`rollup ${rollup.failed > 0 ? 'rollup-failed' : 'rollup-late'}`}>
      {rollup.late} late / {rollup.failed} failed
      <span className="rollup-detail">{rollup.ok} ok</span>
    </div>
  )
}
