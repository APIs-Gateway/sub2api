import { describe, expect, it } from 'vitest'
import { getHighMetricThresholdLevel, getSLAProgressPercent, getSLAThresholdLevel } from '../metricThresholds'

describe('configured metric threshold boundaries', () => {
  it.each([
    [null, 5, 'normal'], [3, undefined, 'normal'], [3, null, 'normal'],
    [NaN, 5, 'normal'], [3, Infinity, 'normal'], [3, 5, 'normal'],
    [4, 5, 'warning'], [5, 5, 'critical'], [0, 0, 'critical']
  ] as const)('classifies high metric %s against %s', (value, threshold, level) => {
    expect(getHighMetricThresholdLevel(value, threshold)).toBe(level)
  })

  it.each([
    [null, 99.5, 'normal'], [99, undefined, 'normal'], [NaN, 99.5, 'normal'],
    [99, NaN, 'normal'], [99, 99.5, 'critical'], [99.5, 99.5, 'warning'],
    [99.6, 99.5, 'normal'], [100, 0, 'normal']
  ] as const)('classifies SLA %s against %s', (value, threshold, level) => {
    expect(getSLAThresholdLevel(value, threshold)).toBe(level)
  })

  it.each([
    [null, 99.5, 0], [99, undefined, 0], [NaN, 99.5, 0], [99, Infinity, 0],
    [90, 80, 50], [79, 80, 0], [101, 80, 100], [100, 100, 100], [99, 100, 0], [50, 0, 50]
  ] as const)('bounds SLA progress %s against %s', (value, threshold, percent) => {
    expect(getSLAProgressPercent(value, threshold)).toBe(percent)
  })
})
