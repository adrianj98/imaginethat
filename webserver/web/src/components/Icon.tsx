// Line icons standing in for the SF Symbols the macOS app uses, drawn on a
// 24px grid with a 1.8 stroke so they sit at the same weight.
const paths = {
  'chevron-left': 'M15 5l-7 7 7 7',
  'chevron-right': 'M9 5l7 7-7 7',
  grid: 'M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z',
  'sidebar-left': 'M3 5h18v14H3zM9 5v14',
  'sidebar-right': 'M3 5h18v14H3zM15 5v14',
  search: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM21 21l-5-5',
  upload: 'M12 15V4M7 9l5-5 5 5M5 15v4h14v-4',
  download: 'M12 4v11M7 10l5 5 5-5M5 19h14',
  'folder-plus': 'M3 6h6l2 2h10v11H3zM12 11v6M9 14h6',
  folder: 'M3 6h6l2 2h10v11H3z',
  pencil: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
  trash: 'M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13',
  close: 'M6 6l12 12M18 6L6 18',
  lock: 'M6 11h12v9H6zM8 11V8a4 4 0 0 1 8 0v3',
  unlock: 'M6 11h12v9H6zM8 11V8a4 4 0 0 1 7.5-2',
  shield: 'M12 3l8 3v6c0 4.5-3.4 8-8 9-4.6-1-8-4.5-8-9V6zM9.5 12.5l2 2 3.5-4',
  minus: 'M6 12h12',
  plus: 'M12 6v12M6 12h12',
  play: 'M8 5v14l11-7z',
  photo: 'M3 5h18v14H3zM3 16l5-5 4 4 3-3 6 6M15.5 9.5a1 1 0 1 0 0-.01',
  film: 'M4 4h16v16H4zM8 4v16M16 4v16M4 8h4M4 12h4M4 16h4M16 8h4M16 12h4M16 16h4',
  doc: 'M6 3h8l4 4v14H6zM14 3v4h4',
  check: 'M5 12l5 5 9-10',
  warning: 'M12 4l9 16H3zM12 10v4M12 17v.01',
}

export type IconName = keyof typeof paths

export default function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg
      className="icon"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill={name === 'play' ? 'currentColor' : 'none'}
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  )
}
