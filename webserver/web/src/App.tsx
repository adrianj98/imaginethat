import { useCallback, useEffect, useState } from 'react'
import * as api from './api'
import type { Vault } from './api'
import { type Backend, clientBackend, logout, serverBackend } from './backend'
import VaultList from './components/VaultList'
import Browser from './components/Browser'
import Sidebar from './components/Sidebar'
import UnlockCard from './components/UnlockCard'
import Icon from './components/Icon'
import Login from './components/Login'

// The URL hash is the single source of truth for where the user is:
// #/v/<vaultId>/<path> inside a vault, anything else is the vault list.
// Hash routing (rather than the History API) keeps the embedded SPA free of
// server-side fallback routes, and every hash change gets a history entry, so
// the browser's back/forward buttons walk the folder trail naturally.
interface Route {
  vaultId: string | null
  path: string
}

function parseHash(): Route {
  const m = /^#\/v\/([^/]+)(?:\/(.+))?$/.exec(window.location.hash)
  if (!m) return { vaultId: null, path: '' }
  // Each segment is encoded separately so file names containing '/' -alikes
  // ('%2F') cannot masquerade as deeper folders.
  const path = m[2] ? m[2].split('/').map(decodeURIComponent).join('/') : ''
  return { vaultId: m[1], path }
}

function hashFor(vaultId: string | null, path: string): string {
  if (!vaultId) return '#/'
  const suffix = path ? '/' + path.split('/').map(encodeURIComponent).join('/') : ''
  return `#/v/${vaultId}${suffix}`
}

const SIDEBAR_KEY = 'imagethat.sidebar'

function initialSidebar(): boolean {
  try {
    const saved = localStorage.getItem(SIDEBAR_KEY)
    if (saved !== null) return saved === '1'
  } catch {
    // Storage blocked (private window): fall through to the default.
  }
  return window.matchMedia('(min-width: 801px)').matches
}

export default function App() {
  // Which way the server works decides who unlocks and decrypts vaults.
  const [backend, setBackend] = useState<Backend | null>(null)
  // Client-decrypt servers want their access password before anything else.
  const [needsLogin, setNeedsLogin] = useState(false)
  const [vaults, setVaults] = useState<Vault[] | null>(null)
  const [route, setRoute] = useState<Route>(parseHash)
  const [error, setError] = useState<string | null>(null)
  const [sidebar, setSidebar] = useState(initialSidebar)
  // The vault whose unlock card is showing.
  const [unlocking, setUnlocking] = useState<Vault | null>(null)

  useEffect(() => {
    const onHashChange = () => setRoute(parseHash())
    window.addEventListener('hashchange', onHashChange)
    return () => window.removeEventListener('hashchange', onHashChange)
  }, [])

  const navigate = useCallback((vaultId: string | null, path = '') => {
    const target = hashFor(vaultId, path)
    if (window.location.hash === target) return
    // Assigning location.hash pushes a history entry and fires hashchange,
    // which is what updates `route` — the URL stays the source of truth.
    window.location.hash = target
  }, [])

  useEffect(() => {
    api
      .info()
      .then(({ mode }) => setBackend(mode === 'client-decrypt' ? clientBackend : serverBackend))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  const refresh = useCallback(async () => {
    if (!backend) return
    try {
      setVaults(await backend.listVaults())
      setNeedsLogin(false)
      setError(null)
    } catch (e) {
      if (backend.mode === 'client-decrypt' && e instanceof api.ApiError && e.status === 401) {
        setNeedsLogin(true)
        return
      }
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [backend])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    backend?.prepare().catch((e) => setError(e instanceof Error ? e.message : String(e)))
  }, [backend])

  const toggleSidebar = useCallback(() => {
    setSidebar((open) => {
      try {
        localStorage.setItem(SIDEBAR_KEY, open ? '0' : '1')
      } catch {
        // Not remembered; harmless.
      }
      return !open
    })
  }, [])

  const handleLock = useCallback(
    async (id: string) => {
      await backend?.lock(id)
      if (route.vaultId === id) navigate(null)
      await refresh()
    },
    [backend, navigate, refresh, route.vaultId],
  )

  const handleLogout = useCallback(async () => {
    try {
      await logout()
    } finally {
      setVaults(null)
      navigate(null)
      await refresh()
    }
  }, [navigate, refresh])
  const onLogout = backend?.mode === 'client-decrypt' ? () => void handleLogout() : undefined

  const select = useCallback(
    (v: Vault) => {
      if (v.unlocked) navigate(v.id)
      else setUnlocking(v)
    },
    [navigate],
  )

  // The route may name a vault that is locked or gone (deep link, expired
  // session): show the vault list then, and keep the hash so unlocking the
  // vault drops the user right back where the link pointed.
  const active = vaults?.find((v) => v.id === route.vaultId && v.unlocked) ?? null
  const linkedLocked = vaults?.find((v) => v.id === route.vaultId && !v.unlocked) ?? null

  // A deep link to a locked vault asks for its password straight away.
  const linkedLockedId = linkedLocked?.id
  useEffect(() => {
    const v = vaults?.find((x) => x.id === linkedLockedId)
    if (v) setUnlocking(v)
    // Only when the link starts pointing at a different locked vault, so
    // Cancel is not undone by the next vault-list refresh.
  }, [linkedLockedId]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="app">
      {error && (
        <div className="banner glass toast">
          <Icon name="warning" />
          {error}
        </div>
      )}

      {!backend ? (
        !error && <p className="muted centered">Connecting…</p>
      ) : needsLogin ? (
        <Login onLoggedIn={() => void refresh()} />
      ) : active && vaults ? (
        <>
          {sidebar && (
            <Sidebar
              vaults={vaults}
              activeId={active.id}
              onSelect={select}
              onHome={() => navigate(null)}
              onLock={(id) => void handleLock(id)}
              onLogout={onLogout}
            />
          )}
          <div className={`content${sidebar ? ' with-sidebar' : ''}`}>
            <Browser
              backend={backend}
              vault={active}
              path={route.path}
              sidebarOpen={sidebar}
              onToggleSidebar={toggleSidebar}
              onNavigate={(path) => navigate(active.id, path)}
              onLeaveVault={() => navigate(null)}
              onLock={() => void handleLock(active.id)}
              onSessionLost={() => void refresh()}
            />
          </div>
        </>
      ) : (
        <VaultList
          mode={backend.mode}
          vaults={vaults}
          onSelect={select}
          onLock={(id) => void handleLock(id)}
          onLogout={onLogout}
        />
      )}

      {unlocking && backend && (
        <UnlockCard
          backend={backend}
          vault={unlocking}
          onCancel={() => setUnlocking(null)}
          onUnlocked={(id) => {
            setUnlocking(null)
            // Unlocking the vault a deep link points at keeps its path.
            navigate(id, route.vaultId === id ? route.path : '')
            void refresh()
          }}
        />
      )}
    </div>
  )
}
