import { useState } from 'react'
import * as api from '../api'
import Icon from './Icon'

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
    <div className="home">
      <form
        className="unlock-card glass"
        aria-labelledby="login-title"
        onSubmit={(e) => {
          e.preventDefault()
          if (password) void submit()
        }}
      >
        <div className="unlock-head">
          <span className="app-mark">
            <Icon name="shield" size={32} />
          </span>
          <div>
            <h2 id="login-title">Log in to this server</h2>
            <p>
              This server only stores encrypted vaults. They are unlocked and decrypted in your
              browser; their passwords are never sent to the server.
            </p>
          </div>
        </div>
        <input
          className="field"
          type="password"
          autoFocus
          autoComplete="current-password"
          placeholder="Server access password"
          aria-label="Server access password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
        />
        {error && <p className="unlock-error">{error}</p>}
        <button className="pill tall prominent" type="submit" disabled={busy || !password}>
          {busy ? 'Logging in…' : 'Log in'}
        </button>
      </form>
    </div>
  )
}
