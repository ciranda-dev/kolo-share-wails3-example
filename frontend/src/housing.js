// The camera housing over the notch page, in CSS pixels. Go reads it from the screen and opens the page with
// ?top=&housing= (its height and width), making the window taller by top, so the page keeps its height below it.

/** housingOf reads the housing from the page's address: 0 and 0 on a screen without one. */
export function housingOf(search) {
  const q = new URLSearchParams(search)
  const top = whole(q.get('top'))
  const width = whole(q.get('housing'))
  return top > 0 && width > 0 ? { top, width } : { top: 0, width: 0 }
}

const whole = (text) => (/^[0-9]{1,4}$/.test(text ?? '') ? Number(text) : 0)

/** showHousing hands the housing to the styles, which start the content below it. */
export function showHousing(root, h) {
  root.style.setProperty('--housing-top', `${h.top}px`)
  root.style.setProperty('--housing-width', `${h.width}px`)
}
