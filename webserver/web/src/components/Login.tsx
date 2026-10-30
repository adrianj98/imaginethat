import { useState } from 'react'
import * as api from '../api'

interface Props {
  onLoggedIn: () => void
}

/**
 * Server access password, for servers in client-decrypt mode. It only lets
 * this browser fetch encrypted files; each vault's own password is entered
 * later and never leaves the browser.
 */
export default function Login({ onLoggedIn }: Props) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      await api.login(password)
      setPassword('')
      onLoggedIn()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="vault-list">
      <h2>Log in to this server</h2>
      <p className="muted">
        This server only stores encrypted vaults. Vaults are unlocked and decrypted in your browser;
        their passwords are never sent to the server.
      </p>
      <form
        className="unlock-form"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <input
          type="password"
          autoFocus
          autoComplete="current-password"
          placeholder="Server access password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
        />
        <button className="btn primary" type="submit" disabled={busy || !password}>
          {busy ? 'Logging in…' : 'Log in'}
        </button>
        {error && <span className="error-text">{error}</span>}
      </form>
    </div>
  )
}
