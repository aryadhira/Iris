/**
 * A small, spec-shaped SSE parser.
 *
 * `EventSource` cannot POST, so the room uses fetch + ReadableStream and this
 * parser. It is deliberately tolerant of chunk boundaries: a chunk may end in
 * the middle of a field name, a data line, or a CRLF pair.
 */

export interface SseFrame {
  event: string
  data: string
  id?: string
}

export class SseParser {
  private buffer = ''
  private dataLines: string[] = []
  private eventName = ''
  private idValue: string | undefined

  /** Feed one decoded chunk. Returns every frame that completed in it. */
  feed(chunk: string): SseFrame[] {
    this.buffer += chunk
    const frames: SseFrame[] = []
    let newline = this.buffer.indexOf('\n')

    while (newline !== -1) {
      let line = this.buffer.slice(0, newline)
      this.buffer = this.buffer.slice(newline + 1)
      if (line.endsWith('\r')) line = line.slice(0, -1)

      if (line === '') {
        this.dispatch(frames)
      } else if (line.startsWith(':')) {
        // Comment / keep-alive. Ignore.
      } else {
        const colon = line.indexOf(':')
        const field = colon === -1 ? line : line.slice(0, colon)
        let value = colon === -1 ? '' : line.slice(colon + 1)
        if (value.startsWith(' ')) value = value.slice(1)

        switch (field) {
          case 'event':
            this.eventName = value
            break
          case 'data':
            this.dataLines.push(value)
            break
          case 'id':
            this.idValue = value
            break
          case 'retry':
            // No reconnect policy is delegated to the browser here.
            break
          default:
            break
        }
      }

      newline = this.buffer.indexOf('\n')
    }

    return frames
  }

  /**
   * Flush a stream that ended without its final blank line. Lenient on
   * purpose: a dropped connection often truncates the last frame.
   */
  end(): SseFrame[] {
    const frames: SseFrame[] = []
    if (this.buffer.length > 0) {
      const line = this.buffer.endsWith('\r') ? this.buffer.slice(0, -1) : this.buffer
      this.buffer = ''
      if (line.startsWith('data:')) {
        let value = line.slice(5)
        if (value.startsWith(' ')) value = value.slice(1)
        this.dataLines.push(value)
      }
    }
    this.dispatch(frames)
    return frames
  }

  private dispatch(frames: SseFrame[]): void {
    if (this.dataLines.length > 0) {
      frames.push({
        event: this.eventName === '' ? 'message' : this.eventName,
        data: this.dataLines.join('\n'),
        id: this.idValue,
      })
    }
    this.dataLines = []
    this.eventName = ''
  }
}

/** Parse a frame's JSON payload, returning null instead of throwing. */
export function frameJson<T>(frame: SseFrame): T | null {
  try {
    return JSON.parse(frame.data) as T
  } catch {
    return null
  }
}
