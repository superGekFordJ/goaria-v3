import { describe, expect, it } from 'vitest'

const forbiddenPattern = new RegExp(
  [
    ['go', 'file'].join(''),
    ['go', 'file[.]io'].join(''),
    ['i', 'bb'].join(''),
    ['i', 'bb[.]co'].join(''),
    ['i[.]', 'i', 'bb[.]co'].join(''),
    ['supported', 'site'].join('[- ]'),
    ['supported', 'sites'].join(' '),
    ['market', 'place'].join(''),
    ['plugin', 'browser'].join(' '),
    ['site', 'catalog'].join(' '),
    ['provider', 'list'].join(' '),
  ].join('|'),
  'i',
)

function extractDownloadGroupCss(raw: string): string {
  return Array.from(raw.matchAll(/\.download-group-[^{}]+\{[^}]*\}/g))
    .map(match => match[0])
    .join('\n')
}

describe('download-group visual policy', () => {
  it('keeps download-group selectors generic and free of hardcoded color syntax', async () => {
    const [taskCardSource, taskHeaderSource] = await Promise.all([
      import('./TaskCard.vue?raw'),
      import('./TaskHeader.vue?raw'),
    ])

    const source = `${taskCardSource.default}\n${taskHeaderSource.default}`
    const css = extractDownloadGroupCss(source)

    expect(css).toContain('.download-group-')
    expect(css).not.toMatch(forbiddenPattern)
    expect(css).not.toMatch(/#[0-9a-f]{3,8}\b/i)
    expect(css).not.toMatch(/\brgba?\(/i)
  })
})
