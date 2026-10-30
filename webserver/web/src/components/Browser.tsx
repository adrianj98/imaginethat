import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry, Listing, Vault } from '../api'
import type { Backend } from '../backend'
import Thumbnail from './Thumbnail'
import Lightbox from './Lightbox'
import InfoCard from './InfoCard'
import Icon from './Icon'

interface Props {
  backend: Backend
  vault: Vault
  /** Current folder within the vault; owned by the URL, not this component. */
  path: string
  /** Requests navigation to another folder (updates the URL). */
  onNavigate: (path: string) => void
  /** Back to the vault list (the Up button at the vault root). */
  onLeaveVault: () => void
  onLock: () => void
  sidebarOpen: boolean
  onToggleSidebar: () => void
  onSessionLost: () => void
}

interface UploadState {
  // id, not name: two uploads of the same file name can be in flight at once
  // (two drops, or a drop of same-named files from different folders), and
  // keying progress rows on the name makes them collide.
  id: number
  name: string
  fraction: number
}

let nextUploadID = 0

const INFO_KEY = 'imagethat.info'

function initialShowInfo(): boolean {
  try {
    return localStorage.getItem(INFO_KEY) !== '0'
  } catch {
    return true
  }
}

/** True while the window is wide enough for the fixed info card. */
function useWide(): boolean {
  const query = '(min-width: 801px)'
  const [wide, setWide] = useState(() => window.matchMedia(query).matches)
  useEffect(() => {
    const mq = window.matchMedia(query)
    const onChange = () => setWide(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return wide
}

/** Directory browser: breadcrumb, folders, media grid, and write actions. */
export default function Browser({
  backend,
  vault,
  path,
  onNavigate,
  onLeaveVault,
  onLock,
  sidebarOpen,
  onToggleSidebar,
  onSessionLost,
}: Props) {
  const [listing, setListing] = useState<Listing | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [viewerIndex, setViewerIndex] = useState<number | null>(null)
  const [uploads, setUploads] = useState<UploadState[]>([])
  const [dragging, setDragging] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [showInfo, setShowInfo] = useState(initialShowInfo)
  const wide = useWide()
  const fileInput = useRef<HTMLInputElement>(null)

  const load = useCallback(
    async (target: string) => {
      setError(null)
      try {
        setListing(await backend.listDir(vault.id, target))
      } catch (e) {
        if (e instanceof api.ApiError && e.status === 401) {
          onSessionLost()
          return
        }
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [backend, vault.id, onSessionLost],
  )

  useEffect(() => {
    void load(path)
    // The viewer indexes into the previous folder's entries; navigation
    // (including the browser's back button) must not carry it over.
    setViewerIndex(null)
    setSelected(null)
    setSearch('')
  }, [load, path])

  const allEntries = listing?.entries ?? []
  const query = search.trim().toLowerCase()
  const entries = query
    ? allEntries.filter((e) => e.name.toLowerCase().includes(query))
    : allEntries
  // The lightbox steps through viewable media only, skipping folders and
  // files the browser cannot render.
  const viewable = entries.filter((e) => !e.isDir && e.mediaType !== '')
  const selectedEntry = entries.find((e) => e.name === selected) ?? null
  // With the info card showing, a click selects and a double-click opens,
  // as on the Mac; on narrow screens a tap opens straight away.
  const clickSelects = wide && showInfo

  function toggleInfo() {
    setShowInfo((on) => {
      try {
        localStorage.setItem(INFO_KEY, on ? '0' : '1')
      } catch {
        // Not remembered; harmless.
      }
      return !on
    })
  }

  function open(entry: Entry) {
    if (entry.isDir) {
      onNavigate(api.joinPath(path, entry.name))
      return
    }
    const idx = viewable.findIndex((v) => v.name === entry.name)
    if (idx >= 0) {
      setSelected(entry.name)
      setViewerIndex(idx)
    } else window.open(backend.fileUrl(vault.id, path, entry), '_blank')
  }

  async function refresh() {
    await load(path)
  }

  async function handleFiles(files: FileList | File[]) {
    const list = Array.from(files)
    for (const file of list) {
      const id = nextUploadID++
      setUploads((u) => [...u, { id, name: file.name, fraction: 0 }])
      try {
        await api.upload(vault.id, path, file, (fraction) => {
          setUploads((u) => u.map((x) => (x.id === id ? { ...x, fraction } : x)))
        })
      } catch (e) {
        setError(`${file.name}: ${e instanceof Error ? e.message : String(e)}`)
      } finally {
        setUploads((u) => u.filter((x) => x.id !== id))
      }
    }
    await refresh()
  }

  async function handleNewFolder() {
    const name = window.prompt('New folder name')
    if (!name) return
    try {
      await api.mkdir(vault.id, api.joinPath(path, name))
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  async function handleRename(entry: Entry) {
    const name = window.prompt(`Rename “${entry.name}” to`, entry.name)
    if (!name || name === entry.name) return
    try {
      await api.move(vault.id, api.joinPath(path, entry.name), api.joinPath(path, name))
      if (selected === entry.name) setSelected(name)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  async function handleDelete(entry: Entry) {
    const what = entry.isDir ? 'folder and everything in it' : 'file'
    if (!window.confirm(`Delete the ${what} “${entry.name}”? This cannot be undone.`)) return
    try {
      await api.remove(vault.id, api.joinPath(path, entry.name))
      if (selected === entry.name) setSelected(null)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const crumbs = path ? path.split('/') : []
  const title = crumbs.length ? crumbs[crumbs.length - 1] : vault.name
  const parentPath = crumbs.length ? crumbs.slice(0, -1).join('/') : null
  const parentName = crumbs.length > 1 ? crumbs[crumbs.length - 2] : crumbs.length ? vault.name : null
  const infoVisible = wide && showInfo

  return (
    <div
      className={`browser${dragging ? ' dragging' : ''}${infoVisible ? ' with-info' : ''}`}
      onDragOver={(e) => {
        if (!backend.canWrite) return
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragging(false)
      }}
      onDrop={(e) => {
        if (!backend.canWrite) return
        e.preventDefault()
        setDragging(false)
        if (e.dataTransfer.files.length) void handleFiles(e.dataTransfer.files)
      }}
      onKeyDown={(e) => {
        if (viewerIndex !== null || (e.target as HTMLElement).tagName === 'INPUT') return
        if (e.key === 'Enter' && selectedEntry) open(selectedEntry)
        if (e.key === 'Escape') setSelected(null)
      }}
    >
      <header className="toolbar">
        <div className="capsule glass">
          <button
            className="icon-btn"
            type="button"
            title={sidebarOpen ? 'Hide Sidebar' : 'Show Sidebar'}
            aria-label={sidebarOpen ? 'Hide Sidebar' : 'Show Sidebar'}
            onClick={onToggleSidebar}
          >
            <Icon name="sidebar-left" />
          </button>
        </div>

        <div className="capsule glass">
          <button
            className="icon-btn"
            type="button"
            title={parentPath === null ? 'Back to Vaults' : 'Go Up'}
            aria-label={parentPath === null ? 'Back to Vaults' : 'Go Up'}
            onClick={() => (parentPath === null ? onLeaveVault() : onNavigate(parentPath))}
          >
            <Icon name="chevron-left" />
          </button>
        </div>

        <nav className="capsule text glass crumbs" aria-label="Breadcrumb">
          {parentName !== null && parentPath !== null && (
            <>
              <button className="parent" type="button" onClick={() => onNavigate(parentPath)}>
                {parentName}
              </button>
              <span className="sep">
                <Icon name="chevron-right" size={10} />
              </span>
            </>
          )}
          <span className="title">{title}</span>
          <span className="count">
            {listing ? (allEntries.length === 1 ? '1 item' : `${allEntries.length} items`) : ''}
          </span>
        </nav>

        <div className="spacer" />

        <label className="capsule glass search">
          <Icon name="search" size={13} />
          <input
            type="search"
            placeholder="Search"
            aria-label="Search this folder"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>

        <div className="capsule glass">
          {backend.canWrite && (
            <>
              <button
                className="icon-btn"
                type="button"
                title="Upload"
                aria-label="Upload"
                onClick={() => fileInput.current?.click()}
              >
                <Icon name="upload" />
              </button>
              <button
                className="icon-btn"
                type="button"
                title="New Folder"
                aria-label="New Folder"
                onClick={() => void handleNewFolder()}
              >
                <Icon name="folder-plus" />
              </button>
            </>
          )}
          {wide && (
            <button
              className={`icon-btn${showInfo ? ' on' : ''}`}
              type="button"
              title={showInfo ? 'Hide Info' : 'Show Info'}
              aria-label={showInfo ? 'Hide Info' : 'Show Info'}
              aria-pressed={showInfo}
              onClick={toggleInfo}
            >
              <Icon name="sidebar-right" />
            </button>
          )}
          <button
            className="icon-btn"
            type="button"
            title="Lock Vault"
            aria-label="Lock Vault"
            onClick={onLock}
          >
            <Icon name="lock" />
          </button>
          <input
            ref={fileInput}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              if (e.target.files?.length) void handleFiles(e.target.files)
              e.target.value = ''
            }}
          />
        </div>
      </header>

      {error && (
        <div className="banner glass inline">
          <Icon name="warning" />
          {error}
        </div>
      )}

      {listing === null ? (
        <p className="muted centered">Loading…</p>
      ) : entries.length === 0 ? (
        <div className="empty-state">
          <Icon name={query ? 'search' : 'photo'} size={44} />
          {query
            ? `Nothing matches “${search.trim()}”`
            : `This folder is empty.${backend.canWrite ? ' Drop files here to upload.' : ''}`}
        </div>
      ) : (
        <div className="grid" role="listbox" aria-label={title}>
          {/* Tiles are keyed on the full path, not the bare name: keying on the
              name lets React reuse a tile (and its Thumbnail's loaded/failed
              state) for a same-named entry in a different folder. */}
          {entries.map((entry) => {
            const isSelected = entry.name === selected
            return (
              <div
                key={api.joinPath(path, entry.name)}
                className={`tile${entry.isDir ? ' folder' : ''}${isSelected ? ' selected' : ''}`}
                role="option"
                aria-selected={isSelected}
                tabIndex={0}
                title={entry.name}
                onClick={() => {
                  if (entry.isDir || !clickSelects) open(entry)
                  else setSelected(entry.name)
                }}
                onDoubleClick={() => {
                  if (!entry.isDir && clickSelects) open(entry)
                }}
                onKeyDown={(e) => {
                  if (e.key === ' ' && clickSelects && !entry.isDir) {
                    e.preventDefault()
                    setSelected(entry.name)
                  } else if (e.key === 'Enter') {
                    e.stopPropagation()
                    open(entry)
                  }
                }}
              >
                <Thumbnail backend={backend} vaultId={vault.id} dir={path} entry={entry} />
                {entry.mediaType === 'video' && (
                  <span className="badge play glass">
                    <Icon name="play" size={9} />
                  </span>
                )}
                {isSelected && (
                  <span className="badge check">
                    <Icon name="check" size={11} />
                  </span>
                )}
                {!entry.isDir && <span className="tile-caption">{entry.name}</span>}
                <div className="tile-actions capsule glass" onClick={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()}>
                  {backend.canWrite && (
                    <button
                      className="icon-btn"
                      type="button"
                      title="Rename"
                      aria-label={`Rename ${entry.name}`}
                      onClick={() => void handleRename(entry)}
                    >
                      <Icon name="pencil" size={13} />
                    </button>
                  )}
                  {!entry.isDir && (
                    <a
                      className="icon-btn"
                      title="Download"
                      aria-label={`Download ${entry.name}`}
                      href={backend.fileUrl(vault.id, path, entry, true)}
                    >
                      <Icon name="download" size={13} />
                    </a>
                  )}
                  {backend.canWrite && (
                    <button
                      className="icon-btn danger"
                      type="button"
                      title="Delete"
                      aria-label={`Delete ${entry.name}`}
                      onClick={() => void handleDelete(entry)}
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}

      {infoVisible && (
        <InfoCard
          backend={backend}
          vaultId={vault.id}
          dir={path}
          entry={selectedEntry}
          folderName={title}
          itemCount={allEntries.length}
          onRename={backend.canWrite ? (e) => void handleRename(e) : undefined}
          onDelete={backend.canWrite ? (e) => void handleDelete(e) : undefined}
        />
      )}

      {uploads.length > 0 && (
        <div className="uploads glass card" role="status">
          {uploads.map((u) => (
            <div key={u.id} className="upload-row">
              <span className="upload-name">{u.name}</span>
              <span className="muted">{Math.round(u.fraction * 100)}%</span>
              <span className="bar">
                <span style={{ width: `${u.fraction * 100}%` }} />
              </span>
            </div>
          ))}
        </div>
      )}

      {viewerIndex !== null && viewable[viewerIndex] && (
        <Lightbox
          backend={backend}
          vaultId={vault.id}
          dir={path}
          folderName={title}
          items={viewable}
          index={viewerIndex}
          onIndexChange={(i) => {
            setViewerIndex(i)
            // Coming back to the grid lands on the item last viewed.
            setSelected(viewable[i]?.name ?? null)
          }}
          onClose={() => setViewerIndex(null)}
        />
      )}
    </div>
  )
}
