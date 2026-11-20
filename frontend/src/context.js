import { createContext, useContext } from 'react'
import { translate } from './i18n.js'
import { errorSentence } from './errors.js'

const english = (key, vars) => translate('en', key, vars)

/**
 * The language of the window: `t` turns a key into a sentence, `fail` turns an error of the service
 * into one, `lang` is the tag for numbers and dates.
 */
export const I18n = createContext({ t: english, fail: (err, fallback) => errorSentence(english, 'en', err, fallback), lang: 'en' })

export const useI18n = () => useContext(I18n)
