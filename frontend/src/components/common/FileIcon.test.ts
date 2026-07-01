import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, h } from 'vue'
import FileIcon from './FileIcon.vue'

describe('FileIcon', () => {
  it('renders a bare svg with aria-hidden by default', () => {
    const wrapper = mount(FileIcon, { props: { fileName: 'movie.mp4' } })
    const svg = wrapper.find('svg')
    expect(svg.exists()).toBe(true)
    expect(svg.attributes('aria-hidden')).toBe('true')
    expect(svg.attributes('role')).toBeUndefined()
    expect(svg.attributes('width')).toBe('18')
    expect(svg.attributes('height')).toBe('18')
  })

  it('routes to the media glyph for .mp4', () => {
    const wrapper = mount(FileIcon, { props: { fileName: 'movie.mp4' } })
    // Media glyph contains a play-triangle path starting with "M4 7 L4 17 L11 12 Z".
    const paths = wrapper.findAll('svg path')
    const triangle = paths.some(p => p.attributes('d')?.startsWith('M4 7 L4 17 L11 12'))
    expect(triangle).toBe(true)
  })

  it('routes to the default three-signal glyph for unknown extensions', () => {
    const wrapper = mount(FileIcon, { props: { fileName: 'data.xyz' } })
    const rects = wrapper.findAll('svg rect')
    // Default bare glyph has three rounded rects and no chipped container rect.
    expect(rects.length).toBe(3)
    expect(rects.every(r => r.attributes('rx') === '1.5')).toBe(true)
  })

  it('switches to role=img + aria-label when a label is provided', () => {
    const wrapper = mount(FileIcon, {
      props: { fileName: 'a.zip', label: 'Archive' },
    })
    const svg = wrapper.find('svg')
    expect(svg.attributes('role')).toBe('img')
    expect(svg.attributes('aria-label')).toBe('Archive')
    expect(svg.attributes('aria-hidden')).toBeUndefined()
  })

  it('renders the chipped squircle container with a unique gradient id', () => {
    const wrapper = mount(FileIcon, {
      props: { fileName: 'unknown.bin', tier: 'chipped', size: 40 },
    })
    const svg = wrapper.find('svg')
    expect(svg.attributes('width')).toBe('40')
    const defs = svg.find('defs')
    expect(defs.exists()).toBe(true)
    const grad = defs.find('linearGradient')
    expect(grad.exists()).toBe(true)
    const gradId = grad.attributes('id')
    expect(gradId).toMatch(/^file-icon-grad-/)
    const containerRect = svg.find('rect[fill]')
    expect(containerRect.attributes('fill')).toBe(`url(#${gradId})`)
    // Inner glyph stroke should use the neon token, not currentColor.
    expect(svg.attributes('stroke')).toBe('var(--neon-btn-text)')
  })

  it('produces non-colliding gradient ids across instances', () => {
    // Mount two icons inside one app so useId() advances its shared counter.
    const Host = defineComponent({
      components: { FileIcon },
      render() {
        return h('div', [
          h(FileIcon, { fileName: 'a.bin', tier: 'chipped', size: 40 }),
          h(FileIcon, { fileName: 'b.bin', tier: 'chipped', size: 40 }),
        ])
      },
    })
    const wrapper = mount(Host)
    const grads = wrapper.findAll('linearGradient')
    expect(grads.length).toBe(2)
    const idA = grads[0].attributes('id')
    const idB = grads[1].attributes('id')
    expect(idA).not.toBe(idB)
    wrapper.unmount()
  })

  it('keeps bare glyph stroke on currentColor', () => {
    const wrapper = mount(FileIcon, { props: { fileName: 'song.flac' } })
    expect(wrapper.find('svg').attributes('stroke')).toBe('currentColor')
  })

  it('falls back to default glyph for null / empty filename', () => {
    const nullWrap = mount(FileIcon, { props: { fileName: null } })
    expect(nullWrap.findAll('svg rect[rx="1.5"]').length).toBe(3)
    const emptyWrap = mount(FileIcon, { props: { fileName: '' } })
    expect(emptyWrap.findAll('svg rect[rx="1.5"]').length).toBe(3)
  })
})
