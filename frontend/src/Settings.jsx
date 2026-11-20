import { useEffect, useRef, useState } from 'react'
import { useI18n } from './context.js'
import { formatBinary, formatCount, formatDuration, safeText } from './format.js'
import { languageNames, locales } from './i18n.js'
import { relayLine, relaySource } from './network.js'

/** Your name and short key: what the other side of a session sees of you. */
function YouForm({ settings, onName }) {
  const { t, fail, lang } = useI18n()
  const [name, setName] = useState(settings?.displayName ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const [saved, setSaved] = useState(false)
  const key = safeText(settings?.publicKey) || '-'

  const save = (ev) => {
    ev.preventDefault()
    setBusy(true)
    setError(null)
    setSaved(false)
    onName(name.trim())
      .then((next) => {
        setName(next?.displayName ?? '')
        setSaved(true)
      })
      .catch(setError)
      .finally(() => setBusy(false))
  }

  return (
    <form className="you" onSubmit={save}>
      <div>
        <label className="label" htmlFor="settings-name">{t('settings.name')}</label>
        <div className="form__row form__row--center">
          <input
            id="settings-name"
            className="field"
            value={name}
            onChange={(ev) => {
              setName(ev.target.value)
              setSaved(false)
            }}
            placeholder={t('settings.name.placeholder', { key })}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={error ? 'true' : undefined}
            aria-describedby="settings-name-note"
          />
          <button type="submit" className="btn btn--ghost" disabled={busy}>{t('settings.name.save')}</button>
        </div>
        <p className="field-note" id="settings-name-note">{t('settings.name.note')}</p>
        {error ? <p className="alert alert--plain" role="alert">{fail(error)}</p> : null}
        {saved ? <p className="note" role="status">{t('settings.name.saved')}</p> : null}
      </div>
      <dl className="facts">
        <div className="fact fact--wide">
          <dt className="label">{t('settings.key')}</dt>
          <dd className="key key--large key--fingerprint">{safeText(settings?.fingerprint) || '-'}</dd>
        </div>
        <div className="fact">
          <dt className="label">{t('settings.pairTTL')}</dt>
          <dd className="fact__value fact__value--large">{settings?.pairTTL > 0 ? formatDuration(settings.pairTTL, lang) : '-'}</dd>
        </div>
      </dl>
      <p className="field-note field-note--tight">{t('settings.key.note')}</p>
      <p className="field-note">{t('settings.autosave')}</p>
    </form>
  )
}

/** The network as the app started it: read only, it is set with flags when the app starts. */
function NetworkFacts({ settings, network }) {
  const { t, fail } = useI18n()
  const from = settings?.entryNodesFrom ?? 'none'
  const nodes = String(settings?.entryNodes ?? '').split(',').map((x) => x.trim()).filter(Boolean)
  let text
  let source = ''
  if (settings?.localNode) {
    text = t('settings.network.local', { addr: settings.localNode })
  } else if (settings?.mode === 'public') {
    text = t('settings.network.public')
  } else {
    text = t('settings.network.nodes', { list: nodes.join(', ') })
    if (from === 'flag' || from === 'env') {
      source = t(`settings.network.from.${from}`)
    }
  }
  const relays = Array.isArray(network?.relays) ? network.relays : []
  const relaysFrom = relaySource(t, settings?.connectionRelaysFrom)
  return (
    <div className="network-facts">
      <span className="label">{t('settings.network')}</span>
      <p className="network-facts__text">{text}{source ? <> <span className="nowrap">{source}</span></> : null}</p>
      {relays.length > 0 ? (
        <>
          <p className="network-facts__text">{t('settings.relays')}{relaysFrom ? <> <span className="nowrap">{relaysFrom}</span></> : null}</p>
          {relays.map((r) => <p key={r.key} className="field-note">{relayLine(t, fail, r)}</p>)}
        </>
      ) : null}
      {settings?.firstNode ? <p className="field-note">{t('settings.firstNode', { addr: settings.firstNode })}</p> : null}
      <p className="field-note">{t('settings.network.note')}</p>
    </div>
  )
}

/** What a person sets in the window, and the network the app was started with. */
function SettingsPanel({ settings, network, version, locale, theme, onLocale, onTheme, onNotch, onName }) {
  const { t, fail, lang } = useI18n()
  const [error, setError] = useState(null)
  return (
    <div className="settings">
      <div className="settings__region">
        <YouForm settings={settings} onName={onName} />
        <NetworkFacts settings={settings} network={network} />
      </div>
      <div className="settings__region">
        <div className="settings__grid">
          <div>
            <label className="label" htmlFor="settings-language">{t('settings.language')}</label>
            <select id="settings-language" className="field" value={locale} onChange={(ev) => onLocale(ev.target.value)}>
              {locales.map((code) => (
                <option key={code} value={code}>{languageNames[code]}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="label" htmlFor="settings-theme">{t('settings.theme')}</label>
            <select id="settings-theme" className="field" value={theme} onChange={(ev) => onTheme(ev.target.value)}>
              <option value="system">{t('theme.system')}</option>
              <option value="light">{t('theme.light')}</option>
              <option value="dark">{t('theme.dark')}</option>
            </select>
          </div>
        </div>
        <div>
          <label className="check">
            <input
              type="checkbox"
              checked={settings?.showInNotch ?? true}
              onChange={(ev) => onNotch(ev.target.checked).catch(setError)}
              aria-describedby="settings-notch-note"
            />
            <span>{t('settings.notch')}</span>
          </label>
          <p className="field-note" id="settings-notch-note">{t('settings.notch.note')}</p>
          {error ? <p className="alert alert--plain" role="alert">{fail(error)}</p> : null}
        </div>
        {version ? (
          <p className="note settings__about">
            {t('settings.about', {
              v: version,
              bytes: settings?.maxDownload > 0 ? formatBinary(settings.maxDownload, lang) : '-',
              files: settings?.maxFiles > 0 ? formatCount(settings.maxFiles, lang) : '-',
            })}
          </p>
        ) : null}
      </div>
    </div>
  )
}

/**
 * Settings as a page of the window: Close or Escape goes back. Focus moves to the title when it
 * opens, so nothing is activated by accident.
 */
export function SettingsPage({ onClose, ...panel }) {
  const { t } = useI18n()
  const title = useRef(null)
  useEffect(() => {
    title.current?.focus()
  }, [])
  return (
    <main
      className="main page"
      aria-labelledby="settings-title"
      onKeyDown={(ev) => {
        if (ev.key === 'Escape') {
          ev.stopPropagation()
          onClose()
        }
      }}
    >
      <div className="bar">
        <h1 className="page__title" id="settings-title" tabIndex={-1} ref={title}>{t('settings.title')}</h1>
        <button type="button" className="pill" onClick={onClose}>{t('nav.close')}</button>
      </div>
      <SettingsPanel {...panel} />
    </main>
  )
}
