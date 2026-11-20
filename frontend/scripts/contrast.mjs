// WCAG 2.x contrast of every text and background pair of src/share.css, in both themes.
// Exits 1 when a pair is under its minimum, or when the two copies of the light theme differ.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const css = readFileSync(join(fileURLToPath(import.meta.url), '..', '..', 'src', 'share.css'), 'utf8')
const tokens = (block) => Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6});/g)].map((m) => [m[1], m[2].toLowerCase()]))
const dark = tokens(css.match(/:root\s*\{([^}]*)\}/)[1])
const lightMedia = tokens(css.match(/:root:not\(\[data-theme="dark"\]\)\s*\{([^}]*)\}/)[1])
const light = tokens(css.match(/:root\[data-theme="light"\]\s*\{([^}]*)\}/)[1])

const lin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
const lum = (hex) => {
  const [r, g, b] = [1, 3, 5].map((i) => lin(parseInt(hex.slice(i, i + 2), 16) / 255))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a, b) => {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

// text, background, where, minimum
const pairs = [
  ['text', 'bg', 'text on the window', 4.5],
  ['text', 'panel', 'text on cards, header and sidebar', 4.5],
  ['text', 'soft', 'chosen tab, drop zone while dragging', 4.5],
  ['muted', 'bg', 'hints, placeholders in fields', 4.5],
  ['muted', 'panel', 'labels, meta lines, history', 4.5],
  ['muted', 'soft', 'notes on the drop zone while dragging', 4.5],
  ['on-accent', 'accent', 'primary button, live badge', 4.5],
  ['on-accent', 'accent-2', 'primary button on hover', 4.5],
  ['danger', 'panel', 'error text on cards', 4.5],
  ['danger', 'bg', 'error text on the window', 4.5],
  ['accent', 'panel', 'ring arc, status dot', 3],
  ['accent', 'bg', 'ring arc on the window', 3],
  ['accent', 'soft', 'ring on the drop zone while dragging', 3],
  ['accent-2', 'panel', 'focus ring on cards', 3],
  ['accent-2', 'bg', 'focus ring on the window', 3],
  ['text', 'panel', 'peer dots on the ring', 3],
  ['warn', 'panel', 'offline dot of a session, line of an offline band', 3],
  ['warn', 'bg', 'offline dot on the window', 3],
  ['text', 'active', 'name of the chosen session, chosen History or Settings', 4.5],
  ['muted', 'active', 'quiet line of the chosen session', 4.5],
  ['muted', 'hover', 'quiet line of a session under the pointer', 4.5],
  ['online', 'panel', 'Connected, in a session row and under its name', 4.5],
  ['online', 'active', 'Connected, in the chosen session row', 4.5],
  ['warn', 'active', 'Offline, in the chosen session row', 4.5],
  ['accent', 'active', 'icon of a row in the add menu', 3],
]

let failed = false
if (JSON.stringify(lightMedia) !== JSON.stringify(light)) {
  console.error('FAIL: the light tokens in the media query and in [data-theme="light"] differ')
  failed = true
}
for (const [name, theme] of [['dark', dark], ['light', { ...dark, ...light }], ['notch (dark on black)', { ...dark, bg: '#000000', panel: '#000000', soft: '#000000' }]]) {
  console.log(`\n${name}`)
  for (const [a, b, where, need] of pairs) {
    const r = ratio(theme[a], theme[b])
    const ok = r >= need
    failed ||= !ok
    console.log(`  ${a.padEnd(9)} ${theme[a]} on ${b.padEnd(8)} ${theme[b]}  ${r.toFixed(2).padStart(5)}:1  ${ok ? 'pass' : 'FAIL'} (${need})  ${where}`)
  }
}
process.exit(failed ? 1 : 0)
