// Shared badge/label helpers for Prompt Audit admin views (list + detail modal).
// Kept dependency-free (no useI18n() call inside) so both the page and the modal
// can reuse the exact same classification logic with their own `t`.

export type TranslateFn = (key: string, params?: Record<string, unknown>) => string

export function decisionLabel(t: TranslateFn, decision: string): string {
  if (!decision) return '-'
  const key = `admin.promptAudit.decisions.${decision}`
  const label = t(key)
  return label === key ? decision : label
}

export function riskLevelLabel(t: TranslateFn, riskLevel: string): string {
  if (!riskLevel) return '-'
  const key = `admin.promptAudit.riskLevels.${riskLevel}`
  const label = t(key)
  return label === key ? riskLevel : label
}

export function decisionClass(decision: string): string {
  if (decision === 'critical') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (decision === 'flag') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  if (decision === 'pass') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
}

export function riskLevelClass(riskLevel: string): string {
  if (riskLevel === 'critical') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (riskLevel === 'high') return 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-300'
  if (riskLevel === 'medium') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  if (riskLevel === 'low') return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
}
