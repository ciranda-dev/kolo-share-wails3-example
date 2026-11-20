/** The elements inside root that Tab reaches, in order. */
export const focusables = (root) =>
  [...root.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')].filter(
    (el) => !el.disabled && el.tabIndex >= 0 && el.getClientRects().length > 0,
  )

/** Where Tab or Shift+Tab goes from index among count elements when focus must stay inside: the element to focus, or -1 to let the browser move it. */
export function trapStep(index, count, shift) {
  if (count === 0) {
    return -1
  }
  if (index < 0) {
    return shift ? count - 1 : 0
  }
  if (shift && index === 0) {
    return count - 1
  }
  if (!shift && index === count - 1) {
    return 0
  }
  return -1
}

/** The index the arrow keys of a menu move to: Down and Up wrap, Home and End jump, anything else is -1. */
export function menuStep(index, count, key) {
  if (count === 0) {
    return -1
  }
  switch (key) {
    case 'ArrowDown':
      return (index + 1) % count
    case 'ArrowUp':
      return (index - 1 + count) % count
    case 'Home':
      return 0
    case 'End':
      return count - 1
  }
  return -1
}
