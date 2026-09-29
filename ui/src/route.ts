// the page has two places to be: the estate at / and one check's detail at
// /check/{id}. routing is a pathname match and the history api; a router
// library would be more code than the routes.

export type Route =
  | { page: 'estate' }
  | { page: 'check'; id: string }
  | { page: 'missing'; path: string }

// the id carries the shape configuration accepts for a check id. the daemon's
// asset handler answers the index for exactly this shape and 404s the rest, so
// the two matchers move together with the configuration's own pattern: a looser
// match here would route a path the daemon refuses to serve on reload.
const checkPath = /^\/check\/([a-z0-9]+(?:-[a-z0-9]+)*)\/?$/

export function matchRoute(pathname: string): Route {
  if (pathname === '' || pathname === '/' || pathname === '/index.html') {
    return { page: 'estate' }
  }
  const matched = checkPath.exec(pathname)
  if (matched) {
    return { page: 'check', id: matched[1] }
  }
  return { page: 'missing', path: pathname }
}

export const estateHref = '/'

export function checkHref(id: string): string {
  return `/check/${id}`
}

// openCheckOf names the check whose detail the route shows, which is the whole
// of what the poll loop needs to know about where the page is.
export function openCheckOf(route: Route): string | null {
  return route.page === 'check' ? route.id : null
}

// navigate pushes a client-side route and tells the page about it the same way
// the back button does. the event is dispatched synchronously, so whatever
// follows the address bar has already moved by the time this returns. a pushed
// route starts at the top, where a loaded page would; the back button is left
// to the browser's own scroll restoration.
export function navigate(to: string): void {
  window.history.pushState(null, '', to)
  window.dispatchEvent(new PopStateEvent('popstate'))
  window.scrollTo(0, 0)
}
