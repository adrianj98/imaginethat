import type { Vault } from '../api'
import Icon from './Icon'

interface Props {
  vaults: Vault[] | null
  /** Open an unlocked vault, or ask for the password of a locked one. */
  onSelect: (vault: Vault) => void
  onLock: (id: string) => void
}

/** Vault picker shown when no vault is open: a grouped card of vaults. */
export default function VaultList({ vaults, onSelect, onLock }: Props) {
  return (
    <div className="home">
      <div className="home-brand">
        <span className="app-mark">
          <Icon name="photo" size={32} />
        </span>
        <h1>ImageThat</h1>
      </div>

      {vaults === null ? (
        <p className="muted">Loading vaults…</p>
      ) : vaults.length === 0 ? (
        <div className="home-empty glass card">
          <h2>No vaults found</h2>
          <p>
            The server did not find any directory containing a <code>gocryptfs.conf</code> under
            its configured root. Start it with <code>-root</code> pointing at your vaults.
          </p>
        </div>
      ) : (
        <div className="vault-card glass card">
          <h2 className="section-heading">Vaults</h2>
          {vaults.map((v) => (
            <div
              key={v.id}
              role="button"
              tabIndex={0}
              className={`vault-row${v.unlocked ? ' unlocked' : ''}`}
              onClick={() => onSelect(v)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onSelect(v)
                }
              }}
            >
              <Icon name={v.unlocked ? 'unlock' : 'lock'} size={18} />
              <span className="name">{v.name}</span>
              {v.unlocked ? (
                <button
                  className="pill small"
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation()
                    onLock(v.id)
                  }}
                >
                  Lock
                </button>
              ) : (
                <span className="state">Locked</span>
              )}
              <span className="chevron">
                <Icon name="chevron-right" size={14} />
              </span>
            </div>
          ))}
        </div>
      )}

      <p className="footnote">
        <Icon name="lock" size={12} />
        Vaults are decrypted on the server, in memory only.
      </p>
    </div>
  )
}
