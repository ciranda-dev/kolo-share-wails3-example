import { formatCount, safeText } from './format.js'

/** After this long the starting sentence says it takes longer than usual: a network on this computer starts in milliseconds. */
export const SLOW_START_MS = 15_000

/** The newer of two network states by their seq: the snapshot of Start can arrive after a newer event. */
export function newerNetwork(prev, next) {
  if (!next) {
    return prev
  }
  return (Number(next.seq) || 0) >= (Number(prev?.seq) || 0) ? next : prev
}

/** Where the relays came from, as Settings says it; '' when they were set in code. */
export function relaySource(t, from) {
  return ['flag', 'file', 'flag-and-file', 'env'].includes(from) ? t(`settings.relays.from.${from}`) : ''
}

/** One relay of Network.relays: its address and state, and why it holds no place. */
export function relayLine(t, fail, relay) {
  const state = ['reserved', 'not-reserved'].includes(relay?.state) ? relay.state : 'idle'
  const text = t(`settings.relay.${state}`, { addr: safeText(relay?.addr) })
  return state === 'not-reserved' && relay.error ? `${text} ${fail(relay.error)}` : text
}

/** Whether a pairing can be asked for: while the network starts, it waits for it. */
export function canStart(network, fatal) {
  return !fatal && (network?.state === 'starting' || network?.state === 'ready')
}

/** The sentence while the network starts, the time it has taken so far ('' when unknown) and whether that is long. */
export function startingText(t, lang, network, now) {
  const ms = network.since > 0 ? Math.max(0, now - network.since) : -1
  const slow = ms >= SLOW_START_MS
  const time = ms < 0 ? '' : new Intl.NumberFormat(lang, { style: 'unit', unit: 'second', unitDisplay: 'short' }).format(Math.floor(ms / 1000))
  const n = Number(network.entryNodes) || 0
  let text = network.localNode
    ? t('network.local', { node: network.localNode })
    : network.firstNode
      ? t('network.first', { node: network.firstNode })
      : network.mode === 'public'
        ? t('network.public')
        : t('network.starting', { n, count: formatCount(n, lang) })
  if (slow) {
    text = `${text} ${t('network.slow')}`
  }
  return { text, time, slow }
}

/** The network as one quiet line under the sessions: which network this is, then how it stands. No address is named. */
export function networkLine(t, mode, stand) {
  const which = ['public', 'own', 'local'].includes(mode) ? t(`net.${mode}`) : ''
  return [which, stand].filter(Boolean).join(' · ')
}
