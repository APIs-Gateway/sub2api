let version = 0

export function getAnnouncementReadSessionVersion(): number {
  return version
}

export function invalidateAnnouncementReadSession(): number {
  return ++version
}
