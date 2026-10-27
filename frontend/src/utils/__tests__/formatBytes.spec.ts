import { describe, expect, it } from 'vitest'
import { formatBytes } from '../format'
import { formatByteRate } from '@/views/admin/ops/utils/opsFormatters'

describe('formatBytes', () => {
  it.each([
    { value: 0.5, expected: '0.5 Bytes' },
    { value: 0.01, expected: '0.01 Bytes' },
    { value: 0, expected: '0 Bytes' },
    { value: 1024, expected: '1 KB' }
  ])('formats $value bytes as $expected', ({ value, expected }) => {
    expect(formatBytes(value)).toBe(expected)
  })

  it('keeps the byte unit for an ops rate below one byte per second', () => {
    expect(formatByteRate(30, 1)).toBe('0.5 Bytes/s')
  })
})
