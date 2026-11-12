/**
 * The browser mirror of internal/reply and voice.normalise.clean_reply.
 *
 * Server deltas are already cleaned, but the store cleans again so a stale
 * server (or a test double) cannot put a STATUS line on screen. The streaming
 * form holds an ambiguous tail: "--" is not painted until the next chunk says
 * whether it is an em dash or a CLI flag.
 */

const STATUS_LINE = new RegExp(
  '^[ \\t>*_`#]*STATUS:[ \\t]*(?:RUNNING|DONE|BLOCKED|CONTINUE)\\b[^\\n]*(?:\\r?\\n|$)',
  'gim',
)
const ARTIFACT_LINE = /^[ \t]*\[\[\s*artifact\s*:\s*(.+?)\s*\]\][ \t]*\r?\n?/gm

function sentinel(index: number): string {
  return `\u0000R${index}\u0001`
}

function maskInline(line: string, base: number): { text: string; spans: string[] } {
  let out = ''
  const spans: string[] = []
  for (let i = 0; i < line.length; ) {
    if (line[i] !== '`') {
      out += line[i]
      i += 1
      continue
    }
    let run = 0
    while (i + run < line.length && line[i + run] === '`') run += 1
    const close = findBacktickRun(line, i + run, run)
    if (close < 0) {
      out += line.slice(i, i + run)
      i += run
      continue
    }
    const end = close + run
    out += sentinel(base + spans.length)
    spans.push(line.slice(i, end))
    i = end
  }
  return { text: out, spans }
}

function findBacktickRun(text: string, start: number, width: number): number {
  for (let i = start; i < text.length; ) {
    if (text[i] !== '`') {
      i += 1
      continue
    }
    let run = 0
    while (i + run < text.length && text[i + run] === '`') run += 1
    if (run === width) return i
    i += run
  }
  return -1
}

function openingFence(line: string): { char: string; width: number } | null {
  const trimmed = line.replace(/^ +/, '')
  if (line.length - trimmed.length > 3 || trimmed.length < 3) return null
  const char = trimmed[0]
  if (char !== '`' && char !== '~') return null
  let width = 0
  while (width < trimmed.length && trimmed[width] === char) width += 1
  return width >= 3 ? { char, width } : null
}

function closingFence(line: string, char: string, width: number): boolean {
  const trimmed = line.replace(/^ +/, '')
  if (line.length - trimmed.length > 3 || trimmed.length < width) return false
  if (trimmed.slice(0, width) !== char.repeat(width)) return false
  return trimmed.slice(width).trim() === ''
}

function splitLines(text: string): string[] {
  return text.match(/[^\n]*\n|[^\n]+$/g) ?? []
}

export function cleanReply(text: string): string {
  if (text === '') return ''
  const lines = splitLines(text)
  const out: string[] = []
  const spans: string[] = []
  for (let i = 0; i < lines.length; ) {
    const fence = openingFence(lines[i])
    if (fence) {
      let end = lines.length - 1
      for (let j = i + 1; j < lines.length; j += 1) {
        if (closingFence(lines[j], fence.char, fence.width)) {
          end = j
          break
        }
      }
      out.push(sentinel(spans.length))
      spans.push(lines.slice(i, end + 1).join(''))
      i = end + 1
      continue
    }
    const masked = maskInline(lines[i], spans.length)
    out.push(masked.text)
    spans.push(...masked.spans)
    i += 1
  }

  let masked = out.join('')
  masked = masked.replace(STATUS_LINE, '')
  masked = masked.replace(ARTIFACT_LINE, '')
  masked = replaceStandaloneDashes(masked)
  masked = removeRuleLines(masked)
  masked = masked.replace(/[ \t\r\n]+$/, '')
  for (let i = 0; i < spans.length; i += 1) {
    masked = masked.split(sentinel(i)).join(spans[i])
  }
  return masked
}

function replaceStandaloneDashes(text: string): string {
  if (!text.includes('--')) return text
  let out = ''
  for (let i = 0; i < text.length; ) {
    if (
      text.startsWith('--', i) &&
      (i === 0 || isSpace(text[i - 1])) &&
      (i + 2 === text.length || isSpace(text[i + 2]))
    ) {
      let end = i + 2
      while (end < text.length && text[end] === '-') end += 1
      if (end - i === 2) {
        out += '—'
        i = end
        continue
      }
    }
    out += text[i]
    i += 1
  }
  return out
}

function isSpace(value: string): boolean {
  return value === ' ' || value === '\t' || value === '\n' || value === '\r'
}

function isRuleLine(line: string): boolean {
  const stripped = line.replace(/[\r\n]+$/, '').trim()
  return stripped.length >= 3 && /^-+$/.test(stripped)
}

function removeRuleLines(text: string): string {
  const lines = splitLines(text)
  const out: string[] = []
  for (let i = 0; i < lines.length; i += 1) {
    if (isRuleLine(lines[i])) {
      if (i + 1 < lines.length && lines[i + 1].trim() === '') i += 1
      continue
    }
    out.push(lines[i])
  }
  return out.join('')
}

function inFenceAtEnd(text: string): boolean {
  let inFence = false
  let char = ''
  let width = 0
  for (const line of splitLines(text)) {
    if (!inFence) {
      const fence = openingFence(line)
      if (fence) {
        inFence = true
        char = fence.char
        width = fence.width
      }
      continue
    }
    if (closingFence(line, char, width)) inFence = false
  }
  return inFence
}

function isStatusCandidate(value: string): boolean {
  const lower = value.replace(/^[ \t>*_`#]+/, '').toLowerCase()
  return lower.startsWith('status:') || 'status:'.startsWith(lower)
}

function isArtifactCandidate(value: string): boolean {
  return value.replace(/^[ \t]+/, '').startsWith('[[')
}

function isRuleCandidate(value: string): boolean {
  const stripped = value.trim()
  return stripped !== '' && /^-+$/.test(stripped)
}

function cleanFragment(text: string): string {
  const masked = maskInline(text, 0)
  let out = replaceStandaloneDashes(masked.text)
  for (let i = 0; i < masked.spans.length; i += 1) {
    out = out.split(sentinel(i)).join(masked.spans[i])
  }
  return out
}

/** Streaming cleaner: prefix-stable, and never paints a held token early. */
export class ReplyStream {
  private raw = ''
  private sent = ''

  feed(delta: string): string {
    if (delta === '') return ''
    this.raw += delta
    return this.emit(this.candidate())
  }

  flush(): string {
    return this.emit(cleanReply(this.raw))
  }

  visible(): string {
    return this.sent
  }

  private emit(candidate: string): string {
    if (candidate === this.sent) return ''
    if (candidate.startsWith(this.sent)) {
      const out = candidate.slice(this.sent.length)
      this.sent = candidate
      return out
    }
    return ''
  }

  private candidate(): string {
    const raw = this.raw
    const lastNewline = raw.lastIndexOf('\n')
    const stableEnd = lastNewline + 1
    const stable = raw.slice(0, stableEnd)
    const tail = raw.slice(stableEnd)

    // Do not trim the stable prefix: its newline is the boundary with the tail.
    const prefix = cleanReply(stable === '' ? '' : stable + '\u0000KEEP')
      .replace(/\u0000KEEP$/, '')
    if (inFenceAtEnd(stable)) return prefix + tail
    if (tail === '') return prefix

    const left = tail.replace(/^[ \t]+/, '')
    if (isStatusCandidate(left) || isArtifactCandidate(left) || isRuleCandidate(left)) {
      return prefix
    }
    if (tail.includes('`')) return prefix

    let cut = tail.length
    while (cut > 0 && tail[cut - 1] === '-') cut -= 1
    return prefix + cleanFragment(tail.slice(0, cut))
  }
}
