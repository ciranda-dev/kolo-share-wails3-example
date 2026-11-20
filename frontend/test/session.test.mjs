import test from 'node:test'
import assert from 'node:assert/strict'
import { codeGroups, formatCountdown, formatDuration } from '../src/format.js'
import { translate } from '../src/i18n.js'
import { errorSentence } from '../src/errors.js'
import { KEEP_FINISHED, codeToCancel, endSentence, keepTransfers, newerPairing, numbersText, peerLabel, rowView, statusText, unreachableText } from '../src/session.js'

const t = (locale) => (key, vars) => translate(locale, key, vars)
const fail = (locale) => (err, fallback) => errorSentence(t(locale), locale, err, fallback)
const item = (patch) => ({ id: 'x', direction: 'receive', kind: 'file', name: 'a.pdf', state: 'offered', bytesDone: 0, bytesTotal: 10, filesTotal: 1, error: null, ...patch })

test('formatCountdown shows minutes and seconds, hours from an hour up, never below zero', () => {
  assert.equal(formatCountdown(59 * 60_000 + 12_000), '59:12')
  assert.equal(formatCountdown(3_600_000), '1:00:00')
  assert.equal(formatCountdown(3_849_000), '1:04:09')
  assert.equal(formatCountdown(500), '0:01')
  assert.equal(formatCountdown(-5000), '0:00')
  assert.equal(formatCountdown(undefined), '0:00')
})

test('formatDuration uses the largest whole unit of the language', () => {
  assert.equal(formatDuration(3_600_000, 'en'), '1 hour')
  assert.equal(formatDuration(2 * 3_600_000, 'pt-BR'), '2 horas')
  assert.equal(formatDuration(90 * 60_000, 'en'), '90 minutes')
  assert.equal(formatDuration(45_000, 'es-ES'), '45 segundos')
  assert.equal(formatDuration(1500, 'en'), '1.5 seconds')
})

test('codeGroups cuts a code in groups of four and keeps every character', () => {
  const code = 'koloinv0abcdefghij'
  assert.deepEqual(codeGroups(code), ['kolo', 'inv0', 'abcd', 'efgh', 'ij'])
  assert.equal(codeGroups(code).join(''), code)
  assert.deepEqual(codeGroups(''), [])
})

test('the state of a transfer reads by its direction', () => {
  assert.equal(statusText(t('en'), fail('en'), 'en', item({ direction: 'send' }), 'Ana'), 'Waiting for Ana to accept')
  assert.equal(statusText(t('en'), fail('en'), 'en', item({}), 'Ana'), 'Waiting for your answer')
  assert.equal(statusText(t('es'), fail('es'), 'es-ES', item({ direction: 'send', state: 'declined' }), 'Ana'), 'Ana lo rechazó')
  assert.equal(statusText(t('pt'), fail('pt'), 'pt-BR', item({ state: 'declined' }), 'Ana'), 'Recusado')
  assert.equal(statusText(t('en'), fail('en'), 'en', item({ state: 'withdrawn' }), 'Ana'), 'Ana cancelled this item')
  assert.equal(statusText(t('en'), fail('en'), 'en', item({ direction: 'send', state: 'cancelled' }), 'Ana'), 'Ana stopped saving it')
  assert.equal(statusText(t('en'), fail('en'), 'en', item({ state: 'done', kind: 'folder', filesTotal: 1200 }), 'Ana'), 'Saved 1,200 files')
  assert.equal(statusText(t('en'), fail('en'), 'en', item({ state: 'queued' }), 'Ana'), 'Waiting for the item before it')
})

test('a failure says its reason', () => {
  const failed = item({ state: 'failed', error: { kind: 'corrupt', reason: 'offer-mismatch', detail: '', cause: '', message: 'x' } })
  assert.equal(statusText(t('pt'), fail('pt'), 'pt-BR', failed, 'Ana'), 'O que chegou não corresponde ao que foi oferecido. Nada foi salvo.')
})

test('an ended session says why, with the name of the other side made safe', () => {
  const s = { peerName: 'Ana‮', peerKey: 'a1b2c3d4', reason: 'peer-closed' }
  assert.equal(endSentence(t('en'), s), 'Ana[U+202E] closed the session.')
  assert.equal(endSentence(t('en'), { ...s, reason: 'closed' }), 'You closed the session.')
  assert.match(endSentence(t('es'), { ...s, reason: 'replaced' }), /^Te conectaste de nuevo con Ana/)
  assert.match(endSentence(t('en'), { ...s, reason: 'peer-protocol' }), /does not understand/)
  assert.match(endSentence(t('en'), { ...s, reason: 'app-closed' }), /Kolo Share closed/)
  assert.equal(peerLabel({ peerName: '', peerKey: 'a1b2c3d4' }), 'Kolo a1b2c3d4')
})

test('a call result never takes a pairing back to an earlier state', () => {
  const waiting = { id: 'p1', role: 'inviter', state: 'waiting', code: 'koloinv0x' }
  const starting = { id: 'p1', role: 'inviter', state: 'starting', code: '' }
  assert.equal(newerPairing(waiting, starting), waiting)
  assert.equal(newerPairing(starting, waiting), waiting)
  assert.equal(newerPairing(waiting, { id: 'p2', role: 'joiner', state: 'looking' }).id, 'p2')
  assert.equal(newerPairing(waiting, null), waiting)
  assert.equal(newerPairing(waiting, { ...waiting, state: 'connected', sessionId: 's1' }).state, 'connected')
})

test('the window keeps every running item and the newest finished ones of each session', () => {
  let list = []
  for (let i = 0; i < KEEP_FINISHED + 30; i++) {
    list = keepTransfers(list, item({ id: `w${i}`, sessionId: 's1', state: 'withdrawn' }))
  }
  list = keepTransfers(list, item({ id: 'run', sessionId: 's1', state: 'receiving' }))
  list = keepTransfers(list, item({ id: 'other', sessionId: 's2', state: 'done' }))
  assert.equal(list.filter((x) => x.sessionId === 's1' && x.state === 'withdrawn').length, KEEP_FINISHED)
  assert.ok(list.some((x) => x.id === 'run') && list.some((x) => x.id === 'other'))
  assert.ok(list.some((x) => x.id === `w${KEEP_FINISHED + 29}`) && !list.some((x) => x.id === 'w0'))
  list = keepTransfers(list, item({ id: 'run', sessionId: 's1', state: 'done' }))
  assert.equal(list.filter((x) => x.id === 'run').length, 1)
})

test('leaving the Connect page cancels a code that waits there, and nothing else', () => {
  const waiting = { id: '1', role: 'inviter', state: 'waiting' }
  assert.equal(codeToCancel(true, false, waiting), true)
  assert.equal(codeToCancel(true, false, { ...waiting, state: 'starting' }), true)
  assert.equal(codeToCancel(true, true, waiting), false)
  assert.equal(codeToCancel(false, false, waiting), false)
  assert.equal(codeToCancel(true, false, { ...waiting, state: 'connected' }), false)
  assert.equal(codeToCancel(true, false, { id: '2', role: 'joiner', state: 'looking' }), false)
})

test('the unreachable band suggests a relay only to an app that lists none', () => {
  const en = t('en')
  assert.equal(unreachableText(en, 'Bob', false), `${en('session.unreachable', { name: 'Bob' })} ${en('session.relay')}`)
  assert.ok(unreachableText(en, 'Bob', false).includes('-relay'))
  assert.equal(unreachableText(en, 'Bob', true), en('session.unreachable.relays', { name: 'Bob' }))
  assert.ok(!unreachableText(en, 'Bob', true).includes('start both apps'))
})

const rate = (n) => `${n} B/s`
const preparing = (patch) => item({ state: 'preparing', bytesDone: 0, bytesTotal: 0, ...patch })

test('an item the other side is preparing shows its sentence in every language and cannot be answered', () => {
  const card = preparing({ bytesTotal: 5_000_000_000, bytesDone: 1_000_000_000 })
  assert.equal(statusText(t('en'), fail('en'), 'en', card, 'Ana'), 'Being prepared by Ana')
  assert.equal(statusText(t('es'), fail('es'), 'es-ES', card, 'Ana'), 'Ana lo está preparando')
  assert.equal(statusText(t('pt'), fail('pt'), 'pt-BR', card, 'Ana'), 'Ana está preparando')
  assert.deepEqual(rowView(card), { progress: true, indeterminate: false, save: false, decline: false, cancel: false })
  assert.equal(numbersText(t('en'), 'en', card, 'Ana', rate), '1.0 GB of 5.0 GB')
})

test('a card without a size sweeps and shows no invented number', () => {
  const card = preparing({})
  assert.equal(rowView(card).indeterminate, true)
  assert.equal(numbersText(t('en'), 'en', card, 'Ana', rate), '')
  assert.equal(statusText(t('en'), fail('en'), 'en', preparing({ direction: 'send' }), 'Ana'), 'Preparing on this computer, Ana cannot take it yet')
})

test('the card of a failed preparation says why, and the same item turns into the offer', () => {
  const stopped = preparing({ state: 'failed', error: { kind: 'lost', reason: 'prepare-stalled', detail: '', cause: '', message: 'x' } })
  assert.match(statusText(t('en'), fail('en'), 'en', stopped, 'Ana'), /stopped waiting/)
  assert.match(statusText(t('es'), fail('es'), 'es-ES', stopped, 'Ana'), /dejó de esperar/)
  assert.match(statusText(t('pt'), fail('pt'), 'pt-BR', stopped, 'Ana'), /parou de esperar/)
  let list = keepTransfers([], preparing({ id: 'c1', sessionId: 's1', name: 'big.iso' }))
  list = keepTransfers(list, item({ id: 'c1', sessionId: 's1', name: 'big.iso', state: 'offered', bytesTotal: 10 }))
  assert.equal(list.length, 1)
  assert.deepEqual(rowView(list[0]), { progress: false, indeterminate: false, save: true, decline: true, cancel: false })
})

test('the numbers say what they count, and a stalled item says it waits', () => {
  const send = (patch) => item({ direction: 'send', bytesDone: 2_000_000, bytesTotal: 10_000_000, bytesPerSecond: 1_000_000, ...patch })
  assert.equal(numbersText(t('en'), 'en', send({ state: 'preparing' }), 'Ana', rate), 'Prepared 2.0 MB of 10.0 MB · 1000000 B/s')
  assert.equal(numbersText(t('en'), 'en', send({ state: 'sending' }), 'Ana', rate), 'Downloaded by Ana: 2.0 MB of 10.0 MB · 1000000 B/s')
  assert.equal(numbersText(t('es'), 'es-ES', send({ state: 'sending' }), 'Ana', rate), 'Descargado por Ana: 2,0 MB de 10,0 MB · 1000000 B/s')
  assert.equal(numbersText(t('pt'), 'pt-BR', item({ state: 'receiving', bytesDone: 2_000_000, bytesTotal: 10_000_000 }), 'Ana', rate), 'Recebido 2,0 MB de 10,0 MB')
  const stalled = item({ state: 'receiving', stalled: true, bytesDone: 2_000_000, bytesTotal: 10_000_000, bytesPerSecond: 0 })
  assert.match(statusText(t('en'), fail('en'), 'en', stalled, 'Ana'), /Waiting for the connection, it continues by itself/)
  assert.match(statusText(t('es'), fail('es'), 'es-ES', stalled, 'Ana'), /Esperando la conexión/)
  assert.match(statusText(t('pt'), fail('pt'), 'pt-BR', { ...stalled, state: 'sending', direction: 'send' }, 'Ana'), /Esperando a conexão/)
  assert.equal(rowView(stalled).cancel, true)
  const gaveUp = item({ state: 'failed', error: { kind: 'lost', reason: 'resume-gave-up', detail: '30 min', cause: '', message: 'x' } })
  assert.match(statusText(t('en'), fail('en'), 'en', gaveUp, 'Ana'), /for 30 min/)
})
