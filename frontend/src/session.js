import { formatBytes, formatCount, safeText } from './format.js'

/** Final states of a transfer, in both directions. */
export const FINAL = ['delivered', 'declined', 'cancelled', 'stopped', 'failed', 'done', 'withdrawn']

/** States whose sentence differs by direction. */
const BY_DIRECTION = ['preparing', 'offered', 'declined', 'cancelled', 'stopped']

/** What the band says about a peer this app cannot reach: with relays listed, they failed too. */
export function unreachableText(t, name, relays) {
  return relays ? t('session.unreachable.relays', { name }) : `${t('session.unreachable', { name })} ${t('session.relay')}`
}

/** The name of the other side, safe to show: the service never sends it empty, the key is the fallback. */
export function peerLabel(session) {
  return safeText(session?.peerName) || `Kolo ${safeText(session?.peerKey)}`.trim()
}

/** The sentence for the state of a transfer. `fail` turns its error into a sentence. */
export function statusText(t, fail, lang, transfer, name) {
  const { state, direction } = transfer
  if (state === 'failed') {
    return fail(transfer.error, t('state.failed'))
  }
  if (transfer.stalled && (state === 'sending' || state === 'receiving')) {
    return t(`state.${state}.stalled`)
  }
  if (state === 'done' && transfer.kind === 'folder' && transfer.filesTotal > 0) {
    return t('state.done.folder', { n: transfer.filesTotal, count: formatCount(transfer.filesTotal, lang) })
  }
  const key = BY_DIRECTION.includes(state) ? `state.${state}.${direction === 'send' ? 'send' : 'receive'}` : `state.${state}`
  return t(key, { name })
}

/** Whether the row shows a progress bar: something is measured while it moves. */
export function hasProgress(transfer) {
  return ['preparing', 'sending', 'receiving', 'verifying'].includes(transfer.state)
}

/**
 * What a row shows and offers for the state of its item. A receive that is still being prepared by the other
 * side has no Save, Decline or Cancel: there is nothing to answer yet. Its bar sweeps until a total is known.
 */
export function rowView(transfer) {
  const { state, direction } = transfer
  const receive = direction === 'receive'
  const progress = hasProgress(transfer)
  return {
    progress,
    indeterminate: progress && !(transfer.bytesTotal > 0),
    save: receive && state === 'offered',
    decline: receive && (state === 'offered' || state === 'queued'),
    cancel: (!receive && ['preparing', 'offered', 'sending'].includes(state)) || (receive && ['receiving', 'verifying'].includes(state)),
  }
}

/**
 * The numbers under the bar, said for what they count: bytes read and stored here while preparing, bytes the
 * other computer downloaded while sending, bytes received while receiving. The speed is the last few seconds.
 */
export function numbersText(t, lang, transfer, name, formatRate) {
  const { state, direction, bytesDone, bytesTotal } = transfer
  const vars = { name, done: formatBytes(bytesDone, lang), total: formatBytes(bytesTotal, lang) }
  const known = bytesTotal > 0
  let main
  if (state === 'preparing' && direction === 'send') {
    main = known ? t('stat.prepared', vars) : t('stat.prepared.unknown', vars)
  } else if (state === 'sending') {
    main = known ? t('stat.pulled', vars) : vars.done
  } else if (state === 'receiving') {
    main = known ? t('stat.received', vars) : vars.done
  } else if (state === 'preparing') {
    main = known ? t('stat.of', vars) : bytesDone > 0 ? vars.done : ''
  } else {
    main = known ? t('stat.of', vars) : vars.done
  }
  const rate = transfer.bytesPerSecond > 0 && !transfer.stalled ? formatRate(transfer.bytesPerSecond, lang) : null
  return [main, rate].filter(Boolean).join(' · ')
}

/** The sentence of an ended session, by its reason. */
export function endSentence(t, session) {
  const name = peerLabel(session)
  switch (session.reason) {
    case 'closed':
      return t('session.ended.self')
    case 'peer-closed':
      return t('session.ended.peer', { name })
    case 'replaced':
      return t('session.ended.replaced', { name })
    case 'peer-protocol':
      return t('session.ended.protocol')
    default:
      return t('session.ended.app')
  }
}

/** The pairing to keep when a call result and an event cross: never go back to an earlier state of the same pairing. */
const RANK = { '': 0, starting: 1, looking: 1, waiting: 2, joining: 2, connected: 3, expired: 3, cancelled: 3, failed: 3 }

export function newerPairing(prev, next) {
  if (!next) {
    return prev
  }
  if (prev?.id && prev.id === next.id && (RANK[prev.state] ?? 0) > (RANK[next.state] ?? 0)) {
    return prev
  }
  return next
}


/** How many finished items of a session the window keeps: as many as the history holds. */
export const KEEP_FINISHED = 50

/**
 * The transfers to keep after `next` arrived: every running one, and the newest KEEP_FINISHED finished ones of
 * each session, so a peer that offers and withdraws without end cannot grow the window.
 */
export function keepTransfers(list, next) {
  const all = [next, ...list.filter((x) => x.id !== next.id)]
  const finished = {}
  return all.filter((x) => {
    if (!FINAL.includes(x.state)) {
      return true
    }
    finished[x.sessionId] = (finished[x.sessionId] ?? 0) + 1
    return finished[x.sessionId] <= KEEP_FINISHED
  })
}

/** Whether leaving the Connect page must cancel the code it shows: a code dies when its page is left. */
export function codeToCancel(wasOnConnect, isOnConnect, pairing) {
  return wasOnConnect && !isOnConnect && pairing?.role === 'inviter' && ['starting', 'waiting'].includes(pairing.state)
}
