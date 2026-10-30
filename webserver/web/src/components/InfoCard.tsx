import * as api from '../api'
import type { Entry } from '../api'
import Icon from './Icon'

interface Props {
  vaultId: string
  dir: string
  /** The selected item; null shows the folder summary instead. */
  entry: Entry | null
  folderName: string
  itemCount: number
  /** Pixel size, when the caller has decoded the image. */
  dimensions?: { width: number; height: number } | null
  /** Thumbnail at the top (grid only; the viewer already shows the image). */
  showPreview?: boolean
  onRename?: (entry: Entry) => void
  onDelete?: (entry: Entry) => void
}

/** "JPEG", "QuickTime", … — the same names ItemFacts.kind gives on the Mac. */
export function kindOf(entry: Entry): string {
  if (entry.isDir) return 'Folder'
  const ext = entry.name.includes('.') ? entry.name.split('.').pop()!.toLowerCase() : ''
  switch (ext) {
    case 'mov':
      return 'QuickTime'
    case 'mp4':
    case 'm4v':
      return 'MPEG-4'
    case 'jpg':
    case 'jpeg':
      return 'JPEG'
    case '':
      return entry.mediaType === 'video' ? 'Video' : entry.mediaType === 'image' ? 'Image' : 'File'
    default:
      return ext.toUpperCase()
  }
}

/** Floating info card (the macOS ItemInfoCard): preview, name, facts, actions. */
export default function InfoCard({
  vaultId,
  dir,
  entry,
  folderName,
  itemCount,
  dimensions,
  showPreview = true,
  onRename,
  onDelete,
}: Props) {
  if (!entry) {
    return (
      <aside className="info-card glass card" aria-label="Info">
        <div className="info-preview">
          <Icon name="folder" size={40} />
        </div>
        <div className="info-title">
          <strong>{folderName}</strong>
          <span>{itemCount === 1 ? '1 item' : `${itemCount} items`}</span>
        </div>
        <p className="info-hint">
          Select an item to see its details. Double-click to open it.
        </p>
        <div className="grow" />
        <Footnote />
      </aside>
    )
  }

  const path = api.joinPath(dir, entry.name)
  const summary = [
    kindOf(entry),
    dimensions ? `${dimensions.width} × ${dimensions.height}` : '',
    entry.isDir ? '' : api.formatSize(entry.size),
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <aside className="info-card glass card" aria-label="Info">
      {showPreview && (
        <div className="info-preview">
          {api.hasThumbnail(entry.name) || entry.mediaType === 'video' ? (
            <img src={api.thumbUrl(vaultId, path)} alt="" decoding="async" />
          ) : (
            <Icon name={entry.isDir ? 'folder' : 'doc'} size={40} />
          )}
        </div>
      )}

      <div className="info-title">
        <strong title={entry.name}>{entry.name}</strong>
        <span>{summary}</span>
      </div>

      <dl className="facts">
        <dt>Folder</dt>
        <dd>{folderName}</dd>
      </dl>

      <div className="info-actions">
        {!entry.isDir && (
          <a className="pill" href={api.fileUrl(vaultId, path, true)}>
            <Icon name="download" size={14} />
            Download
          </a>
        )}
        {onRename && (
          <button className="pill" type="button" onClick={() => onRename(entry)}>
            Rename
          </button>
        )}
        {onDelete && (
          <button className="pill danger" type="button" onClick={() => onDelete(entry)}>
            Delete
          </button>
        )}
      </div>

      <div className="grow" />
      <Footnote />
    </aside>
  )
}

function Footnote() {
  return (
    <p className="footnote">
      <Icon name="lock" size={12} />
      Decrypted in memory only
    </p>
  )
}
