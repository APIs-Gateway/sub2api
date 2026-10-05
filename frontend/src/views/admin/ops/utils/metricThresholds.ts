export type ThresholdLevel = 'normal' | 'warning' | 'critical'

export function getHighMetricThresholdLevel(
  value: number | null,
  threshold: number | null | undefined
): ThresholdLevel {
  if (value == null || threshold == null || !Number.isFinite(value) || !Number.isFinite(threshold)) return 'normal'
  if (value >= threshold) return 'critical'
  if (value >= threshold * 0.8) return 'warning'
  return 'normal'
}

export function getSLAThresholdLevel(
  slaPercent: number | null,
  threshold: number | null | undefined
): ThresholdLevel {
  if (slaPercent == null || threshold == null || !Number.isFinite(slaPercent) || !Number.isFinite(threshold)) return 'normal'
  if (slaPercent < threshold) return 'critical'
  if (slaPercent < threshold + 0.1) return 'warning'
  return 'normal'
}

export function getSLAProgressPercent(
  slaPercent: number | null,
  threshold: number | null | undefined
): number {
  if (slaPercent == null || threshold == null || !Number.isFinite(slaPercent) || !Number.isFinite(threshold)) return 0
  const span = 100 - threshold
  if (span <= 0) return slaPercent >= 100 ? 100 : 0
  return Math.min(100, Math.max(((slaPercent - threshold) / span) * 100, 0))
}
