import { Avatar } from './Avatar.jsx'
import { useI18n } from './context.js'
import { formatBinary, formatRate, safeText } from './format.js'
import { Name } from './Name.jsx'
import { Mark } from './Ring.jsx'
import { HistoryList } from './History.jsx'
import { KindIcon, PathText, Progress, Status, metaLine, platform } from './parts.jsx'
import { FINAL, endSentence, numbersText, peerLabel, rowView, statusText, unreachableText } from './session.js'

/** Items that need an answer first, then running ones, oldest first as the queue runs; then finished ones, newest first. */
function ordered(list) {
  const asks = (x) => (x.direction === 'receive' && x.state === 'offered' ? 0 : 1)
  const open = list.filter((x) => !FINAL.includes(x.state)).sort((a, b) => asks(a) - asks(b) || a.started - b.started)
  const done = list.filter((x) => FINAL.includes(x.state)).sort((a, b) => (b.finished || b.started) - (a.finished || a.started))
  return [...open, ...done]
}

/** One item of a session: what it is, where it stands, and what can be done with it now. */
function TransferRow({ transfer, name, offline, error, onSave, onDecline, onCancel, onReveal }) {
  const { t, fail, lang } = useI18n()
  const { state, direction } = transfer
  const receive = direction === 'receive'
  const status = statusText(t, fail, lang, transfer, name)
  const view = rowView(transfer)
  const numbers = numbersText(t, lang, transfer, name, formatRate)
  return (
    <li className={`row is-${state}`}>
      <span className="row__icon"><KindIcon kind={transfer.kind} /></span>
      <div className="row__main">
        <Name value={transfer.name} max={64} />
        <span className="row__meta">{metaLine(t, lang, transfer)}</span>
        <Status state={transfer.stalled ? 'waiting' : state} className="row__state">{status}</Status>
      </div>
      <div className="row__actions">
        {view.save ? <button type="button" className="btn btn--small" disabled={offline} onClick={() => onSave(transfer.id)}>{t('offer.save')}</button> : null}
        {view.decline ? <button type="button" className="btn btn--ghost btn--small" onClick={() => onDecline(transfer.id)}>{t('offer.decline')}</button> : null}
        {view.cancel ? <button type="button" className="btn btn--ghost btn--small" onClick={() => onCancel(transfer.id)}>{t('transfer.cancel')}</button> : null}
        {receive && state === 'done' && transfer.savedTo ? <button type="button" className="btn btn--ghost btn--small" onClick={() => onReveal(transfer.savedTo)}>{t(`reveal.${platform()}`)}</button> : null}
      </div>
      {view.progress ? (
        <div className="row__progress">
          <Progress done={transfer.bytesDone} total={transfer.bytesTotal} />
          {numbers ? <span className="row__numbers">{numbers}</span> : null}
        </div>
      ) : null}
      {error ? <p className="alert alert--plain row__error" role="alert">{fail(error)}</p> : null}
    </li>
  )
}

/** One direction of the session as a list of rows. */
function TransferList({ id, title, items, empty, ...row }) {
  return (
    <section className={`list list--${id}`} aria-labelledby={`list-${id}`}>
      <h2 className="list__title" id={`list-${id}`}>
        {title}
        {items.length > 0 ? <span className="list__count">{items.length}</span> : null}
      </h2>
      {items.length > 0 ? (
        <ul className="list__rows">
          {items.map((x) => <TransferRow key={x.id} transfer={x} error={row.errors[x.id]} {...row} />)}
        </ul>
      ) : (
        <p className="list__empty">{empty}</p>
      )}
    </section>
  )
}

/** "Save into {folder}" with the path cut at its start, so the folder that matters stays visible. */
function FolderText({ folder }) {
  const { t } = useI18n()
  if (!folder) {
    return <span className="session__folder-text">{t('session.folder.none')}</span>
  }
  const [before, after = ''] = t('session.folder').split('{folder}')
  return (
    <span className="session__folder-text">
      {before}
      <PathText value={folder} />
      {after}
    </span>
  )
}

/**
 * A session with one other computer. The header says who it is and how the session stands; the
 * body takes drops and lists what goes each way. An ended session stays, read only, until dismissed.
 */
export function SessionPage({ session, transfers, history, settings, notice, error, errors, onPickFile, onPickFolder, onChangeFolder, onAutoSave, onClose, onDismiss, onSave, onDecline, onCancel, onReveal }) {
  const { t, fail, lang } = useI18n()
  const name = peerLabel(session)
  const ended = session.state === 'ended'
  const received = ordered(transfers.filter((x) => x.direction === 'receive'))
  const sent = ordered(transfers.filter((x) => x.direction === 'send'))
  const empty = transfers.length === 0
  const row = { name, offline: session.state !== 'connected', errors, onSave, onDecline, onCancel, onReveal }
  const limit = settings?.maxDownload > 0 ? formatBinary(settings.maxDownload, lang) : '-'
  const declined = session.notice
    ? session.notice.reason === 'offer-limit' ? fail(session.notice) : t('session.declined', { name, reason: fail(session.notice) })
    : ''

  const choose = (
    <div className="drop__actions">
      <button type="button" className={`btn${empty ? '' : ' btn--ghost'}`} onClick={onPickFile}>{t('session.chooseFile')}</button>
      <button type="button" className="btn btn--ghost" onClick={onPickFolder}>{t('session.chooseFolder')}</button>
    </div>
  )

  return (
    <main className="main session" aria-labelledby="session-title">
      <div className="session__head">
        <Avatar name={name} seed={session.peerId || session.id} size={48} />
        <div className="session__who">
          <div className="session__line">
            <h1 className="session__name" id="session-title" title={name}>{name}</h1>
            <span className="key" title={t('session.key', { name })}>{safeText(session.peerKey)}</span>
          </div>
          <div className="session__sub">
            <Status state={session.state}>{t(`session.state.${session.state}`)}</Status>
            <span className="fingerprint" title={t('session.fingerprint.note', { name })}>
              <span className="fingerprint__label">{t('session.fingerprint')}</span> {safeText(session.fingerprint)}
            </span>
          </div>
        </div>
        {ended ? (
          <button type="button" className="pill" onClick={onDismiss}>{t('session.dismiss')}</button>
        ) : (
          <button type="button" className="pill" onClick={onClose}>{t('session.close')}</button>
        )}
      </div>
      {notice}
      {ended && !empty ? <p className="band" role="status">{endSentence(t, session)}</p> : null}
      {!ended && session.state === 'reconnecting' ? <p className="band band--warn" role="status">{t('session.reconnecting', { name })}</p> : null}
      {!ended && session.unreachable ? (
        <p className="band band--warn" role="status">
          {unreachableText(t, name, Boolean(settings?.connectionRelays))}
        </p>
      ) : null}
      {declined ? <p className="band" role="status">{declined}</p> : null}
      {error ? <p className="band band--alert" role="alert">{fail(error)}</p> : null}

      <div className="session__body" data-file-drop-target={ended ? undefined : true}>
        {ended ? null : (
          <div className="session__save">
            <div className="session__folder">
              <FolderText folder={session.folder} />
              <button type="button" className="btn btn--ghost btn--small" onClick={onChangeFolder}>{t('session.folder.change')}</button>
            </div>
            <div className="session__auto">
              <label className="switch">
                <input type="checkbox" role="switch" checked={Boolean(session.autoSave)} onChange={(ev) => onAutoSave(ev.target.checked)} aria-describedby="session-auto-note" />
                <span className="switch__track" aria-hidden="true" />
                <span>{t('session.autosave', { name })}</span>
              </label>
              <p className="field-note" id="session-auto-note">{t('session.autosave.note', { limit })}</p>
            </div>
          </div>
        )}
        {ended && empty ? (
          <div className="drop drop--ended">
            <Mark className="drop__ring" />
            <p className="drop__title" role="status">{endSentence(t, session)}</p>
            <div className="drop__actions">
              <button type="button" className="btn" onClick={onDismiss}>{t('session.dismiss')}</button>
            </div>
          </div>
        ) : ended ? null : empty ? (
          <div className="drop">
            <Mark className="drop__ring" />
            <h2 className="drop__title">
              <span className="drop__rest">{t('session.drop', { name })}</span>
              <span className="drop__over">{t('session.drop.over', { name })}</span>
            </h2>
            {choose}
          </div>
        ) : (
          <div className="drop drop--strip">
            <p className="drop__title">
              <span className="drop__rest">{t('session.drop', { name })}</span>
              <span className="drop__over">{t('session.drop.over', { name })}</span>
            </p>
            {choose}
          </div>
        )}
        {empty ? null : (
          <div className="session__lists">
            <TransferList id="received" title={t('session.received')} items={received} empty={t('session.received.empty', { name })} {...row} />
            <TransferList id="sent" title={t('session.sent')} items={sent} empty={t('session.sent.empty')} {...row} />
          </div>
        )}
        {history.length > 0 ? (
          <section className="session__history" aria-labelledby="session-history">
            <h2 className="list__title" id="session-history">{t('session.history', { name })}</h2>
            <HistoryList items={history} onReveal={onReveal} />
          </section>
        ) : null}
      </div>
    </main>
  )
}
