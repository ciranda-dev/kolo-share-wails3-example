import { middleCut, safeText } from './format.js'

/** A file or folder name, made safe to show and cut in the middle when long. The whole name shows on hover and on keyboard focus. */
export function Name({ value, max = 40 }) {
  const full = safeText(value)
  const short = middleCut(full, max)
  const cut = short !== full
  return (
    <span className={`name${cut ? ' is-cut' : ''}`} tabIndex={cut ? 0 : undefined} aria-label={cut ? full : undefined}>
      <span className="name__text" aria-hidden={cut ? 'true' : undefined}>{short}</span>
      {cut ? <span className="name__full" aria-hidden="true">{full}</span> : null}
    </span>
  )
}
