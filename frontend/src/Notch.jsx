import { useCallback, useEffect, useRef, useState } from 'react'
import { call, on } from './service.js'
import { htmlLang, pickLocale, translate } from './i18n.js'
import { formatBytes, formatRate, middleCut, safeText } from './format.js'
import { errorSentence } from './errors.js'
import { loadPrefs } from './prefs.js'
import { metaLine } from './parts.jsx'
import { statusText } from './session.js'
import { Ring } from './Ring.jsx'
import './share.css'
import './notch.css'

/** The second line of a transfer: where it stands, how fast, and how many more run. */
function detail(t, fail, lang, s) {
  const parts = [statusText(t, fail, lang, s, safeText(s.peerName))]
  if (s.state !== 'failed') {
    if (s.active && s.bytesPerSecond > 0) {
      parts.push(formatRate(s.bytesPerSecond, lang))
    }
    if (s.others > 0) {
      parts.push(t('notch.others', { n: s.others }))
    }
  }
  return parts.join(' · ')
}

/** The one large number: the percent when there is one, the bytes so far otherwise. */
function datum(lang, s) {
  if (s.state === 'done' || s.state === 'delivered') {
    return formatBytes(s.bytesTotal, lang)
  }
  if (!s.active || s.state === 'queued') {
    return ''
  }
  if (s.percent >= 0) {
    return `${Math.min(100, Math.floor(s.percent))}%`
  }
  return s.bytesDone > 0 ? formatBytes(s.bytesDone, lang) : ''
}

/**
 * The page of the macOS notch window: one thing in one row. The library draws the notch; this page
 * only fills its 660 x 92 area, and shows nothing when there is nothing to show. It never holds the
 * pairing code: Copy asks the service to put it on the clipboard. Save and Decline act on the offer
 * the service shows here, so the page never names one.
 */
export function Notch() {
  const [locale, setLocale] = useState(() => pickLocale(loadPrefs().lang, window.navigator.languages ?? []))
  const [state, setState] = useState(null)
  const [copied, setCopied] = useState(false)
  const timer = useRef(0)
  const lang = htmlLang[locale]
  const t = useCallback((key, vars) => translate(locale, key, vars), [locale])
  const fail = useCallback((err, fallback) => errorSentence(t, locale, err, fallback), [t, locale])

  useEffect(() => {
    document.documentElement.lang = lang
  }, [lang])

  useEffect(() => {
    const off = on('share:notch', (next) => next && setState(next))
    call('Notch').then((now) => setState((prev) => prev ?? now)).catch(() => {})
    // The main window saves the language; follow it without a reload.
    const onStorage = () => setLocale(pickLocale(loadPrefs().lang, window.navigator.languages ?? []))
    window.addEventListener('storage', onStorage)
    return () => {
      off()
      window.removeEventListener('storage', onStorage)
      window.clearTimeout(timer.current)
    }
  }, [])

  // An offer's buttons carry the ID the page shows, so a click acts on that item and never on one that took its place.
  const act = (method, ...args) => () => call(method, ...args).catch(() => {})
  const hide = (
    <button type="button" className="notch__hide" aria-label={t('notch.hide')} title={t('notch.hide')} onClick={act('NotchHide')}>
      <svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3.5 9.5 4.5-4 4.5 4" /></svg>
    </button>
  )

  if (!state || (!state.id && !state.hasCode)) {
    return <main className="notch" aria-label={t('app.title')} />
  }

  if (!state.id) {
    const copy = () => {
      call('NotchCopyCode').then(() => {
        setCopied(true)
        window.clearTimeout(timer.current)
        timer.current = window.setTimeout(() => setCopied(false), 2000)
      }).catch(() => {})
    }
    return (
      <main className="notch" aria-label={t('app.title')}>
        <Ring busy label={t('progress.label')} valueText={t('connect.waiting')} />
        <div className="notch__text">
          <p className="notch__name">{t('notch.code')}</p>
          <p className="notch__detail" role="status">{t('connect.waiting')}</p>
        </div>
        <button type="button" className="btn notch__action" onClick={copy}>{copied ? t('copied') : t('connect.copy')}</button>
        {hide}
      </main>
    )
  }

  const name = safeText(state.name)
  if (state.hasOffer) {
    const title = t('offer.incoming', { name: safeText(state.peerName), item: name })
    return (
      <main className="notch notch--offer" aria-label={t('app.title')}>
        <div className="notch__text">
          <p className="notch__name" title={title}>{t('offer.incoming', { name: middleCut(safeText(state.peerName), 18), item: middleCut(name, 26) })}</p>
          <p className="notch__detail" role="status">{metaLine(t, lang, state)}</p>
        </div>
        <button type="button" className="btn notch__action" onClick={act('NotchSaveOffer', state.id)}>{t('offer.save')}</button>
        <button type="button" className="btn btn--ghost notch__action" onClick={act('NotchDeclineOffer', state.id)}>{t('offer.decline')}</button>
        <button type="button" className="btn btn--ghost notch__action" onClick={act('NotchOpenApp')}>{t('notch.open')}</button>
        {hide}
      </main>
    )
  }

  const failed = state.state === 'failed'
  const quiet = ['cancelled', 'stopped', 'declined', 'withdrawn'].includes(state.state)
  const finished = state.state === 'done' || state.state === 'delivered'
  const measured = state.active && state.percent >= 0
  const text = detail(t, fail, lang, state)
  const number = datum(lang, state)
  return (
    <main className="notch" aria-label={t('app.title')}>
      <Ring
        value={finished ? 1 : measured ? state.percent / 100 : 0}
        busy={state.active && !measured && state.state !== 'queued'}
        peers={state.active ? 1 : 0}
        tone={failed ? 'failed' : quiet ? 'muted' : ''}
        label={t('progress.label')}
        valueText={measured ? t('progress.percent', { n: Math.floor(state.percent) }) : text}
      />
      <div className="notch__text">
        <p className="notch__name" title={name}>{middleCut(name, 34)}</p>
        <p className={`notch__detail${failed ? ' is-failed' : ''}`} role="status" title={failed ? text : undefined}>{text}</p>
      </div>
      {number ? <p className="notch__datum">{number}</p> : null}
      <button type="button" className="btn btn--ghost notch__action" onClick={act('NotchOpenApp')}>{t('notch.open')}</button>
      {hide}
    </main>
  )
}
