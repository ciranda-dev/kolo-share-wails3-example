import test from 'node:test'
import assert from 'node:assert/strict'
import { middleCut, safeText } from '../src/format.js'
import { translate } from '../src/i18n.js'
import { errorSentence } from '../src/errors.js'

const hidden = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\p{Co}\p{Cs}]/u

test('a right-to-left override cannot hide an extension', () => {
  const shown = safeText('invoice-\u202egnp.exe')
  assert.equal(shown, 'invoice-[U+202E]gnp.exe')
  assert.ok(shown.endsWith('.exe'))
})

test('every bidi control is shown as its code', () => {
  for (const code of [0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069]) {
    const shown = safeText(`a${String.fromCodePoint(code)}b`)
    assert.equal(shown, `a[U+${code.toString(16).toUpperCase().padStart(4, '0')}]b`)
    assert.ok(!hidden.test(shown))
  }
})

test('zero-width, control and separator characters are shown too', () => {
  assert.equal(safeText('a\u200bb\u200cc\u200dd\ufeffe\u2060f'), 'a[U+200B]b[U+200C]c[U+200D]d[U+FEFF]e[U+2060]f')
  assert.equal(safeText('line\nbreak\ttab\u0000nul\u007fdel\u0085nel'), 'line[U+000A]break[U+0009]tab[U+0000]nul[U+007F]del[U+0085]nel')
  assert.equal(safeText('a\u2028b\u2029c\u00add'), 'a[U+2028]b[U+2029]c[U+00AD]d')
  assert.equal(safeText('tag\u{e0041}x'), 'tag[U+E0041]x')
  assert.equal(safeText('lone\ud800x'), 'lone[U+D800]x')
})

test('ordinary names in any script stay as they are', () => {
  for (const name of ['photo.jpg', 'Año nuevo ñ.txt', 'relatório final.pdf', 'שלום.txt', 'مرحبا.pdf', '日本語のファイル.zip', 'a b  c.tar.gz', 'emoji \u{1F600}.png', '']) {
    assert.equal(safeText(name), name)
  }
  assert.equal(safeText(undefined), '')
  assert.equal(safeText(null), '')
})

test('a name that looks like markup stays text', () => {
  const name = '<img src=x onerror=alert(1)>.pdf'
  assert.equal(safeText(name), name)
  assert.equal(safeText('<b>\u202ex</b>'), '<b>[U+202E]x</b>')
})

test('a very long hostile name is escaped, then cut, and keeps its real ending', () => {
  const name = `${'a'.repeat(5000)}\u202e${'b'.repeat(5000)}gpj.exe`
  const shown = middleCut(safeText(name), 40)
  assert.equal(Array.from(shown).length, 40)
  assert.ok(shown.endsWith('.exe'))
  assert.ok(!hidden.test(shown))
  assert.ok(!hidden.test(safeText('\u202e'.repeat(10000))))
})

test('an error sentence escapes the name and the cause it carries', () => {
  const t = (key, vars) => translate('es', key, vars)
  const err = { kind: 'unsafe-name', reason: 'unsafe-name', detail: 'invoice-\u202egnp.exe', cause: 'bad\u200bword', message: 'x' }
  const text = errorSentence(t, 'es', err)
  assert.ok(text.includes('invoice-[U+202E]gnp.exe'))
  assert.ok(!hidden.test(text))
  const unknown = { kind: 'k', reason: 'not-known', detail: '', cause: '', message: 'Name \u202eevil' }
  assert.equal(errorSentence(t, 'es', unknown), 'Name [U+202E]evil')
  assert.equal(errorSentence(t, 'es', new Error('plain \u2066x')), 'plain [U+2066]x')
})
