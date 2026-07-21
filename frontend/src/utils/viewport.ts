export function clampDropdownLeft(
  anchorLeft: number,
  viewportWidth: number,
  dropdownWidth = 380,
  gutter = 8,
): number {
  const effectiveDropdownWidth = Math.min(dropdownWidth, viewportWidth - gutter * 2)
  return Math.max(gutter, Math.min(anchorLeft, viewportWidth - effectiveDropdownWidth - gutter))
}
