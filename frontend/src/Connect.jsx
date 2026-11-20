import { useI18n } from './context.js'
import { codeGroups, formatCountdown } from './format.js'
import { Mark } from './Ring.jsx'
import { CopyButton, Status } from './parts.jsx'

/** Show a code: make one, show it while it waits, and say how long it lives. It fills the dialog of Show a code. */
export function ShowForm({ pairing, now, ready, busy, error, onCreate, onCancel, onCopy }) {
  const { t, fail } = useI18n()
  const mine = pairing?.role === 'inviter'
  const waiting = mine && pairing.state === 'waiting' && Boolean(pairing.code)
  const starting = mine && (pairing.state === 'starting' || (pairing.state === 'waiting' && !pairing.code))
  const failed = mine && (pairing.state === 'failed' || pairing.state === 'expired')
  return (
    <section className="connect__form connect__show">
      <p className="connect__lead">{t('connect.show.lead')}</p>
      <div className="connect__stage">
        {waiting ? (
          <>
            <span className="sr-only" id="connect-code">{t('connect.code')}</span>
            <div className="connect__code" role="textbox" aria-readonly="true" aria-multiline="true" aria-labelledby="connect-code" tabIndex={0}>
              {codeGroups(pairing.code).map((group, i) => <span key={i} className="connect__group">{group}</span>)}
            </div>
            <div className="connect__row">
              <CopyButton text={pairing.code} label={t('connect.copy')} onCopy={onCopy} />
              {pairing.expires > 0 ? (
                // The countdown is not a live region: it would be read out every second.
                <span className="connect__expires">{t('connect.expires', { time: formatCountdown(pairing.expires - now) })}</span>
              ) : null}
            </div>
            <Status state="waiting" className="connect__status">{t('connect.waiting')}</Status>
          </>
        ) : starting ? (
          <Status state="waiting" className="connect__status">{t('connect.starting')}</Status>
        ) : (
          <>
            {failed ? <p className="alert" role="alert">{fail(pairing.error, t('state.failed'))}</p> : null}
            {error ? <p className="alert" role="alert">{fail(error)}</p> : null}
            <button type="button" className="btn btn--wide btn--large" onClick={onCreate} disabled={!ready || busy}>
              {failed ? t('connect.again') : t('connect.make')}
            </button>
          </>
        )}
      </div>
      <div className="connect__foot">
        <p className="field-note">{t('connect.note')}</p>
        {waiting || starting ? <button type="button" className="btn btn--ghost" onClick={() => onCancel(pairing.id)}>{t('connect.cancel')}</button> : null}
      </div>
    </section>
  )
}

/** Paste a code: the code of the other person, then connect. It fills the dialog of Paste a code. */
export function PasteForm({ pairing, code, ready, busy, error, onCode, onPaste, onJoin, onCancel }) {
  const { t, fail } = useI18n()
  const mine = pairing?.role === 'joiner'
  const active = mine && (pairing.state === 'looking' || pairing.state === 'joining')
  const failure = error ?? (mine && pairing.state === 'failed' ? pairing.error : null)
  return (
    <form
      className="connect__form connect__paste"
      onSubmit={(ev) => {
        ev.preventDefault()
        onJoin()
      }}
    >
      <p className="connect__lead">{t('connect.enter.lead')}</p>
      <textarea
        id="connect-code-field"
        className="field field--mono connect__field"
        value={code}
        onChange={(ev) => onCode(ev.target.value)}
        placeholder={t('connect.placeholder')}
        aria-label={t('connect.enter')}
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        readOnly={active}
        aria-invalid={failure ? 'true' : undefined}
        aria-describedby={failure ? 'connect-error' : undefined}
      />
      {failure ? <p className="alert alert--plain" id="connect-error" role="alert">{fail(failure)}</p> : null}
      {active ? <Status state="waiting" className="connect__status">{t(pairing.state === 'joining' ? 'connect.joining' : 'connect.looking')}</Status> : null}
      <div className="connect__foot">
        <p className="field-note">{t('connect.trust')}</p>
        <span className="connect__actions">
          {active ? (
            <button type="button" className="btn btn--ghost" onClick={() => onCancel(pairing.id)}>{t('connect.cancel')}</button>
          ) : (
            <>
              <button type="button" className="btn btn--ghost" onClick={onPaste}>{t('connect.paste')}</button>
              <button type="submit" className="btn btn--wide" disabled={!ready || busy}>{t('connect.join')}</button>
            </>
          )}
        </span>
      </div>
    </form>
  )
}

/**
 * The page of the window while no session is open: what connecting is, and the two ways to do it, each opening its
 * dialog. Dropped files land here too, and are told to connect first.
 */
export function ConnectPage({ dropError, notice, onOpen }) {
  const { t, fail } = useI18n()
  return (
    <main className="main connect-page" aria-labelledby="connect-title">
      <div className="bar">
        <h1 className="page__title" id="connect-title">{t('connect.title')}</h1>
        <p className="hint">{t('connect.lead')}</p>
      </div>
      {notice}
      {dropError ? <p className="band band--alert" role="alert">{fail(dropError)}</p> : null}
      <div className="connect" data-file-drop-target>
        {['show', 'join'].map((kind) => (
          <section key={kind} className="connect__half" aria-labelledby={`connect-${kind}`}>
            <Mark className="connect__mark" />
            <h2 className="connect__title" id={`connect-${kind}`}>{t(kind === 'show' ? 'connect.show' : 'connect.enter')}</h2>
            <p className="connect__lead">{t(kind === 'show' ? 'connect.show.lead' : 'connect.enter.lead')}</p>
            <button type="button" className="btn btn--large" onClick={(ev) => onOpen(kind, ev.currentTarget)}>{t(kind === 'show' ? 'connect.show' : 'connect.enter')}</button>
          </section>
        ))}
      </div>
    </main>
  )
}
