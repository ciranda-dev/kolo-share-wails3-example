const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

/** A byte count as a short size in decimal units: 999 B, 1.2 MB, 4.25 GB. */
export function formatBytes(bytes, lang = 'en') {
  let n = Number(bytes)
  if (!Number.isFinite(n) || n < 0) {
    n = 0
  }
  let unit = 0
  while (n >= 999.5 && unit < UNITS.length - 1) {
    n /= 1000
    unit++
  }
  const digits = unit === 0 || n >= 100 ? 0 : n >= 10 ? 1 : 2
  const text = new Intl.NumberFormat(lang, { maximumFractionDigits: digits, minimumFractionDigits: unit === 0 ? 0 : Math.min(digits, 1) }).format(n)
  return `${text} ${UNITS[unit]}`
}

/** Bytes per second as a speed: 3.4 MB/s. */
export function formatRate(bytesPerSecond, lang = 'en') {
  return `${formatBytes(bytesPerSecond, lang)}/s`
}

/** A whole number with the separators of the language: 12,480. */
export function formatCount(n, lang = 'en') {
  return new Intl.NumberFormat(lang).format(Number(n) || 0)
}

/** The share of done in total, from 0 to 1. A total of 0 counts as nothing done. */
export function fraction(done, total) {
  const d = Number(done)
  const t = Number(total)
  if (!(t > 0) || !(d > 0)) {
    return 0
  }
  return Math.min(1, d / t)
}

/** Whole percent that never shows 100 before the work is really done. */
export function percent(done, total) {
  const f = fraction(done, total)
  return f >= 1 ? 100 : Math.min(99, Math.floor(f * 100))
}

/** Cuts a long name in the middle and keeps its ending, so the extension stays readable. */
export function middleCut(name, max = 44) {
  const chars = Array.from(String(name ?? ''))
  if (chars.length <= max) {
    return chars.join('')
  }
  const tail = Math.min(12, Math.floor((max - 1) / 3))
  const head = max - 1 - tail
  return `${chars.slice(0, head).join('')}…${chars.slice(chars.length - tail).join('')}`
}


/** A date for the history list: the time for today, the day and month otherwise. */
export function formatWhen(value, lang = 'en', now = new Date()) {
  const date = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const sameDay = date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate()
  const options = sameDay ? { hour: 'numeric', minute: '2-digit' } : { day: 'numeric', month: 'short' }
  return new Intl.DateTimeFormat(lang, options).format(date)
}

/** Time left as a countdown: 59:12, or 1:04:09 from an hour up. Never negative. */
export function formatCountdown(ms) {
  const total = Math.max(0, Math.ceil((Number(ms) || 0) / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = String(total % 60).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`
}

/** A duration in its largest whole unit, in words of the language: 1 hour, 90 minutes, 45 seconds. */
export function formatDuration(ms, lang = 'en') {
  const n = Math.max(0, Math.round(Number(ms) || 0))
  const [value, unit] = n >= 3_600_000 && n % 3_600_000 === 0
    ? [n / 3_600_000, 'hour']
    : n >= 60_000 && n % 60_000 === 0
      ? [n / 60_000, 'minute']
      : [n / 1000, 'second']
  return new Intl.NumberFormat(lang, { style: 'unit', unit, unitDisplay: 'long', maximumFractionDigits: 1 }).format(value)
}

/** A code in groups of four, easier to read aloud and to compare. */
export function codeGroups(code) {
  return String(code ?? '').match(/.{1,4}/gu) ?? []
}

/** Turns whatever a rejected call threw into a string for the UI. */
export function errorText(err) {
  if (err instanceof Error && err.message) {
    return err.message
  }
  if (typeof err === 'string') {
    return err
  }
  if (err && typeof err === 'object' && typeof err.message === 'string') {
    return err.message
  }
  return String(err)
}

const BINARY_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

/** A byte limit in binary units, the way the service words it: 32 GiB. */
export function formatBinary(bytes, lang = 'en') {
  let n = Math.max(Number(bytes) || 0, 0)
  let unit = 0
  while (n >= 1024 && unit < BINARY_UNITS.length - 1) {
    n /= 1024
    unit++
  }
  return `${new Intl.NumberFormat(lang, { maximumFractionDigits: 1 }).format(n)} ${BINARY_UNITS[unit]}`
}

// Controls, format characters (bidi overrides and isolates, zero-width characters), line and
// paragraph separators, private use and lone surrogates: none of them has a visible shape.
const UNSEEN = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\p{Co}\p{Cs}]/gu

/**
 * Text that came from another computer (a file name, a path, the words of an error), made safe to
 * show: every character that is invisible or that changes the direction of the text around it is
 * replaced by its code in brackets, so a name cannot pass for another one on screen.
 */
export function safeText(value) {
  return String(value ?? '').replace(UNSEEN, (ch) => `[U+${ch.codePointAt(0).toString(16).toUpperCase().padStart(4, '0')}]`)
}
