import { describe, expect, it } from 'vitest'

import { sanitizeSvg } from '../sanitize'

const svgNamespace = 'http://www.w3.org/2000/svg'
const xlinkNamespace = 'http://www.w3.org/1999/xlink'

function parsedSvg(output: string): Element {
  const document = new DOMParser().parseFromString(output, 'image/svg+xml')
  expect(document.querySelector('parsererror')).toBeNull()
  expect(document.documentElement.localName).toBe('svg')
  expect(document.documentElement.namespaceURI).toBe(svgNamespace)
  return document.documentElement
}

describe('sanitizeSvg with real DOMPurify', () => {
  it('preserves safe SVG geometry and the supported filter profile', () => {
    const dirty = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><defs><filter id="soft"><feGaussianBlur stdDeviation="1" /></filter></defs><circle cx="4" cy="4" r="2" fill="red" filter="url(#soft)" /></svg>'
    const svg = parsedSvg(sanitizeSvg(dirty))

    expect(svg.getAttribute('viewBox')).toBe('0 0 8 8')
    const circle = svg.querySelector('circle')
    expect(circle).not.toBeNull()
    expect(circle?.getAttribute('cx')).toBe('4')
    expect(circle?.getAttribute('cy')).toBe('4')
    expect(circle?.getAttribute('r')).toBe('2')
    expect(circle?.getAttribute('fill')).toBe('red')
    expect(circle?.getAttribute('filter')).toBe('url(#soft)')
    const blur = svg.querySelector('defs > filter > feGaussianBlur')
    expect(blur).not.toBeNull()
    expect(blur?.parentElement?.getAttribute('id')).toBe('soft')
    expect(blur?.getAttribute('stdDeviation')).toBe('1')
  })

  it('removes scripts, event handlers and executable links while retaining safe SVG', () => {
    const dirty = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" onload="window.__svgXss=1"><script>window.__svgXss=2</script><a xmlns:xlink="http://www.w3.org/1999/xlink" href="javascript:alert(1)" xlink:href="javascript:alert(2)"><circle cx="4" cy="4" r="2" onclick="window.__svgXss=3" /></a><foreignObject><p>not an SVG consumer</p></foreignObject></svg>'
    const svg = parsedSvg(sanitizeSvg(dirty))

    expect(svg.getAttribute('viewBox')).toBe('0 0 8 8')
    expect(svg.hasAttribute('onload')).toBe(false)
    expect(svg.querySelector('script')).toBeNull()
    expect(svg.querySelector('foreignObject')).toBeNull()
    expect(svg.querySelector('[onload], [onclick]')).toBeNull()
    const link = svg.querySelector('a')
    expect(link).not.toBeNull()
    expect(link?.hasAttribute('href')).toBe(false)
    expect(link?.getAttributeNS(xlinkNamespace, 'href')).toBeNull()
    const circle = svg.querySelector('a > circle')
    expect(circle).not.toBeNull()
    expect(circle?.getAttribute('cx')).toBe('4')
    expect(circle?.getAttribute('cy')).toBe('4')
    expect(circle?.getAttribute('r')).toBe('2')
    expect(circle?.hasAttribute('onclick')).toBe(false)
  })

  it('returns an empty value for empty SVG input', () => {
    expect(sanitizeSvg('')).toBe('')
  })
})
