import { useCallback, useEffect, useState } from 'react'
import * as api from './api'
import type { Vault } from './api'
import VaultList from './components/VaultList'
import Browser from './components/Browser'

export default function App() {
  const [vaults, setVaults] = useState<Vault[] | null>(null)
  const [activeId, setActiveId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const { vaults } = await api.listVaults()
      setVaults(vaults)
      setError(null)
      // If the active vault got locked out from under us, go back to the list.
      setActiveId((current) =>
        current && vaults.some((v) => v.id === current && v.unlocked) ? current : null,
      )
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const handleLock = useCallback(
    async (id: string) => {
      await api.lockVault(id)
      setActiveId(null)
      await refresh()
    },
    [refresh],
  )

  const active = vaults?.find((v) => v.id === activeId && v.unlocked) ?? null

  return (
    <div className="app">
      <header className="topbar">
        <button
          className="brand"
          onClick={() => setActiveId(null)}
          title="Back to vaults"
          type="button"
        >
          ImageThat
        </button>
        {active && (
          <>
            <span className="topbar-vault">{active.name}</span>
            <button className="btn" type="button" onClick={() => void handleLock(active.id)}>
              Lock
            </button>
          </>
        )}
      </header>

      {error && <div className="banner error">{error}</div>}

      <main>
        {active ? (
          <Browser vault={active} onSessionLost={() => void refresh()} />
        ) : (
          <VaultList
            vaults={vaults}
            onOpened={(id) => {
              setActiveId(id)
              void refresh()
            }}
            onRefresh={refresh}
          />
        )}
      </main>
    </div>
  )
}
