import { en } from './messages/en.js'
import { es } from './messages/es.js'
import { pt } from './messages/pt.js'

/** The three languages; English is the default. */
export const locales = ['en', 'es', 'pt']

/** The full language tag of each code, for `<html lang>` and for number and date formats. */
export const htmlLang = { en: 'en', es: 'es-ES', pt: 'pt-BR' }

/** The name of each language, in that language. */
export const languageNames = { en: 'English', es: 'Español', pt: 'Português (Brasil)' }

export const messages = { en, es, pt }

/** The language to start in: the saved one, else the first system language we have, else English. */
export function pickLocale(saved, systemLanguages = []) {
  if (locales.includes(saved)) {
    return saved
  }
  for (const tag of systemLanguages) {
    const code = String(tag).toLowerCase().split('-')[0]
    if (locales.includes(code)) {
      return code
    }
  }
  return 'en'
}

/**
 * The sentence for a key, with {name} places filled from vars. A key with `.one` and `.other`
 * forms picks one by vars.n. A missing key falls back to English, then to the key itself.
 */
export function translate(locale, key, vars = {}) {
  const table = messages[locale] ?? en
  let text = table[key] ?? en[key]
  if (text === undefined && vars.n !== undefined) {
    const form = Number(vars.n) === 1 ? 'one' : 'other'
    text = table[`${key}.${form}`] ?? en[`${key}.${form}`]
  }
  if (text === undefined) {
    return key
  }
  return text.replace(/\{(\w+)\}/g, (whole, name) => (vars[name] === undefined ? whole : String(vars[name])))
}
