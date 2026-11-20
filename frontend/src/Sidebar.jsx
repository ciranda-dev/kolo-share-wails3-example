import { AddMenu } from './AddMenu.jsx'
import { Avatar } from './Avatar.jsx'
import { useI18n } from './context.js'
import { formatCount } from './format.js'
import { ClockIcon, GearIcon, SidebarIcon } from './Icons.jsx'
import { Mark } from './Ring.jsx'
import { peerLabel } from './session.js'

/**
 * The sidebar, the full height of the window: the Kolo mark and the button that folds it to a rail of avatars, the
 * sessions (this list scrolls, the rest stays) with "+" beside the heading, and a card at the bottom with History,
 * Settings and one quiet line about the network. A session shows who it is, whether it is connected, and how many
 * items wait for an answer.
 */
export function Sidebar({ sessions, selected, collapsed, page, pageButtons, network, onToggle, onChoose, onSelect, onPage }) {
  const { t, lang } = useI18n()
  const toggle = t(collapsed ? 'sidebar.expand' : 'sidebar.collapse')
  return (
    <nav className={`side${collapsed ? ' is-collapsed' : ''}`} id="sessions-panel" aria-label={t('nav.sessions')}>
      <div className="side__brand">
        <Mark className="side__logo" />
        <span className="side__name">kolo</span>
        <span className="side__app">{t('app.tag')}</span>
        <button type="button" className="iconbtn" aria-label={toggle} title={toggle} aria-expanded={!collapsed} aria-controls="sessions-panel" onClick={onToggle}>
          <SidebarIcon />
        </button>
      </div>
      <div className="side__heading">
        <h2 className="side__title">{t('nav.sessions')}</h2>
        <AddMenu onChoose={onChoose} />
      </div>
      {sessions.length === 0 ? <p className="side__empty">{t('rail.empty')}</p> : null}
      <ul className="side__list">
        {sessions.map((s) => {
          const name = peerLabel(s)
          const active = s.id === selected
          const state = t(`rail.state.${s.state}`)
          return (
            <li key={s.id}>
              <button type="button" className={`sess${active ? ' is-active' : ''}`} aria-current={active ? 'page' : undefined} aria-label={collapsed ? `${name}, ${state}` : undefined} title={`${name}, ${state}`} onClick={() => onSelect(s.id)}>
                <span className="sess__avatar">
                  <Avatar name={name} seed={s.peerId || s.id} size={44} />
                  {s.waiting > 0 ? <span className="sess__dot" /> : null}
                </span>
                <span className="sess__text">
                  <span className="sess__top">
                    <span className="sess__name">{name}</span>
                    {s.waiting > 0 ? <span className="sess__pill" role="img" aria-label={t('rail.waiting', { n: s.waiting })}>{formatCount(s.waiting, lang)}</span> : null}
                  </span>
                  <span className={`sess__line is-${s.state}`}>{t(`session.state.${s.state}`)}</span>
                </span>
              </button>
            </li>
          )
        })}
      </ul>
      <div className="dock">
        <div className="dock__nav">
          <button type="button" className="dock__btn" ref={pageButtons.history} aria-pressed={page === 'history'} aria-label={collapsed ? t('nav.history') : undefined} title={t('nav.history')} onClick={() => onPage('history')}>
            <ClockIcon />
            <span className="dock__label">{t('nav.history')}</span>
          </button>
          <button type="button" className="dock__btn" ref={pageButtons.settings} aria-pressed={page === 'settings'} aria-label={collapsed ? t('nav.settings') : undefined} title={t('nav.settings')} onClick={() => onPage('settings')}>
            <GearIcon />
            <span className="dock__label">{t('nav.settings')}</span>
          </button>
        </div>
        <p className="dock__net" title={network.text}>
          <span className={`dock__dot is-${network.tone}`} aria-hidden="true" />
          <span className="dock__text">{network.text}</span>
        </p>
      </div>
    </nav>
  )
}
