import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

describe('OpsDashboard trend card layout', () => {
  it('bounds responsive chart cards to a fixed height', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/views/admin/ops/OpsDashboard.vue'), 'utf8')

    expect(source).toContain('<div class="lg:col-span-1 h-[360px]">')
    expect(source).toContain('<div class="lg:col-span-2 h-[360px]">')
    expect(source).toContain('<div class="lg:col-span-1 min-h-[360px]">')
  })
})
