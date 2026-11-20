import { Component, useState } from 'react'
import { call, copyText } from './service.js'
import { htmlLang, pickLocale, translate } from './i18n.js'
import { loadPrefs } from './prefs.js'
import { reportParts, reportText } from './report.js'
import './share.css'

/** Writes the error to the terminal through Go, as a "frontend error: " line, and returns the text for the pane. */
function report(error, componentStack = '') {
  const parts = reportParts(error, componentStack)
  call('ReportError', parts.message, parts.stack).catch(() => {})
  return reportText(parts)
}

/** The error on the whole window with a Copy button, never a blank window. onDismiss when the window may go on. */
function CrashPane({ text, compact, onDismiss }) {
  const locale = pickLocale(loadPrefs().lang, window.navigator.languages ?? [])
  const t = (key) => translate(locale, key)
  const [copied, setCopied] = useState('')
  const copy = () => copyText(text).then(() => setCopied('crash.copied'), () => setCopied('crash.copyFailed'))
  return (
    <div className={`crash${compact ? ' is-compact' : ''}`} role="alert" lang={htmlLang[locale]}>
      <h1 className="crash__title">{t('crash.title')}</h1>
      <p className="crash__note">{t('crash.note')}</p>
      <pre className="crash__text" tabIndex={0}>{text}</pre>
      <div className="crash__actions">
        <button type="button" className="btn btn--small" onClick={copy} autoFocus>{t('crash.copy')}</button>
        {onDismiss ? <button type="button" className="btn btn--ghost btn--small" onClick={onDismiss}>{t('crash.dismiss')}</button> : null}
        <span className="crash__status" aria-live="polite">{copied ? t(copied) : ''}</span>
      </div>
    </div>
  )
}

/**
 * The root of a page. A render that throws shows the pane in place of the page; an error no code caught (an event,
 * a promise) shows it over the page, which Dismiss gives back. Every error goes to the terminal.
 */
export class CrashBoundary extends Component {
  state = { text: '', fatal: false }

  static getDerivedStateFromError(error) {
    return { text: reportText(reportParts(error)), fatal: true }
  }

  componentDidCatch(error, info) {
    this.setState({ text: report(error, info?.componentStack ?? '') })
  }

  componentDidMount() {
    this.onError = (ev) => this.setState({ text: report(ev.error ?? ev.message) })
    this.onRejection = (ev) => this.setState({ text: report(ev.reason) })
    window.addEventListener('error', this.onError)
    window.addEventListener('unhandledrejection', this.onRejection)
  }

  componentWillUnmount() {
    window.removeEventListener('error', this.onError)
    window.removeEventListener('unhandledrejection', this.onRejection)
  }

  render() {
    const { text, fatal } = this.state
    const dismiss = fatal ? null : () => this.setState({ text: '' })
    return (
      <>
        {fatal ? null : this.props.children}
        {text ? <CrashPane text={text} compact={this.props.compact} onDismiss={dismiss} /> : null}
      </>
    )
  }
}
