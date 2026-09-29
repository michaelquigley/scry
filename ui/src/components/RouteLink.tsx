import type { MouseEvent, ReactNode } from 'react'
import { navigate } from '../route'

// RouteLink is a real anchor that the page follows itself. a plain click stays
// on the page and keeps every held document; a modified or non-primary click is
// left to the browser, which opens the path the daemon also serves.
export function RouteLink({
  to,
  className,
  label,
  children,
}: {
  to: string
  className?: string
  label?: string
  children: ReactNode
}) {
  const follow = (event: MouseEvent<HTMLAnchorElement>) => {
    if (event.defaultPrevented || event.button !== 0) {
      return
    }
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return
    }
    event.preventDefault()
    navigate(to)
  }
  return (
    <a href={to} className={className} aria-label={label} onClick={follow}>
      {children}
    </a>
  )
}
