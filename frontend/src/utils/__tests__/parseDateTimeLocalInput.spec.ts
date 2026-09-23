import { afterEach, describe, expect, it, vi } from 'vitest'
import { getBrowserTimeZone, parseDateTimeLocalInput } from '../format'

const localSeconds = (y: number, m: number, d: number, h = 0, min = 0, s = 0, ms = 0) =>
  Math.floor(new Date(y, m - 1, d, h, min, s, ms).getTime() / 1000)

describe('parseDateTimeLocalInput', () => {
  it('parses datetime-local values in the browser time zone', () => {
    expect(parseDateTimeLocalInput('2026-03-01T10:30')).toBe(localSeconds(2026, 3, 1, 10, 30))
    expect(parseDateTimeLocalInput('2026-03-01T10:30:45')).toBe(localSeconds(2026, 3, 1, 10, 30, 45))
    expect(parseDateTimeLocalInput('2026-03-01T10:30:45.9876')).toBe(localSeconds(2026, 3, 1, 10, 30, 45, 987))
    expect(parseDateTimeLocalInput('2028-02-29T00:00')).toBe(localSeconds(2028, 2, 29))
  })

  it('rejects empty, malformed and timezone-bearing values', () => {
    for (const value of ['', '2026-03-01', '2026-03-01 10:30', '2026-3-1T10:30', '2026-03-01T10:30Z', '2026-03-01T10:30:00+08:00', 'not a date']) {
      expect(parseDateTimeLocalInput(value), value).toBeNull()
    }
  })

  it('rejects out-of-range fields and calendar overflows instead of normalizing them', () => {
    for (const value of ['0000-01-01T00:00', '2026-00-10T10:00', '2026-13-10T10:00', '2026-01-00T10:00', '2026-01-32T10:00', '2026-01-10T24:00', '2026-01-10T10:60', '2026-01-10T10:00:60', '2026-02-30T10:00', '2027-02-29T10:00', '2026-04-31T10:00']) {
      expect(parseDateTimeLocalInput(value), value).toBeNull()
    }
  })
})

describe('getBrowserTimeZone', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('returns the resolved IANA time zone', () => {
    vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(
      () => ({ resolvedOptions: () => ({ timeZone: 'Asia/Shanghai' }) }) as unknown as Intl.DateTimeFormat,
    )
    expect(getBrowserTimeZone()).toBe('Asia/Shanghai')
  })

  it('falls back to UTC when the time zone is unavailable', () => {
    vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(
      () => ({ resolvedOptions: () => ({ timeZone: '' }) }) as unknown as Intl.DateTimeFormat,
    )
    expect(getBrowserTimeZone()).toBe('UTC')
    vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(() => {
      throw new Error('unsupported')
    })
    expect(getBrowserTimeZone()).toBe('UTC')
  })
})
