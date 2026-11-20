import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { call, on, copyText, pasteText } from './service.js'
import { I18n } from './context.js'
import { htmlLang, pickLocale, translate } from './i18n.js'
import { safeText } from './format.js'
import { list } from './lists.js'
import { errorSentence } from './errors.js'
import { loadPrefs, savePrefs } from './prefs.js'
import { canStart, networkLine, newerNetwork, startingText } from './network.js'
import { FINAL, codeToCancel, keepTransfers, newerPairing, peerLabel, statusText } from './session.js'
import { Modal } from './Modal.jsx'
import { Sidebar } from './Sidebar.jsx'
import { ConnectPage, PasteForm, ShowForm } from './Connect.jsx'
import { SessionPage } from './Session.jsx'
import { HistoryPage } from './History.jsx'
import { SettingsPage } from './Settings.jsx'
import { readCollapsed, writeCollapsed } from './sidebar.js'
import './share.css'

const NO_PAIRING = { id: '', role: '', state: '', code: '', expires: 0, error: null, sessionId: '' }

/** How many items of a session's own history its page lists. */
const SESSION_HISTORY = 8

function App() {
  const [prefs, setPrefs] = useState(loadPrefs)
  const locale = pickLocale(prefs.lang, window.navigator.languages ?? [])
  const theme = prefs.theme === 'light' || prefs.theme === 'dark' ? prefs.theme : 'system'
  const lang = htmlLang[locale]
  const t = useCallback((key, vars) => translate(locale, key, vars), [locale])
  const fail = useCallback((err, fallback) => errorSentence(t, locale, err, fallback), [t, locale])
  const i18n = useMemo(() => ({ t, fail, lang }), [t, fail, lang])

  const [info, setInfo] = useState(null)
  const [network, setNetwork] = useState({ state: 'starting', message: '', entryNodes: 0, firstNode: '', since: 0, seq: 0 })
  const [now, setNow] = useState(Date.now)
  const [fatal, setFatal] = useState(null)
  const [reconnectError, setReconnectError] = useState(null)
  const [transfers, setTransfers] = useState([])
  const [history, setHistory] = useState([])
  const [settings, setSettings] = useState(null)
  const [sessions, setSessions] = useState([])
  const [pairing, setPairing] = useState(NO_PAIRING)
  const [selected, setSelected] = useState('')
  const [page, setPage] = useState('')
  const [code, setCode] = useState('')
  const [showError, setShowError] = useState(null)
  const [joinError, setJoinError] = useState(null)
  const [dropError, setDropError] = useState(null)
  const [sessionErrors, setSessionErrors] = useState({})
  const [rowErrors, setRowErrors] = useState({})
  const [closing, setClosing] = useState('')
  const [modal, setModal] = useState('') // '', 'show' or 'join': the dialog of the two ways to connect
  const [collapsed, setCollapsed] = useState(() => readCollapsed())
  const [spoken, setSpoken] = useState('')
  const said = useRef({})
  const seen = useRef(new Set())
  const view = useRef({})
  const returnTo = useRef(null)
  const pageButtons = { history: useRef(null), settings: useRef(null) }

  const update = (patch) => {
    setPrefs((prev) => {
      const next = { ...prev, ...patch }
      savePrefs(next)
      return next
    })
  }

  useEffect(() => {
    document.documentElement.lang = lang
    document.title = t('app.title')
    if (theme === 'system') {
      delete document.documentElement.dataset.theme
    } else {
      document.documentElement.dataset.theme = theme
    }
  }, [lang, theme, t])

  const upsert = (transfer) => {
    if (!transfer?.id) {
      return
    }
    setTransfers((prev) => keepTransfers(prev, transfer))
  }

  const current = page === '' ? sessions.find((s) => s.id === selected) ?? null : null
  view.current = { current }

  // A code dies when the person closes the dialog that shows it.
  const showOpen = modal === 'show'
  const wasShowOpen = useRef(showOpen)
  useEffect(() => {
    if (codeToCancel(wasShowOpen.current, showOpen, pairing)) {
      call('CancelPairing', pairing.id).catch(() => {})
    }
    wasShowOpen.current = showOpen
  }, [showOpen, pairing])

  useEffect(() => {
    const offs = [
      on('share:transfer', upsert),
      on('share:history', (items) => setHistory(list(items))),
      on('share:network', (n) => setNetwork((prev) => newerNetwork(prev, n))),
      on('share:sessions', (items) => setSessions(list(items))),
      on('share:pairing', (p) => setPairing((prev) => newerPairing(prev, p))),
      on('share:dropped', (paths) => {
        const target = view.current.current
        if (!target || target.state === 'ended') {
          setDropError({ key: 'connect.first' })
          return
        }
        for (const path of list(paths)) {
          send(target.id, path)
        }
      }),
    ]
    call('Start')
      .then((started) => {
        setInfo(started)
        setNetwork((prev) => newerNetwork(prev, started?.network))
        return Promise.all([call('Transfers'), call('History'), call('Settings'), call('Sessions'), call('Pairing')])
      })
      .then(([active, items, current, open, now]) => {
        setTransfers((prev) => {
          const known = new Set(prev.map((x) => x.id))
          return [...prev, ...list(active).filter((x) => !known.has(x.id))]
        })
        setHistory(list(items))
        setSettings(current)
        setSessions((prev) => (prev.length > 0 ? prev : list(open)))
        setPairing((prev) => newerPairing(prev, now?.state ? now : null))
        // A pairing that already runs shows in its dialog.
        if (now?.role === 'inviter' && ['starting', 'waiting'].includes(now.state)) {
          setModal('show')
        } else if (now?.role === 'joiner' && ['looking', 'joining'].includes(now.state)) {
          setModal('join')
        }
      })
      .catch(setFatal)
    return () => offs.forEach((off) => off())
    // The service is started once; the handlers only use state setters and refs.
  }, [])

  // A new session opens its page; a dismissed one goes back to Connect.
  useEffect(() => {
    if (pairing.state === 'connected' && pairing.sessionId) {
      setSelected(pairing.sessionId)
      setPage('')
      setModal('')
      setCode('')
      setPairing(NO_PAIRING)
    } else if (pairing.state === 'cancelled') {
      setPairing(NO_PAIRING)
    }
  }, [pairing])

  useEffect(() => {
    for (const s of sessions) {
      seen.current.add(s.id)
    }
    if (selected && seen.current.has(selected) && !sessions.some((s) => s.id === selected)) {
      setSelected('')
    }
  }, [sessions, selected])

  useEffect(() => setReconnectError(null), [network.seq])

  const starting = network.state === 'starting' && !fatal
  const waitingCode = pairing.state === 'waiting'
  // The clock of the starting sentence and of the code's countdown; it stops when neither shows.
  useEffect(() => {
    if (!starting && !waitingCode) {
      return undefined
    }
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [starting, waitingCode])

  // One polite sentence per change of state of a transfer, not one per progress event.
  useEffect(() => {
    const live = new Set(transfers.map((x) => x.id))
    for (const id of Object.keys(said.current)) {
      if (!live.has(id)) {
        delete said.current[id]
      }
    }
    for (const x of transfers) {
      const before = said.current[x.id]
      if (before === x.state) {
        continue
      }
      said.current[x.id] = x.state
      if (before === undefined && FINAL.includes(x.state)) {
        continue
      }
      const session = sessions.find((s) => s.id === x.sessionId)
      const name = session ? peerLabel(session) : safeText(x.peerName)
      setSpoken(`${safeText(x.name)}: ${statusText(t, fail, lang, x, name)}`)
    }
  }, [transfers, sessions, t, fail, lang])

  const setSessionError = (id, err) => setSessionErrors((prev) => ({ ...prev, [id]: err }))
  const setRowError = (id, err) => setRowErrors((prev) => ({ ...prev, [id]: err }))
  const replaceSession = (next) => {
    if (next?.id) {
      setSessions((prev) => prev.map((s) => (s.id === next.id ? next : s)))
    }
  }

  function send(sessionId, path) {
    if (!path) {
      return
    }
    setSessionError(sessionId, null)
    call('Send', sessionId, path).catch((err) => setSessionError(sessionId, err))
  }

  const canAsk = canStart(network, fatal)

  const openModal = (kind, opener) => {
    returnTo.current = opener
    setShowError(null)
    setJoinError(null)
    setDropError(null)
    setModal(kind)
  }

  const toggleSidebar = () => {
    writeCollapsed(!collapsed)
    setCollapsed(!collapsed)
  }

  const createPairing = () => {
    setShowError(null)
    setJoinError(null)
    setDropError(null)
    call('CreatePairing').then((p) => setPairing((prev) => newerPairing(prev, p))).catch(setShowError)
  }

  const cancelPairing = (id) => {
    call('CancelPairing', id).then(() => setPairing((prev) => (prev.id === id ? NO_PAIRING : prev))).catch(setShowError)
  }

  const joinPairing = () => {
    setDropError(null)
    if (!code.trim()) {
      setJoinError({ key: 'connect.needCode' })
      return
    }
    setJoinError(null)
    call('JoinPairing', code).then((p) => setPairing((prev) => newerPairing(prev, p))).catch(setJoinError)
  }

  const editCode = (value) => {
    setCode(value)
    setJoinError(null)
    setPairing((prev) => (prev.role === 'joiner' && prev.state === 'failed' ? NO_PAIRING : prev))
  }

  const saveName = (name) =>
    call('SetDisplayName', name).then((next) => {
      setSettings(next)
      return next
    })

  const togglePage = (name) => {
    if (page === name) {
      setPage('')
      pageButtons[name].current?.focus()
      return
    }
    if (name === 'settings') {
      call('Settings').then(setSettings).catch(() => {})
    }
    setPage(name)
  }

  const closePage = () => {
    const was = page
    setPage('')
    pageButtons[was]?.current?.focus()
  }

  const openSession = (id) => {
    setPage('')
    setSelected(id)
    setDropError(null)
  }

  const reconnect = () => {
    setReconnectError(null)
    call('Reconnect').catch(setReconnectError)
  }

  const connected = sessions.filter((s) => s.state === 'connected').length
  const badge = fatal || network.state === 'failed'
    ? { tone: 'error', text: t('badge.error') }
    : network.state === 'starting'
      ? { tone: 'waiting', text: t('badge.starting') }
      : connected > 0
        ? { tone: 'live', text: t('badge.sessions', { n: connected }) }
        : waitingCode
          ? { tone: 'waiting', text: t('badge.code') }
          : { tone: 'waiting', text: t('badge.ready') }

  // A network that is up and waiting for a connection is fine, not a warning: only starting and failing stand out.
  const line = {
    text: networkLine(t, network.mode, badge.text),
    tone: badge.tone === 'waiting' && network.state === 'ready' && !fatal ? 'live' : badge.tone,
  }

  const notice = fatal
    ? { tone: 'error', text: fail(fatal) }
    : network.state === 'failed'
      ? { tone: 'error', text: fail(network.error, network.message), retry: true }
      : starting
        ? { tone: 'plain', ...startingText(t, lang, network, now) }
        : network.state === 'ready' && network.localNode
          ? { tone: 'plain', text: reconnectError ? fail(reconnectError) : t('network.local.ready'), reconnect: true }
          : null

  const noticeBand = notice ? (
    <div className={`notice is-${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}>
      <p>
        {notice.text}
        {/* Hidden from the live region, which would read it out every second. */}
        {notice.time ? <span className="notice__time" aria-hidden="true"> · {notice.time}</span> : null}
      </p>
      {notice.reconnect || notice.retry ? (
        <span className="notice__actions">
          {notice.reconnect ? <button type="button" className="btn btn--ghost btn--small" onClick={reconnect}>{t('network.reconnect')}</button> : null}
          {notice.retry ? <button type="button" className="btn btn--ghost btn--small" onClick={() => call('Reconnect').catch(setFatal)}>{t('network.retry')}</button> : null}
        </span>
      ) : null}
    </div>
  ) : null

  const reveal = (path) => call('Reveal', path).catch((err) => setSpoken(fail(err)))
  const closingSession = sessions.find((s) => s.id === closing)

  let content
  if (page === 'settings') {
    content = (
      <SettingsPage
        key={settings ? 'ready' : 'loading'}
        settings={settings}
        network={network}
        version={info?.version}
        locale={locale}
        theme={theme}
        onLocale={(value) => update({ lang: value })}
        onTheme={(value) => update({ theme: value })}
        onName={saveName}
        onNotch={(value) => call('SetShowInNotch', value).then(setSettings)}
        onClose={closePage}
      />
    )
  } else if (page === 'history') {
    content = <HistoryPage items={history} onReveal={reveal} onClear={() => call('ClearHistory').catch(() => {})} onClose={closePage} />
  } else if (current) {
    const id = current.id
    const mine = transfers.filter((x) => x.sessionId === id)
    const ids = new Set(mine.map((x) => x.id))
    content = (
      <SessionPage
        key={id}
        session={current}
        transfers={mine}
        history={history.filter((h) => h.peerId && h.peerId === current.peerId && !ids.has(h.id)).slice(0, SESSION_HISTORY)}
        settings={settings}
        notice={noticeBand}
        error={sessionErrors[id]}
        errors={rowErrors}
        onPickFile={() => call('PickFile').then((path) => send(id, path)).catch((err) => setSessionError(id, err))}
        onPickFolder={() => call('PickFolder').then((path) => send(id, path)).catch((err) => setSessionError(id, err))}
        onChangeFolder={() => {
          setSessionError(id, null)
          call('PickSaveFolder')
            .then((path) => (path ? call('SetSessionFolder', id, path) : null))
            .then(replaceSession)
            .catch((err) => setSessionError(id, err))
        }}
        onAutoSave={(value) => {
          setSessionError(id, null)
          call('SetAutoSave', id, value).then(replaceSession).catch((err) => setSessionError(id, err))
        }}
        onClose={() => setClosing(id)}
        onDismiss={() => call('CloseSession', id).then(() => setSelected('')).catch((err) => setSessionError(id, err))}
        onSave={(x) => {
          setRowError(x, null)
          call('SaveOffer', x).catch((err) => setRowError(x, err))
        }}
        onDecline={(x) => {
          setRowError(x, null)
          call('DeclineOffer', x).catch((err) => setRowError(x, err))
        }}
        onCancel={(x) => {
          setRowError(x, null)
          call('CancelTransfer', x).catch((err) => setRowError(x, err))
        }}
        onReveal={(path) => call('Reveal', path).catch((err) => setSessionError(id, err))}
      />
    )
  } else {
    content = <ConnectPage dropError={dropError} notice={noticeBand} onOpen={openModal} />
  }

  return (
    <I18n.Provider value={i18n}>
      <div className="app">
        <div className="titlebar" />
        <div className="body">
          <Sidebar
            sessions={sessions}
            selected={page === '' && current ? current.id : ''}
            collapsed={collapsed}
            page={page}
            pageButtons={pageButtons}
            network={line}
            onToggle={toggleSidebar}
            onChoose={openModal}
            onSelect={openSession}
            onPage={togglePage}
          />
          {content}
        </div>

        {modal === 'show' ? (
          <Modal title={t('connect.show')} returnFocus={returnTo} onClose={() => setModal('')}>
            <ShowForm pairing={pairing} now={now} ready={canAsk} busy={pairing.role === 'joiner' && ['looking', 'joining'].includes(pairing.state)} error={showError} onCreate={createPairing} onCancel={cancelPairing} onCopy={(text) => copyText(text).then(() => setSpoken(t('connect.copied')))} />
          </Modal>
        ) : null}
        {modal === 'join' ? (
          <Modal title={t('connect.enter')} returnFocus={returnTo} onClose={() => setModal('')}>
            <PasteForm
              pairing={pairing}
              code={code}
              ready={canAsk}
              busy={pairing.role === 'inviter' && ['starting', 'waiting'].includes(pairing.state)}
              error={joinError}
              onCode={editCode}
              onPaste={() => pasteText().then((text) => editCode(String(text ?? '').trim())).catch(setJoinError)}
              onJoin={joinPairing}
              onCancel={cancelPairing}
            />
          </Modal>
        ) : null}
        {closingSession ? (
          <Modal title={t('session.close')} onClose={() => setClosing('')}>
            <p className="modal__text">{t('session.close.confirm', { name: peerLabel(closingSession) })}</p>
            <div className="modal__actions">
              <button type="button" className="btn btn--ghost" onClick={() => setClosing('')}>{t('session.keep')}</button>
              <button
                type="button"
                className="btn"
                onClick={() => {
                  setClosing('')
                  call('CloseSession', closingSession.id).catch((err) => setSessionError(closingSession.id, err))
                }}
              >
                {t('session.close')}
              </button>
            </div>
          </Modal>
        ) : null}
        <p className="sr-only" role="status" aria-live="polite">{spoken}</p>
      </div>
    </I18n.Provider>
  )
}

export default App
