// File-type rules shared by the page and the service worker, mirroring
// core/vault/formats.go so both server modes classify files the same way.

import type { MediaType } from './api'

const IMAGE = new Set([
  'jpg', 'jpeg', 'png', 'heic', 'heif', 'tiff', 'tif', 'bmp', 'gif', 'webp',
  'raw', 'cr2', 'cr3', 'nef', 'arw', 'dng', 'orf', 'rw2', 'pef', 'srw', 'raf',
])
const VIDEO = new Set(['mp4', 'mov', 'm4v', 'avi', 'mkv', 'webm'])

export function extension(name: string): string {
  const dot = name.lastIndexOf('.')
  return dot < 0 ? '' : name.slice(dot + 1).toLowerCase()
}

export function mediaType(name: string): MediaType {
  const ext = extension(name)
  if (IMAGE.has(ext)) return 'image'
  if (VIDEO.has(ext)) return 'video'
  return ''
}

const CONTENT_TYPES: Record<string, string> = {
  jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif',
  webp: 'image/webp', bmp: 'image/bmp', tif: 'image/tiff', tiff: 'image/tiff',
  heic: 'image/heic', heif: 'image/heif', dng: 'image/x-adobe-dng',
  cr2: 'image/x-dcraw', cr3: 'image/x-dcraw', nef: 'image/x-dcraw', arw: 'image/x-dcraw',
  orf: 'image/x-dcraw', rw2: 'image/x-dcraw', pef: 'image/x-dcraw', srw: 'image/x-dcraw',
  raf: 'image/x-dcraw', raw: 'image/x-dcraw',
  mp4: 'video/mp4', m4v: 'video/mp4', mov: 'video/quicktime', webm: 'video/webm',
  mkv: 'video/x-matroska', avi: 'video/x-msvideo',
}

export function contentType(name: string): string {
  return CONTENT_TYPES[extension(name)] ?? 'application/octet-stream'
}
