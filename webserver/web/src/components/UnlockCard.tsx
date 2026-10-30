import { useState } from 'react'
import type { Vault } from '../api'
import type { Backend } from '../backend'
import Icon from './Icon'

interface Props {
  backend: Backend
  vault: Vault
  onUnlocked: (id: string) => void
  onCancel: () => void
}

/** Centered unlock card over a dimmed page, as in the macOS VaultUnlockSheet. */
export default function UnlockCard({ backend, vault, onUnlocked, onCancel }: Props) {
  const inBrowser = backend.mode === 'client-decrypt'
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      await backend.unlock(vault.id, password)
      // Clear the password from component state as soon as it has been used.
      setPassword('')
      onUnlocked(vault.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      className="scrim"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && !busy) onCancel()
      }}
    >
      <form
        className="unlock-card glass"
        role="dialog"
        aria-modal="true"
        aria-labelledby="unlock-title"
        onSubmit={(e) => {
          e.preventDefault()
          if (password) void submit()
        }}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && !busy) onCancel()
        }}
      >
        <div className="unlock-head">
          <span className="app-mark">
            <Icon name="shield" size={32} />
          </span>
          <div>
            <h2 id="unlock-title">Unlock “{vault.name}”</h2>
            <p>
              {inBrowser
                ? 'Decrypted in this browser — the password never leaves it.'
                : 'Decrypted in memory only — nothing is written to disk.'}
            </p>
          </div>
        </div>

        <input
          className="field"
          type="password"
          autoFocus
          autoComplete="current-password"
          placeholder="Password"
          aria-label="Vault password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
        />
        {error && <p className="unlock-error">{error}</p>}

        <div className="unlock-buttons">
          <button className="pill tall" type="button" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button className="pill tall prominent" type="submit" disabled={busy || !password}>
            {busy ? 'Unlocking…' : 'Unlock'}
          </button>
        </div>
      </form>
    </div>
  )
}
