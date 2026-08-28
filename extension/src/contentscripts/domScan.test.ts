import { describe, expect, it } from 'vitest'
import { collectDomLinks, type DomScanNode, type DomScanRoot } from './domScan'

function attrMap(attrs: Record<string, string>): (name: string) => string | null {
  return name => (name in attrs ? attrs[name] : null)
}

function node(partial: Omit<DomScanNode, 'getAttribute'> & { attrs?: Record<string, string> }): DomScanNode {
  const attrs = partial.attrs ?? {}
  return {
    tagName: partial.tagName,
    href: partial.href,
    currentSrc: partial.currentSrc,
    referrerPolicy: partial.referrerPolicy,
    rel: partial.rel,
    ownerDocument: partial.ownerDocument,
    getAttribute: attrMap(attrs),
  }
}

function rootOf(nodes: DomScanNode[], extra: Partial<DomScanRoot> = {}): DomScanRoot {
  return {
    querySelectorAll: () => nodes,
    baseURI: 'https://example.com/page',
    referrerPolicy: '',
    title: 'Example',
    ...extra,
  }
}

describe('collectDomLinks', () => {
  it('sets truncated and stops at 128 unique on the 129th', () => {
    const nodes = Array.from({ length: 129 }, (_, i) =>
      node({
        tagName: 'A',
        attrs: { href: `https://example.com/f/${i}.bin` },
      }),
    )
    const result = collectDomLinks(rootOf(nodes))
    expect(result.items).toHaveLength(128)
    expect(result.truncated).toBe(true)
    expect(result.items[0]?.url).toBe('https://example.com/f/0.bin')
    expect(result.items[127]?.url).toBe('https://example.com/f/127.bin')
  })

  it('stops at the visit cap', () => {
    const nodes = Array.from({ length: 5 }, (_, i) =>
      node({
        tagName: 'A',
        attrs: { href: `https://example.com/v/${i}.bin` },
      }),
    )
    const result = collectDomLinks(rootOf(nodes), { maxVisit: 2 })
    expect(result.items).toHaveLength(2)
    expect(result.truncated).toBe(true)
  })

  it('stops when the injected clock exceeds the time budget', () => {
    let calls = 0
    const now = () => {
      calls += 1
      return calls > 1 ? 100 : 0
    }
    const nodes = Array.from({ length: 8 }, (_, i) =>
      node({
        tagName: 'A',
        attrs: { href: `https://example.com/t/${i}.bin` },
      }),
    )
    const result = collectDomLinks(rootOf(nodes), { now, maxMs: 50 })
    expect(result.truncated).toBe(true)
    expect(result.items.length).toBeLessThan(8)
  })

  it('prefers currentSrc over the src attribute', () => {
    const result = collectDomLinks(
      rootOf([
        node({
          tagName: 'IMG',
          currentSrc: 'https://example.com/current.png',
          attrs: { src: 'https://example.com/attr.png' },
        }),
      ]),
    )
    expect(result.items).toHaveLength(1)
    expect(result.items[0]?.url).toBe('https://example.com/current.png')
    expect(result.items[0]?.kind).toBe('image')
  })

  it('resolves relative URLs against baseURI', () => {
    const result = collectDomLinks(
      rootOf(
        [
          node({
            tagName: 'A',
            attrs: { href: 'files/a.bin' },
            ownerDocument: { baseURI: 'https://example.com/dir/' },
          }),
        ],
        { baseURI: 'https://example.com/dir/' },
      ),
    )
    expect(result.items[0]?.url).toBe('https://example.com/dir/files/a.bin')
  })

  it('keeps the first document-order occurrence on duplicate URLs', () => {
    const result = collectDomLinks(
      rootOf([
        node({ tagName: 'A', attrs: { href: 'https://example.com/same.bin' } }),
        node({
          tagName: 'IMG',
          attrs: { src: 'https://example.com/same.bin' },
        }),
      ]),
    )
    expect(result.items).toHaveLength(1)
    expect(result.items[0]?.kind).toBe('link')
  })

  it('skips m3u playlists', () => {
    const result = collectDomLinks(
      rootOf([
        node({ tagName: 'A', attrs: { href: 'https://example.com/live.m3u8' } }),
        node({ tagName: 'A', attrs: { href: 'https://example.com/ok.bin' } }),
      ]),
    )
    expect(result.items).toHaveLength(1)
    expect(result.items[0]?.url).toBe('https://example.com/ok.bin')
  })

  it('does not walk a provided shadowRoot or iframe document', () => {
    const hidden = node({
      tagName: 'A',
      attrs: { href: 'https://example.com/hidden.bin' },
    })
    const visible = node({
      tagName: 'A',
      attrs: { href: 'https://example.com/visible.bin' },
    })
    let shadowQueries = 0
    const result = collectDomLinks({
      querySelectorAll: () => [visible],
      baseURI: 'https://example.com/',
      title: '',
      shadowRoot: {
        querySelectorAll: () => {
          shadowQueries += 1
          return [hidden]
        },
      },
    })
    expect(shadowQueries).toBe(0)
    expect(result.items.map(row => row.url)).toEqual(['https://example.com/visible.bin'])
  })
})
