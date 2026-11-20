import { useEffect, useRef, useState } from 'react'
import { useI18n } from './context.js'
import { CodeIcon, LinkIcon, PlusIcon } from './Icons.jsx'
import { menuStep } from './focus.js'

/**
 * The "+" next to Sessions. It opens a small menu with the two ways to connect, Show a code and Paste a code; a
 * row hands its choice and the "+" button to onChoose, which opens the dialog. The menu closes on Escape and on a
 * click outside, takes the arrow keys, Home, End and Enter, and gives the focus back to the button.
 */
export function AddMenu({ onChoose }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const button = useRef(null)
  const menu = useRef(null)

  const items = () => [...(menu.current?.querySelectorAll('[role="menuitem"]:not(:disabled)') ?? [])]
  const close = (refocus) => {
    setOpen(false)
    if (refocus) {
      button.current?.focus()
    }
  }

  useEffect(() => {
    if (!open) {
      return undefined
    }
    ;(items()[0] ?? menu.current)?.focus()
    const away = (ev) => {
      if (!menu.current?.contains(ev.target) && !button.current?.contains(ev.target)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', away)
    return () => document.removeEventListener('mousedown', away)
  }, [open])

  const onKey = (ev) => {
    if (ev.key === 'Escape') {
      ev.preventDefault()
      ev.stopPropagation()
      close(true)
    } else if (ev.key === 'Tab') {
      close(false)
    } else {
      const list = items()
      const next = menuStep(list.indexOf(document.activeElement), list.length, ev.key)
      if (next >= 0) {
        ev.preventDefault()
        list[next].focus()
      }
    }
  }

  const choose = (kind) => {
    setOpen(false)
    onChoose(kind, button.current)
  }

  return (
    <div className="add">
      <button
        type="button"
        ref={button}
        className="iconbtn iconbtn--box"
        aria-label={t('connect.new')}
        title={t('connect.new')}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        onKeyDown={(ev) => {
          if (ev.key === 'ArrowDown' && !open) {
            ev.preventDefault()
            setOpen(true)
          }
        }}
      >
        <PlusIcon />
      </button>
      {open ? (
        <div className="popover" role="menu" aria-label={t('connect.new')} ref={menu} tabIndex={-1} onKeyDown={onKey}>
          <button type="button" role="menuitem" className="choice" onClick={() => choose('show')}>
            <span className="choice__icon"><CodeIcon size={20} /></span>
            <span className="choice__text"><span className="choice__title">{t('connect.show')}</span><span className="choice__sub">{t('connect.show.lead')}</span></span>
          </button>
          <button type="button" role="menuitem" className="choice" onClick={() => choose('join')}>
            <span className="choice__icon"><LinkIcon size={20} /></span>
            <span className="choice__text"><span className="choice__title">{t('connect.enter')}</span><span className="choice__sub">{t('connect.enter.lead')}</span></span>
          </button>
        </div>
      ) : null}
    </div>
  )
}
