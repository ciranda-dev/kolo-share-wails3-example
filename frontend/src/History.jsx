import { useEffect, useRef } from 'react'
import { useI18n } from './context.js'
import { formatBytes, formatWhen, safeText } from './format.js'

const ARROW = { send: 'M8 13V3M4 7l4-4 4 4', receive: 'M8 3v10M4 9l4 4 4-4' }

/**
 * Finished sends and receives, newest first. `withPeer` adds the column of the other side, for the
 * history of every session; a session page lists only its own peer and leaves it out.
 */
export function HistoryList({ items, onReveal, withPeer = false, empty }) {
  const { t, fail, lang } = useI18n()
  if (items.length === 0) {
    return <p className="history__empty">{empty ?? t('history.empty')}</p>
  }
  return (
    <ul className={`history${withPeer ? ' history--peer' : ''}`}>
      {items.map((item) => {
        const direction = t(item.direction === 'send' ? 'history.sent' : 'history.received')
        const result = t(`history.result.${item.result}`)
        const name = safeText(item.name)
        const peer = safeText(item.peerName)
        const when = formatWhen(item.at, lang)
        const size = item.bytes > 0 ? formatBytes(item.bytes, lang) : ''
        const detail = item.result === 'failed' ? fail(item.error, item.message) : safeText(item.savedTo)
        const canReveal = Boolean(onReveal) && item.result === 'done' && Boolean(item.savedTo)
        const Row = canReveal ? 'button' : 'div'
        return (
          <li key={item.id}>
            <Row
              className={`item is-${item.result}`}
              title={[name, peer, detail, when].filter(Boolean).join('\n')}
              type={canReveal ? 'button' : undefined}
              aria-label={canReveal ? t('history.reveal', { name }) : undefined}
              onClick={canReveal ? () => onReveal(item.savedTo) : undefined}
            >
              <span className="item__icon" aria-hidden="true">
                <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                  <path d={ARROW[item.direction] ?? ARROW.send} />
                </svg>
              </span>
              <span className="item__name">
                <span className="sr-only">{direction}: </span>
                {name}
              </span>
              {withPeer ? <span className="item__peer">{peer}</span> : null}
              <span className="item__result">{result}</span>
              <span className="item__size">{size}</span>
              <span className="item__when">{when}</span>
            </Row>
          </li>
        )
      })}
    </ul>
  )
}

/** History as a page of the window: every finished transfer of every session, and the button that empties it. */
export function HistoryPage({ items, onReveal, onClear, onClose }) {
  const { t } = useI18n()
  const title = useRef(null)
  useEffect(() => {
    title.current?.focus()
  }, [])
  return (
    <main
      className="main page page--history"
      aria-labelledby="history-title"
      onKeyDown={(ev) => {
        if (ev.key === 'Escape') {
          ev.stopPropagation()
          onClose()
        }
      }}
    >
      <div className="bar">
        <h1 className="page__title" id="history-title" tabIndex={-1} ref={title}>{t('history.title')}</h1>
        <span className="bar__actions">
          {items.length > 0 ? <button type="button" className="btn btn--ghost btn--small" onClick={onClear}>{t('history.clear')}</button> : null}
          <button type="button" className="pill" onClick={onClose}>{t('nav.close')}</button>
        </span>
      </div>
      <div className="page__body">
        <HistoryList items={items} onReveal={onReveal} withPeer />
      </div>
    </main>
  )
}
