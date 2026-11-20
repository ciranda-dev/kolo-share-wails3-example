import { useEffect, useRef, useState } from 'react'
import { useI18n } from './context.js'
import { formatBytes, formatCount, fraction, percent, safeText } from './format.js'

/** One line under a name: what it is, how large, how many files. */
export function metaLine(t, lang, item) {
  const total = item.bytesTotal ?? item.bytes ?? 0
  const files = item.filesTotal ?? item.files ?? 0
  const parts = [t(item.kind === 'folder' ? 'kind.folder' : 'kind.file')]
  if (total > 0) {
    parts.push(formatBytes(total, lang))
  }
  if (item.kind === 'folder' && files > 0) {
    parts.push(t('files', { n: files, count: formatCount(files, lang) }))
  }
  return parts.join(' · ')
}

/** The state of something as a dot and a sentence. */
export function Status({ state, children, className = '' }) {
  return (
    <p className={`status is-${state}${className ? ` ${className}` : ''}`}>
      <span className="status__dot" aria-hidden="true" />
      <span>{children}</span>
    </p>
  )
}

/** A thin bar for done of total; it sweeps when there is no total yet. */
export function Progress({ done, total }) {
  const { t } = useI18n()
  const known = total > 0
  return (
    <div
      className={`meter${known ? '' : ' is-busy'}`}
      role="progressbar"
      aria-label={t('progress.label')}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={known ? percent(done, total) : undefined}
    >
      <span className="meter__fill" style={known ? { width: `${fraction(done, total) * 100}%` } : undefined} />
    </div>
  )
}

/** A file or a folder, drawn in the current text color. */
export function KindIcon({ kind }) {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" aria-hidden="true">
      {kind === 'folder'
        ? <path d="M1.75 4.25a1 1 0 0 1 1-1h3.1l1.4 1.5h6a1 1 0 0 1 1 1v6.5a1 1 0 0 1-1 1H2.75a1 1 0 0 1-1-1z" />
        : <path d="M4 1.75h5.25L12.5 5v9.25H4zM9.25 1.75V5h3.25" />}
    </svg>
  )
}

/** A button that copies text and says so for two seconds. */
export function CopyButton({ text, label, onCopy, className = 'btn btn--wide' }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const copy = () => {
    Promise.resolve(onCopy(text)).then(() => {
      setCopied(true)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(false), 2000)
    }).catch(() => {})
  }
  return (
    <button type="button" className={className} onClick={copy}>
      {copied ? t('copied') : label}
    </button>
  )
}

/** A folder path cut at its start, so the last folders stay readable. */
export function PathText({ value, empty }) {
  if (!value) {
    return <span className="path__value">{empty}</span>
  }
  return (
    <span className="path__value" title={safeText(value)}>
      <bdi>{safeText(value)}</bdi>
    </span>
  )
}

/** The system this window runs on, for the name of its file manager. */
export function platform() {
  const name = `${window.navigator.userAgentData?.platform ?? ''} ${window.navigator.platform ?? ''} ${window.navigator.userAgent ?? ''}`.toLowerCase()
  return name.includes('mac') ? 'mac' : name.includes('win') ? 'windows' : 'other'
}
