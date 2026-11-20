/** Round avatars use these backgrounds, all light enough for the dark ink on them. */
export const AVATAR_COLORS = ['#6ab5fb', '#7fd1b9', '#f2c46d', '#f29d8b', '#e7a1c2', '#a6d36f', '#78d0e0', '#d9b38c']

/** The color of a name or key: the same text always gets the same color. */
export function avatarColor(seed) {
  let h = 2166136261
  for (const ch of String(seed ?? '')) {
    h = Math.imul(h ^ ch.codePointAt(0), 16777619)
  }
  return AVATAR_COLORS[((h ^ (h >>> 15)) >>> 0) % AVATAR_COLORS.length]
}

/** The first letter of a name, in capitals, or "?" for no name. */
export const initial = (name) => Array.from(String(name ?? '').trim())[0]?.toUpperCase() ?? '?'
