import { describe, expect, it } from 'vitest'
import { clampDropdownLeft } from '../viewport'

describe('clampDropdownLeft', () => {
  it('keeps a wide viewport anchor within both gutters', () => {
    expect(clampDropdownLeft(24, 1024)).toBe(24)
    expect(clampDropdownLeft(900, 1024)).toBe(636)
    expect(clampDropdownLeft(-20, 1024)).toBe(8)
  })

  it('uses the available width on a narrow viewport', () => {
    expect(clampDropdownLeft(160, 320)).toBe(8)
    expect(clampDropdownLeft(0, 375)).toBe(8)
  })

  it('supports a custom dropdown width and gutter', () => {
    expect(clampDropdownLeft(700, 1200, 400, 16)).toBe(784)
  })
})
