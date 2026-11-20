import { messages } from './i18n.js'
import { errorText, safeText } from './format.js'

/**
 * The service's error inside whatever the window got: a rejected call (Wails puts the Go error in
 * `err.cause`, as an object or as its JSON), or the `error` field of a transfer, the network or a
 * history item. Null when there is none.
 */
export function shareError(err) {
  if (!err || typeof err !== 'object') {
    return null
  }
  if (typeof err.reason === 'string') {
    return err
  }
  let cause = err.cause
  if (typeof cause === 'string' && cause.startsWith('{')) {
    try {
      cause = JSON.parse(cause)
    } catch {
      return null
    }
  }
  return cause && typeof cause === 'object' && typeof cause.reason === 'string' ? cause : null
}

/**
 * The sentence to show for an error, in the language of the window: translated by the stable
 * reason of the service, with its English sentence as the fallback for a reason we do not know.
 * A `{ key }` object is a sentence of the window itself. The detail and the cause can hold a name
 * from another computer, so they are made safe to show.
 */
export function errorSentence(t, locale, err, fallback = '') {
  if (!err) {
    return fallback
  }
  if (typeof err === 'object' && typeof err.key === 'string') {
    return t(err.key)
  }
  const known = shareError(err)
  if (known) {
    const key = `error.${known.reason}`
    if (messages[locale]?.[key] !== undefined || messages.en[key] !== undefined) {
      return t(key, { detail: safeText(known.detail), cause: safeText(known.cause) })
    }
    return safeText(known.message) || fallback
  }
  return safeText(errorText(err)) || fallback
}
