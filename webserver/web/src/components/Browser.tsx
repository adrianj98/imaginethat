import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry, Listing, Vault } from '../api'
import Thumbnail from './Thumbnail'
import Lightbox from './Lightbox'

interface Props {
  vault: Vault
  onSessionLost: () => void
}

interface UploadState {
  name: string
  fraction: number
}

/** Directory browser: breadcrumb, folders, media grid, and write actions. */
export default function Browser({ vault, onSessionLost }: Props) {
  const [path, setPath] = useState('')
  const [listing, setListing] = useState<Listing | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [viewerIndex, setViewerIndex] = useState<number | null>(null)
  const [uploads, setUploads] = useState<UploadState[]>([])
  const [dragging, setDragging] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)

  const load = useCallback(
    async (target: string) => {
      setError(null)
      try {
        setListing(await api.listDir(vault.id, target))
      } catch (e) {
        if (e instanceof api.ApiError && e.status === 401) {
          onSessionLost()
          return
        }
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [vault.id, onSessionLost],
  )

  useEffect(() => {
    void load(path)
  }, [load, path])

  const entries = listing?.entries ?? []
  // The lightbox steps through viewable media only, skipping folders and
  // files the browser cannot render.
  const viewable = entries.filter((e) => !e.isDir && e.mediaType !== '')

  async function refresh() {
    await load(path)
  }

  async function handleFiles(files: FileList | File[]) {
    const list = Array.from(files)
    for (const file of list) {
      setUploads((u) => [...u, { name: file.name, fraction: 0 }])
      try {
        await api.upload(vault.id, path, file, (fraction) => {
          setUploads((u) => u.map((x) => (x.name === file.name ? { ...x, fraction } : x)))
        })
      } catch (e) {
        setError(`${file.name}: ${e instanceof Error ? e.message : String(e)}`)
      } finally {
        setUploads((u) => u.filter((x) => x.name !== file.name))
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
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const crumbs = path ? path.split('/') : []

  return (
    <div
      className={`browser${dragging ? ' dragging' : ''}`}
      onDragOver={(e) => {
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragging(false)
      }}
      onDrop={(e) => {
        e.preventDefault()
        setDragging(false)
        if (e.dataTransfer.files.length) void handleFiles(e.dataTransfer.files)
      }}
    >
      <div className="toolbar">
        <nav className="breadcrumb" aria-label="Breadcrumb">
          <button className="crumb" type="button" onClick={() => setPath('')}>
            {vault.name}
          </button>
          {crumbs.map((part, i) => (
            <span key={i}>
              <span className="crumb-sep">/</span>
              <button
                className="crumb"
                type="button"
                onClick={() => setPath(crumbs.slice(0, i + 1).join('/'))}
              >
                {part}
              </button>
            </span>
          ))}
        </nav>
        <div className="toolbar-actions">
          <button className="btn" type="button" onClick={() => void handleNewFolder()}>
            New folder
          </button>
          <button className="btn" type="button" onClick={() => fileInput.current?.click()}>
            Upload
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
      </div>

      {error && <div className="banner error">{error}</div>}

      {uploads.length > 0 && (
        <div className="uploads">
          {uploads.map((u) => (
            <div key={u.name} className="upload-row">
              <span className="upload-name">{u.name}</span>
              <progress value={u.fraction} max={1} />
              <span className="muted">{Math.round(u.fraction * 100)}%</span>
            </div>
          ))}
        </div>
      )}

      {listing === null ? (
        <p className="muted centered">Loading…</p>
      ) : entries.length === 0 ? (
        <p className="muted centered">This folder is empty. Drop files here to upload.</p>
      ) : (
        <div className="grid">
          {entries.map((entry) => (
            <div key={entry.name} className={`tile${entry.isDir ? ' folder' : ''}`}>
              <button
                className="tile-main"
                type="button"
                onClick={() => {
                  if (entry.isDir) {
                    setPath(api.joinPath(path, entry.name))
                  } else {
                    const idx = viewable.findIndex((v) => v.name === entry.name)
                    if (idx >= 0) setViewerIndex(idx)
                    else window.open(api.fileUrl(vault.id, api.joinPath(path, entry.name)), '_blank')
                  }
                }}
                title={entry.name}
              >
                <Thumbnail vaultId={vault.id} dir={path} entry={entry} />
                <span className="tile-name">{entry.name}</span>
                {!entry.isDir && <span className="tile-size">{api.formatSize(entry.size)}</span>}
              </button>
              <div className="tile-actions">
                <button
                  className="icon-btn"
                  type="button"
                  title="Rename"
                  onClick={() => void handleRename(entry)}
                >
                  ✎
                </button>
                {!entry.isDir && (
                  <a
                    className="icon-btn"
                    title="Download"
                    href={api.fileUrl(vault.id, api.joinPath(path, entry.name), true)}
                  >
                    ↓
                  </a>
                )}
                <button
                  className="icon-btn danger"
                  type="button"
                  title="Delete"
                  onClick={() => void handleDelete(entry)}
                >
                  ✕
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {viewerIndex !== null && viewable[viewerIndex] && (
        <Lightbox
          vaultId={vault.id}
          dir={path}
          items={viewable}
          index={viewerIndex}
          onIndexChange={setViewerIndex}
          onClose={() => setViewerIndex(null)}
        />
      )}
    </div>
  )
}
