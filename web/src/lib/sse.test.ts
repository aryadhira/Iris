import { describe, expect, it } from 'vitest'
import { frameJson, SseParser } from './sse'

describe('SseParser', () => {
  it('parses one frame in one chunk', () => {
    const parser = new SseParser()
    const frames = parser.feed('event: delta\ndata: {"text":"hi"}\n\n')
    expect(frames).toEqual([{ event: 'delta', data: '{"text":"hi"}', id: undefined }])
    expect(frameJson<{ text: string }>(frames[0])).toEqual({ text: 'hi' })
  })

  it('parses every split point of a frame', () => {
    const wire = 'event: delta\ndata: {"text":"p1 works"}\n\n'
    for (let cut = 1; cut < wire.length; cut += 1) {
      const parser = new SseParser()
      const frames = [...parser.feed(wire.slice(0, cut)), ...parser.feed(wire.slice(cut))]
      expect(frames, `cut at ${cut}`).toHaveLength(1)
      expect(frames[0].event).toBe('delta')
      expect(frameJson<{ text: string }>(frames[0])).toEqual({ text: 'p1 works' })
    }
  })

  it('parses a data line split one character at a time', () => {
    const parser = new SseParser()
    const wire = 'event: delta\ndata: {"text":"slow"}\n\n'
    const frames = []
    for (const char of wire) frames.push(...parser.feed(char))
    expect(frames).toHaveLength(1)
    expect(frameJson<{ text: string }>(frames[0])).toEqual({ text: 'slow' })
  })

  it('parses multiple events in a single chunk', () => {
    const parser = new SseParser()
    const frames = parser.feed(
      'event: delta\ndata: {"text":"a"}\n\nevent: tool\ndata: {"name":"_thinking","status":"running"}\n\n',
    )
    expect(frames.map((f) => f.event)).toEqual(['delta', 'tool'])
    expect(frameJson<{ name: string }>(frames[1])).toEqual({
      name: '_thinking',
      status: 'running',
    })
  })

  it('joins multiple data lines with a newline', () => {
    const parser = new SseParser()
    const frames = parser.feed('event: note\ndata: first\ndata: second\n\n')
    expect(frames[0].data).toBe('first\nsecond')
  })

  it('handles CRLF split across chunks', () => {
    const parser = new SseParser()
    const frames = [
      ...parser.feed('event: delta\r'),
      ...parser.feed('\ndata: {"text":"x"}\r'),
      ...parser.feed('\n\r\n'),
    ]
    expect(frames).toHaveLength(1)
    expect(frames[0].event).toBe('delta')
    expect(frameJson<{ text: string }>(frames[0])).toEqual({ text: 'x' })
  })

  it('ignores comments and unknown fields, and does not dispatch empty events', () => {
    const parser = new SseParser()
    expect(parser.feed(': keep-alive\n\n')).toEqual([])
    expect(parser.feed('retry: 3000\n\n')).toEqual([])
    expect(parser.feed('event: delta\n\n')).toEqual([])
    const frames = parser.feed('id: 7\ndata: plain\n\n')
    expect(frames).toEqual([{ event: 'message', data: 'plain', id: '7' }])
  })

  it('resets the event name after each frame', () => {
    const parser = new SseParser()
    const frames = parser.feed('event: delta\ndata: one\n\ndata: two\n\n')
    expect(frames.map((f) => f.event)).toEqual(['delta', 'message'])
  })

  it('flushes a truncated final frame at end of stream', () => {
    const parser = new SseParser()
    expect(parser.feed('event: delta\ndata: {"text":"tail"}')).toEqual([])
    const frames = parser.end()
    expect(frames).toHaveLength(1)
    expect(frameJson<{ text: string }>(frames[0])).toEqual({ text: 'tail' })
  })

  it('returns null for malformed JSON instead of throwing', () => {
    const parser = new SseParser()
    const [frame] = parser.feed('data: {oops\n\n')
    expect(frameJson(frame)).toBeNull()
  })
})
