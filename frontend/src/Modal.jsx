import { useEffect, useId, useRef } from 'react'
import { useI18n } from './context.js'
import { CloseIcon } from './Icons.jsx'
import { focusables, trapStep } from './focus.js'

/**
 * A dialog over a backdrop. Escape, the close button and a click outside close it. Focus starts on the first field
 * (the box itself when there is none, so nothing is activated by accident), stays inside while it is open, and goes
 * back to the element in returnFocus, else to where it was, when it closes.
 */
export function Modal({ title, onClose, returnFocus, children }) {
  const { t } = useI18n()
  const box = useRef(null)
  const titleId = useId()
  const close = useRef(onClose)
  close.current = onClose

  useEffect(() => {
    const el = box.current
    const before = document.activeElement
    ;(el.querySelector('.modal__body input:not([disabled]), .modal__body textarea:not([disabled])') ?? el).focus()
    const onKey = (ev) => {
      if (ev.key === 'Escape') {
        ev.stopPropagation()
        close.current()
      } else if (ev.key === 'Tab') {
        const items = focusables(el)
        const next = trapStep(items.indexOf(document.activeElement), items.length, ev.shiftKey)
        if (next >= 0) {
          ev.preventDefault()
          items[next].focus()
        }
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      const back = returnFocus?.current ?? before
      if (back instanceof HTMLElement) {
        back.focus()
      }
    }
  }, [returnFocus])

  return (
    <div
      className="modal"
      role="presentation"
      onMouseDown={(ev) => {
        if (ev.target === ev.currentTarget) {
          ev.preventDefault() // the click must not take the focus away from the element it goes back to
          close.current()
        }
      }}
    >
      <div className="modal__box" role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} ref={box}>
        <div className="modal__head">
          <h2 className="modal__title" id={titleId}>{title}</h2>
          <button type="button" className="iconbtn" aria-label={t('nav.close')} title={t('nav.close')} onClick={() => close.current()}>
            <CloseIcon />
          </button>
        </div>
        <div className="modal__body">{children}</div>
      </div>
    </div>
  )
}
