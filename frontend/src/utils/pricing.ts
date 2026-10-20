/**
 * formatScaled formats a per-token (or per-request) USD price scaled by `scale`.
 *
 *   formatScaled(0.000003, 1_000_000) → "$3"        // per 1M tokens
 *   formatScaled(0.5,        1)        → "$0.5"      // per request
 *   formatScaled(null,       1_000_000) → "-"
 *
 * Rounds to 10 significant digits, then formats the resulting number without
 * trimming digits from an exponent or an integer magnitude.
 */
export function formatScaled(value: number | null, scale: number): string {
  if (value == null) return '-'
  return `$${Number((value * scale).toPrecision(10)).toString()}`
}
