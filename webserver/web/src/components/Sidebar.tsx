import type { Vault } from '../api'
import Icon from './Icon'

interface Props {
  vaults: Vault[]
  activeId: string
  onSelect: (vault: Vault) => void
  onHome: () => void
  onLock: (id: string) => void
  /** Client-decrypt servers only: end the server session. */
  onLogout?: () => void
}

/** Floating glass sidebar listing the server's vaults. */
export default function Sidebar({ vaults, activeId, onSelect, onHome, onLock, onLogout }: Props) {
  return (
    <aside className="sidebar glass card" aria-label="Vaults">
      <button className="sidebar-row sidebar-brand" type="button" onClick={onHome}>
        ImageThat
      </button>
      <h2 className="section-heading">Vaults</h2>
      <nav className="sidebar-list">
        {vaults.map((v) => (
          <div
            key={v.id}
            role="button"
            tabIndex={0}
            aria-current={v.id === activeId ? 'page' : undefined}
            className={`sidebar-row${v.id === activeId ? ' active' : ''}`}
            onClick={() => onSelect(v)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onSelect(v)
              }
            }}
          >
            <Icon name={v.unlocked ? 'unlock' : 'lock'} />
            <span className="name">{v.name}</span>
            {v.unlocked && (
              <button
                className="icon-btn"
                type="button"
                title="Lock"
                aria-label={`Lock ${v.name}`}
                onClick={(e) => {
                  e.stopPropagation()
                  onLock(v.id)
                }}
              >
                <Icon name="lock" size={13} />
              </button>
            )}
          </div>
        ))}
      </nav>
      <p className="footnote">
        <Icon name="lock" size={12} />
        {onLogout ? 'Decrypted in this browser' : 'Decrypted in memory only'}
      </p>
      {onLogout && (
        <button className="pill small sidebar-logout" type="button" onClick={onLogout}>
          Log out
        </button>
      )}
    </aside>
  )
}
