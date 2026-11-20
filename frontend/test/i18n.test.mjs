import test from 'node:test'
import assert from 'node:assert/strict'
import { locales, languageNames, htmlLang, messages, pickLocale, translate } from '../src/i18n.js'

const places = (text) => (text.match(/\{\w+\}/g) ?? []).sort().join(' ')

test('every language has the keys of English, with the same places', () => {
  const keys = Object.keys(messages.en).sort()
  for (const locale of locales) {
    assert.deepEqual(Object.keys(messages[locale]).sort(), keys, `keys of ${locale}`)
    for (const key of keys) {
      assert.equal(places(messages[locale][key]), places(messages.en[key]), `${locale} ${key}`)
      assert.ok(messages[locale][key].trim() !== '', `${locale} ${key} is empty`)
    }
    assert.ok(languageNames[locale])
    assert.ok(htmlLang[locale])
  }
})

test('the copy has no em dash and no exclamation mark', () => {
  for (const locale of locales) {
    for (const [key, text] of Object.entries(messages[locale])) {
      assert.ok(!/[\u2014!\u00a1]/.test(text), `${locale} ${key}: ${text}`)
    }
  }
})

test('pickLocale prefers the saved language, then the system, then English', () => {
  assert.equal(pickLocale('pt', ['es-ES']), 'pt')
  assert.equal(pickLocale(undefined, ['fr-FR', 'es-MX']), 'es')
  assert.equal(pickLocale('xx', ['pt-BR']), 'pt')
  assert.equal(pickLocale(undefined, ['fr-FR']), 'en')
  assert.equal(pickLocale(undefined), 'en')
})

test('translate fills places and picks the form by n', () => {
  assert.equal(translate('es', 'badge.sessions', { n: 1 }), '1 conectada')
  assert.equal(translate('es', 'badge.sessions', { n: 3 }), '3 conectadas')
  assert.equal(translate('pt', 'state.done.folder', { n: 2, count: '2' }), '2 arquivos salvos')
  assert.equal(translate('pt', 'files', { n: 12480, count: '12.480' }), '12.480 arquivos')
  assert.equal(translate('en', 'no.such.key'), 'no.such.key')
  assert.equal(translate('xx', 'nav.history'), 'History')
  assert.equal(translate('en', 'connect.expires', {}), 'Expires in {time}')
})
