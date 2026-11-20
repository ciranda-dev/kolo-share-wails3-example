import test from 'node:test'
import assert from 'node:assert/strict'
import { translate } from '../src/i18n.js'
import { errorSentence } from '../src/errors.js'
import { canStart, networkLine, newerNetwork, relayLine, relaySource, SLOW_START_MS, startingText } from '../src/network.js'

const starting = { state: 'starting', message: '', entryNodes: 1, firstNode: '', since: 1000, seq: 1 }
const ready = { ...starting, state: 'ready', since: 1040, seq: 2 }

test('the snapshot of Start never hides a newer event', () => {
  // The event comes first, then the older snapshot that Start took.
  assert.equal(newerNetwork(newerNetwork(starting, ready), starting), ready)
  // The snapshot comes first, then the event.
  assert.equal(newerNetwork(newerNetwork({ state: 'starting' }, starting), ready), ready)
  assert.equal(newerNetwork(ready, null), ready)
  assert.equal(newerNetwork(ready, { ...ready }).state, 'ready')
})

/** What the window holds after these payloads, in this order, from its first state. */
const held = (...payloads) => payloads.reduce(newerNetwork, { state: 'starting', seq: 0 })

test('the orders of real use end in the right state', () => {
  // Alice first, then Bob: Bob's network is ready before his Start call returns its snapshot.
  assert.equal(held(ready, starting).state, 'ready')
  // Bob before Alice: no entry node answers, then Try again once Alice runs.
  const failed = { ...starting, state: 'failed', seq: 2 }
  assert.equal(held(starting, failed).state, 'failed')
  const retried = held(starting, failed, { ...starting, seq: 3 }, { ...ready, seq: 4 })
  assert.equal(retried.state, 'ready')
  assert.equal(retried.seq, 4)
  // A reload while ready: the new page starts at seq 0 and the snapshot is the only payload.
  assert.equal(held({ ...ready, seq: 4 }).state, 'ready')
})

test('a pairing can be asked for while the network starts', () => {
  assert.equal(canStart(starting, null), true)
  assert.equal(canStart(ready, null), true)
  assert.equal(canStart({ state: 'failed' }, null), false)
  assert.equal(canStart(ready, new Error('fatal')), false)
})

test('the starting sentence names the entry nodes and the time it takes', () => {
  const t = (locale) => (key, vars) => translate(locale, key, vars)
  const one = startingText(t('en'), 'en', starting, 13_400)
  assert.match(one.text, /^Connecting to 1 entry node\./)
  assert.match(one.text, /show or paste a code now/)
  assert.equal(one.time, '12 sec')
  assert.equal(one.slow, false)
  const two = startingText(t('pt'), 'pt-BR', { ...starting, entryNodes: 2 }, 1000)
  assert.match(two.text, /^Conectando a 2 nós de entrada\./)
  const first = startingText(t('es'), 'es-ES', { ...starting, firstNode: '127.0.0.1:4000' }, 1000)
  assert.match(first.text, /127\.0\.0\.1:4000/)
  const local = startingText(t('en'), 'en', { ...starting, localNode: '127.0.0.1:7451' }, 1000)
  assert.match(local.text, /^Starting the network on this computer only \(127\.0\.0\.1:7451\)\./)
  assert.match(startingText(t('pt'), 'pt-BR', { ...starting, localNode: '127.0.0.1:7451' }, 1000).text, /rede só neste computador/)
  const pub = { ...starting, mode: 'public', entryNodes: 2 }
  assert.match(startingText(t('en'), 'en', pub, 1000).text, /^Connecting to the public network\./)
  assert.match(startingText(t('es'), 'es-ES', pub, 1000).text, /^Conectando a la red pública\./)
  assert.match(startingText(t('pt'), 'pt-BR', pub, 1000).text, /^Conectando à rede pública\./)
  const slow = startingText(t('en'), 'en', starting, 1000 + SLOW_START_MS)
  assert.equal(slow.slow, true)
  assert.match(slow.text, /longer than usual/)
  assert.equal(startingText(t('en'), 'en', { ...starting, since: 0 }, 5000).time, '')
})

test('Settings says where the relays came from', () => {
  const t = (key, vars) => translate('en', key, vars)
  assert.equal(relaySource(t, 'flag'), '(from -relay)')
  assert.equal(relaySource(t, 'file'), '(from -relay-file)')
  assert.equal(relaySource(t, 'flag-and-file'), '(from -relay and -relay-file)')
  assert.equal(relaySource(t, 'env'), '(from KOLO_SHARE_RELAYS)')
  assert.equal(relaySource(t, 'none'), '')
})

test('Settings shows each relay with its state, and why it holds no place', () => {
  for (const locale of ['en', 'es', 'pt']) {
    const t = (key, vars) => translate(locale, key, vars)
    const fail = (err, fallback) => errorSentence(t, locale, err, fallback)
    const wrong = { reason: 'relay-wrong-key', detail: '127.0.0.1:7448', cause: '', message: 'x' }
    const line = relayLine(t, fail, { addr: '127.0.0.1:7448', state: 'not-reserved', error: wrong })
    assert.equal(line, `${t('settings.relay.not-reserved', { addr: '127.0.0.1:7448' })} ${t('error.relay-wrong-key', { detail: '127.0.0.1:7448' })}`)
    assert.equal(relayLine(t, fail, { addr: 'r:1', state: 'reserved', error: null }), t('settings.relay.reserved', { addr: 'r:1' }))
    assert.equal(relayLine(t, fail, { addr: 'r:1', state: 'idle', error: null }), t('settings.relay.idle', { addr: 'r:1' }))
    assert.notEqual(t('settings.relay.reserved', { addr: 'r:1' }), t('settings.relay.idle', { addr: 'r:1' }))
  }
})

test('a relay address never carries a hidden character to the window', () => {
  const t = (key, vars) => translate('en', key, vars)
  const line = relayLine(t, () => '', { addr: 'evil\u202e8447:yalp', state: 'reserved', error: null })
  assert.ok(!line.includes('\u202e'), line)
  assert.ok(line.startsWith('evil[U+202E]8447:yalp'), line)
})

test('Settings describes the public network in one sentence and lists no address', () => {
  for (const locale of ['en', 'es', 'pt']) {
    const sentence = translate(locale, 'settings.network.public', { list: '203.0.113.10:7447' })
    assert.match(sentence, /Ciranda/)
    assert.doesNotMatch(sentence, /\d+\.\d+\.\d+\.\d+|:\d{2,5}/)
    assert.equal(sentence, translate(locale, 'settings.network.public'))
    assert.match(translate(locale, 'settings.network.nodes', { list: '203.0.113.10:4000' }), /203\.0\.113\.10:4000/)
  }
})

test('the network is one quiet line: which network, then how it stands, in every language, with no address', () => {
  assert.equal(networkLine((k) => translate('en', k), 'public', '2 connected'), 'Public network · 2 connected')
  assert.equal(networkLine((k) => translate('en', k), 'own', 'Ready'), 'Your own network · Ready')
  assert.equal(networkLine((k) => translate('es', k), 'local', 'Listo'), 'Solo este equipo · Listo')
  assert.equal(networkLine((k) => translate('pt', k), 'local', 'Pronto'), 'Somente este computador · Pronto')
  assert.equal(networkLine((k) => translate('en', k), '', 'Starting'), 'Starting', 'before the mode is known only the state shows')
  assert.equal(networkLine((k) => translate('en', k), 'elsewhere', ''), '', 'an unknown mode says nothing')
})
