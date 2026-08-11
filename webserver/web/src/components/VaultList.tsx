import { useState } from 'react'
import * as api from '../api'
import type { Vault } from '../api'

interface Props {
  vaults: Vault[] | null
  onOpened: (id: string) => void
  onRefresh: () => Promise<void>
}

/** Vault picker with an inline unlock form. */
export default function VaultList({ vaults, onOpened, onRefresh }: Props) {
  const [unlocking, setUnlocking] = useState<string | null>(null)
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(id: string) {
    setBusy(true)
    setError(null)
    try {
      await api.unlockVault(id, password)
      // Clear the password from component state as soon as it has been used.
      setPassword('')
      setUnlocking(null)
      onOpened(id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (vaults === null) {
    return <p className="muted centered">Loading vaults…</p>
  }

  if (vaults.length === 0) {
    return (
      <div className="empty">
        <h2>No vaults found</h2>
        <p className="muted">
          The server did not find any directory containing a <code>gocryptfs.conf</code> under its
          configured root. Start it with <code>-root</code> pointing at your vaults.
        </p>
      </div>
    )
  }

  return (
    <div className="vault-list">
      <h2>Vaults</h2>
      {vaults.map((v) => (
        <div key={v.id} className="vault-card">
          <div className="vault-card-head">
            <span className="vault-icon" aria-hidden="true">
              {v.unlocked ? '🔓' : '🔒'}
            </span>
            <span className="vault-name">{v.name}</span>
            {v.unlocked ? (
              <>
                <button className="btn primary" type="button" onClick={() => onOpened(v.id)}>
                  Open
                </button>
                <button
                  className="btn"
                  type="button"
                  onClick={async () => {
                    await api.lockVault(v.id)
                    await onRefresh()
                  }}
                >
                  Lock
                </button>
              </>
            ) : (
              <button
                className="btn primary"
                type="button"
                onClick={() => {
                  setUnlocking(unlocking === v.id ? null : v.id)
                  setPassword('')
                  setError(null)
                }}
              >
                Unlock
              </button>
            )}
          </div>

          {unlocking === v.id && (
            <form
              className="unlock-form"
              onSubmit={(e) => {
                e.preventDefault()
                void submit(v.id)
              }}
            >
              <input
                type="password"
                autoFocus
                autoComplete="current-password"
                placeholder="Vault password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={busy}
              />
              <button className="btn primary" type="submit" disabled={busy || !password}>
                {busy ? 'Unlocking…' : 'Unlock'}
              </button>
              {error && <span className="error-text">{error}</span>}
            </form>
          )}
        </div>
      ))}
    </div>
  )
}
