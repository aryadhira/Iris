import type { FileAttachment } from './types'

export type FileKind = 'image' | 'pdf' | 'doc' | 'sheet' | 'archive' | 'text' | 'other'

/** Indonesian human size, matching the server's attachment line. */
export function formatBytes(size: number): string {
  if (!Number.isFinite(size) || size < 0) return '0 B'
  if (size < 1024) return `${Math.round(size)} B`
  const kb = size / 1024
  if (kb < 1024) {
    return `${kb < 10 ? kb.toFixed(1).replace('.', ',') : Math.round(kb)} KB`
  }
  const mb = kb / 1024
  return `${mb < 100 ? mb.toFixed(1).replace('.', ',') : Math.round(mb)} MB`
}

export function fileKind(mime: string, name = ''): FileKind {
  if (mime.startsWith('image/')) return 'image'
  if (mime === 'application/pdf') return 'pdf'
  if (mime.includes('wordprocessingml')) return 'doc'
  if (mime.includes('spreadsheetml')) return 'sheet'
  if (mime === 'application/zip') return 'archive'
  if (mime.startsWith('text/') || mime === 'application/json') return 'text'
  const ext = name.toLowerCase().split('.').pop() ?? ''
  if (['doc', 'docx', 'odt'].includes(ext)) return 'doc'
  if (['xls', 'xlsx', 'ods', 'csv'].includes(ext)) return 'sheet'
  if (['zip', 'tar', 'gz', 'rar', '7z'].includes(ext)) return 'archive'
  if (['md', 'txt', 'log', 'json'].includes(ext)) return 'text'
  return 'other'
}

export function fileTypeLabel(mime: string, name = ''): string {
  switch (fileKind(mime, name)) {
    case 'image':
      return (name.split('.').pop() ?? 'Gambar').toUpperCase()
    case 'pdf':
      return 'PDF'
    case 'doc':
      return 'DOCX'
    case 'sheet':
      return 'XLSX'
    case 'archive':
      return 'ZIP'
    case 'text':
      return mime === 'application/json' ? 'JSON' : 'Teks'
    default:
      return (name.split('.').pop() ?? 'Berkas').toUpperCase()
  }
}

export function fileUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}`
}

export function isImage(file: Pick<FileAttachment, 'mime'>): boolean {
  return file.mime.startsWith('image/')
}

export interface ArtifactCardModel {
  kind: FileKind
  title: string
  meta: string
  downloadUrl: string
}

/** The single presentation rule shared by the artifact card and its tests. */
export function artifactCard(file: FileAttachment): ArtifactCardModel {
  return {
    kind: fileKind(file.mime, file.name),
    title: file.name,
    meta: `${fileTypeLabel(file.mime, file.name)} · ${formatBytes(file.size)}`,
    downloadUrl: fileUrl(file.id),
  }
}
