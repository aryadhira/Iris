import { describe, expect, it } from 'vitest'
import { roleLabel, snippetParts } from './search'

describe('snippetParts', () => {
  it('turns only the server marks into hits', () => {
    expect(snippetParts('kapal ke <mark>pelabuhan</mark> pagi')).toEqual([
      { text: 'kapal ke ', hit: false },
      { text: 'pelabuhan', hit: true },
      { text: ' pagi', hit: false },
    ])
  })

  it('escapes message HTML instead of rendering it', () => {
    const parts = snippetParts('<script>alert(1)</script><mark>kopi</mark>')
    const text = parts.map((part) => part.text).join('')
    expect(text).toContain('&lt;script&gt;')
    expect(text).not.toContain('<script>')
    expect(parts.filter((part) => part.hit).map((part) => part.text)).toEqual(['kopi'])
  })

  it('handles plain text with no match', () => {
    expect(snippetParts('tidak ada')).toEqual([{ text: 'tidak ada', hit: false }])
  })
})

describe('roleLabel', () => {
  it('uses the same labels as the transcript', () => {
    expect(roleLabel('user')).toBe('Kamu')
    expect(roleLabel('assistant')).toBe('Hermes')
    expect(roleLabel('system')).toBe('Sistem')
  })
})
