import { describe, expect, it } from 'vitest'
import { checkHref, estateHref, matchRoute, openCheckOf } from './route'

describe('matchRoute', () => {
  it('reads the estate from the root and the index file', () => {
    for (const pathname of ['', '/', '/index.html']) {
      expect(matchRoute(pathname)).toEqual({ page: 'estate' })
    }
    expect(matchRoute(estateHref)).toEqual({ page: 'estate' })
  })

  it('reads a check from its detail path, with or without a trailing slash', () => {
    expect(matchRoute('/check/nas-snapshot')).toEqual({ page: 'check', id: 'nas-snapshot' })
    expect(matchRoute('/check/nas-snapshot/')).toEqual({ page: 'check', id: 'nas-snapshot' })
    expect(matchRoute('/check/a')).toEqual({ page: 'check', id: 'a' })
    expect(matchRoute('/check/db2-replica-01')).toEqual({ page: 'check', id: 'db2-replica-01' })
  })

  // the same near misses the daemon's asset handler answers 404 for. the two
  // matchers are one shape stated twice, and these cases are what pins them.
  it('refuses every path the daemon refuses to serve the page for', () => {
    for (const pathname of [
      '/checks/nas-snapshot',
      '/report/nas-snapshot',
      '/dashboard',
      '/check',
      '/check/',
      '/check/nas-snapshot/history',
      '/check/NAS-snapshot',
      '/check/nas_snapshot',
      '/check/nas--snapshot',
      '/check/-nas',
      '/check/nas-',
      '/check/nas.js',
    ]) {
      expect(matchRoute(pathname)).toEqual({ page: 'missing', path: pathname })
    }
  })
})

describe('checkHref', () => {
  it('builds the path matchRoute reads back as the same check', () => {
    for (const id of ['a', 'nas-snapshot', 'db2-replica-01', '0', 'x-1-y']) {
      expect(matchRoute(checkHref(id))).toEqual({ page: 'check', id })
    }
  })
})

describe('openCheckOf', () => {
  it('names a check only on a check route', () => {
    expect(openCheckOf({ page: 'estate' })).toBeNull()
    expect(openCheckOf({ page: 'missing', path: '/dashboard' })).toBeNull()
    expect(openCheckOf({ page: 'check', id: 'nas-snapshot' })).toBe('nas-snapshot')
  })
})
