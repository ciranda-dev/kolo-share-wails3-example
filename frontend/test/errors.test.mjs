import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { locales, messages, translate } from '../src/i18n.js'
import { errorSentence, shareError } from '../src/errors.js'

// The reasons of the service are the keys of `reasons` in errors.go, at the root of the repository: a new reason
// on the Go side fails here until it has its three sentences.
const goFile = readFileSync(new URL('../../errors.go', import.meta.url), 'utf8')
const start = goFile.indexOf('var reasons = map[string]')
const block = goFile.slice(start, goFile.indexOf('\n}', start))
const goReasons = Object.fromEntries([...block.matchAll(/^\t"([\w-]+)":\s*\{\w+, "((?:[^"\\]|\\.)*)"\},?$/gm)].map((m) => [m[1], JSON.parse(`"${m[2]}"`)]))
const reasons = Object.keys(goReasons)

test('every reason of errors.go has a sentence in every language, with the places of the Go sentence', () => {
  assert.ok(reasons.length >= 45)
  const places = (text) => (text.match(/\{\w+\}/g) ?? []).sort().join(' ')
  for (const [reason, sentence] of Object.entries(goReasons)) {
    assert.equal(messages.en[`error.${reason}`], sentence, `en error.${reason} is the Go sentence`)
    for (const locale of locales) {
      const text = messages[locale][`error.${reason}`]
      assert.ok(text && text.trim() !== '', `${locale} error.${reason}`)
      assert.equal(places(text), places(sentence), `${locale} error.${reason}`)
      assert.ok(!/sorry|lo sentimos|perd[oó]n|desculp/i.test(text), `${locale} error.${reason} apologizes`)
    }
  }
  const extra = Object.keys(messages.en).filter((k) => k.startsWith('error.') && !reasons.includes(k.slice(6)))
  assert.deepEqual(extra, [])
})

const t = (locale) => (key, vars) => translate(locale, key, vars)
const disk = { kind: 'disk', reason: 'disk', detail: '/tmp/a.txt', cause: 'no space left on device', message: 'Could not write /tmp/a.txt: no space left on device.' }

test('an error is translated by its reason, with its detail and cause', () => {
  assert.equal(errorSentence(t('en'), 'en', disk), disk.message)
  assert.equal(errorSentence(t('es'), 'es', disk), 'No se ha podido escribir /tmp/a.txt: no space left on device.')
  assert.equal(errorSentence(t('pt'), 'pt', disk), 'Não foi possível gravar /tmp/a.txt: no space left on device.')
})

test('a rejected call carries the error as its cause, as an object or as JSON', () => {
  const thrown = Object.assign(new Error(disk.message), { cause: disk })
  assert.equal(shareError(thrown), disk)
  assert.equal(errorSentence(t('es'), 'es', thrown), 'No se ha podido escribir /tmp/a.txt: no space left on device.')
  const asJson = Object.assign(new Error(disk.message), { cause: JSON.stringify(disk) })
  assert.equal(shareError(asJson).reason, 'disk')
  assert.equal(shareError(Object.assign(new Error('x'), { cause: '{broken' })), null)
})

test('an unknown reason keeps the English sentence of the service', () => {
  const unknown = { kind: 'internal', reason: 'brand-new', detail: '', cause: '', message: 'A new sentence.' }
  assert.equal(errorSentence(t('pt'), 'pt', unknown), 'A new sentence.')
  assert.equal(errorSentence(t('pt'), 'pt', new Error('plain')), 'plain')
  assert.equal(errorSentence(t('pt'), 'pt', null, 'fallback'), 'fallback')
  assert.equal(errorSentence(t('es'), 'es', { key: 'connect.first' }), 'Primero conéctate con alguien.')
})
