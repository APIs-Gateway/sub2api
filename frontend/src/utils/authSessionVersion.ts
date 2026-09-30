let version = 0

export function getAuthSessionVersion(): number {
  return version
}

export function invalidateAuthSession(): number {
  return ++version
}
