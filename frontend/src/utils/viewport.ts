export interface DropdownPosition {
  top?: number
  bottom?: number
  left: number
}

interface DropdownAnchorRect {
  top: number
  bottom: number
  left: number
}

export function clampDropdownLeft(
  anchorLeft: number,
  viewportWidth: number,
  dropdownWidth = 380,
  gutter = 8,
): number {
  const effectiveDropdownWidth = Math.min(dropdownWidth, viewportWidth - gutter * 2)
  return Math.max(gutter, Math.min(anchorLeft, viewportWidth - effectiveDropdownWidth - gutter))
}

export function getDropdownPosition(
  rect: DropdownAnchorRect,
  viewportWidth: number,
  viewportHeight: number,
  dropdownHeight = 400,
): DropdownPosition {
  const spaceBelow = viewportHeight - rect.bottom
  const spaceAbove = rect.top
  const left = clampDropdownLeft(rect.left, viewportWidth)

  if (spaceBelow < dropdownHeight && spaceAbove > spaceBelow) {
    return {
      bottom: viewportHeight - rect.top + 4,
      left,
    }
  }

  return {
    top: rect.bottom + 4,
    left,
  }
}
