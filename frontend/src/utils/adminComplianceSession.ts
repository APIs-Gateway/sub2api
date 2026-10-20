let version = 0

export function getAdminComplianceSessionVersion(): number {
  return version
}

export function invalidateAdminComplianceSession(): void {
  version++
}
