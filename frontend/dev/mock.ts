// A stand-in for the Go service, for design work in a browser: `npm run dev`, then open
// http://127.0.0.1:9245/?mock (a live simulation) or ?mock=<scene> (one frozen state).
// It has the shape of the generated bindings plus the two runtime objects the window uses.
// src/service.js only loads it in a dev server, so a production build never includes it.

interface ShareError {
  kind: string
  reason: string
  detail: string
  cause: string
  message: string
}

interface Pairing {
  id: string
  role: 'inviter' | 'joiner' | ''
  state: string
  code: string
  expires: number
  error: ShareError | null
  sessionId: string
}

interface Session {
  id: string
  peerName: string
  peerKey: string
  peerId?: string
  fingerprint?: string
  state: 'connecting' | 'connected' | 'reconnecting' | 'ended'
  reason: string
  since: number
  started: number
  folder: string
  autoSave: boolean
  waiting: number
  unreachable: boolean
  notice: ShareError | null
}

interface Transfer {
  id: string
  direction: 'send' | 'receive'
  kind: 'file' | 'folder'
  name: string
  state: string
  sessionId: string
  peerName: string
  bytesDone: number
  bytesTotal: number
  filesDone: number
  filesTotal: number
  skipped: number
  stalled: boolean
  bytesPerSecond: number
  savedTo: string
  error: ShareError | null
  started: number
  finished: number
}

interface HistoryItem {
  id: string
  direction: 'send' | 'receive'
  kind: 'file' | 'folder'
  name: string
  result: string
  message: string
  files: number
  bytes: number
  savedTo: string
  at: number
  error: ShareError | null
  peerName: string
  peerKey: string
  peerId?: string
  fingerprint?: string
}

const scene = new URLSearchParams(window.location.search).get('mock') || 'live'
const frozen = scene !== 'live'
// nulls: every list as null, from the methods and the events, as Go would send a nil slice.
const nulls = scene === 'nulls'
const now = Date.now()
const MB = 1000 * 1000
const GB = 1000 * MB
const HOUR = 3600 * 1000
const DOWNLOADS = '/home/ana/Downloads'
// 92 characters, as kolo-invite writes them: koloinv0 and base32 of 52 bytes.
const CODE = 'koloinv0' + 'mfrggzdfmztwq2lknnwg23tpobyxe43uov3ho6dzpjzgk4tbmvqwy3lpmnxxezlsmfrggzdfmztwq2lknnwg23tp'.slice(0, 84)
// Names a hostile peer could use: a right-to-left override that hides an extension, zero-width
// characters, and text that looks like markup.
const SPOOF_NAME = 'invoice-‮gnp.exe'
const HOSTILE_NAME = '<img src=x onerror=alert(1)>​‍⁦report⁩﻿.pdf'
const HOSTILE_PEER = 'Ana‮​ (admin)'
const LONG_NAME = 'Quarterly field recordings, northern route (final master, corrected levels) 2026-11-14 take 03.tar.zst'

const handlers = new Map<string, Set<(event: { name: string; data: unknown }) => void>>()
let netSeq = 1
const emit = (name: string, data: unknown) => {
  // As the service does: every network state has a larger seq than the one before.
  const payload = name === 'share:network' ? { since: Date.now(), ...(data as object), seq: ++netSeq } : data
  handlers.get(name)?.forEach((cb) => cb({ name, data: payload }))
}

// dev/shots.mjs sends events the Go side would send, such as a file drop; test/crash.test.mjs reads what the
// window sent to ReportError and the clipboard.
const reports: { message: string; stack: string }[] = []
Object.assign(window, { mockEmit: emit, mockReports: reports, mockClipboard: () => clipboard })

export const Events = {
  On(name: string, cb: (event: { name: string; data: unknown }) => void) {
    if (!handlers.has(name)) {
      handlers.set(name, new Set())
    }
    handlers.get(name)!.add(cb)
    return () => handlers.get(name)?.delete(cb)
  },
}

let clipboard = CODE.replace(/a/, 'b')
export const Clipboard = {
  async SetText(text: string) {
    clipboard = text
  },
  async Text() {
    return clipboard
  },
}

const err = (kind: string, reason: string, message: string, detail = '', cause = ''): ShareError => ({ kind, reason, detail, cause, message })

/** A rejected call, shaped like the one the Wails runtime throws: the Go error is its cause. */
function reject(error: ShareError): never {
  throw Object.assign(new Error(error.message), { cause: error })
}

let uid = 0
const nextId = (prefix: string) => `${prefix}${++uid}`

/** The whole key and the 128-bit fingerprint of a mock peer, made from its short key. */
const pid = (key: string) => key.repeat(8)
const fp = (key: string) => (key + [...key].reverse().join('') + key + key.slice(4) + key.slice(0, 4)).match(/.{4}/g)!.join(' ')

function session(patch: Partial<Session>): Session {
  const s = { id: nextId('s'), peerName: 'Ana Ribeiro', peerKey: 'a1b2c3d4', state: 'connected', reason: '', since: now - 20 * 60 * 1000, started: now - 20 * 60 * 1000, folder: DOWNLOADS, autoSave: false, waiting: 0, unreachable: false, notice: null, ...patch }
  return { ...s, peerId: s.peerId ?? pid(s.peerKey), fingerprint: s.fingerprint ?? fp(s.peerKey) }
}

function transfer(s: Session, patch: Partial<Transfer>): Transfer {
  return { id: nextId('t'), direction: 'receive', kind: 'file', name: 'field-notes.pdf', state: 'offered', sessionId: s.id, peerName: s.peerName, bytesDone: 0, bytesTotal: 4.2 * MB, filesDone: 0, filesTotal: 1, skipped: 0, stalled: false, bytesPerSecond: 0, savedTo: '', error: null, started: now, finished: 0, ...patch }
}

const LOST = err('lost', 'lost', 'The connection to the other computer was lost. Nothing was saved.')
const ENDED = err('cancelled', 'session-ended', 'This session is closed.')

let sessions: Session[] = []
let transfers: Transfer[] = []
let lastResult: Transfer | null = null
let pairing: Pairing = { id: '', role: '', state: '', code: '', expires: 0, error: null, sessionId: '' }
let network: { state: string; mode?: string; message: string; error?: ShareError | null; entryNodes: number; firstNode: string; localNode?: string; since?: number } = { state: 'ready', message: '', error: null, entryNodes: 2, firstNode: '', mode: 'own' }
let settings = {
  mode: 'own',
  entryNodes: '203.0.113.10:4000,entry.example.org:4000',
  entryNodesFrom: 'flag',
  firstNode: '',
  localNode: '',
  dataDir: '/home/ana/Library/Application Support/kolo-share',
  listen: '0.0.0.0:0',
  maxDownload: 32 * 1024 * 1024 * 1024,
  maxFiles: 100000,
  showInNotch: true,
  displayName: '',
  publicKey: 'e4c1a9f0',
  fingerprint: fp('e4c1a9f0'),
  pairTTL: HOUR,
}

const ana = { peerName: 'Ana Ribeiro', peerKey: 'a1b2c3d4', peerId: pid('a1b2c3d4') }
const bruno = { peerName: 'Bruno', peerKey: '7f3e9c21', peerId: pid('7f3e9c21') }
const sampleHistory: HistoryItem[] = [
  { id: 'h1', direction: 'receive', kind: 'file', name: 'field-notes.pdf', result: 'done', message: '', files: 1, bytes: 4.2 * MB, savedTo: '/home/ana/Downloads/field-notes.pdf', at: now - 12 * 60 * 1000, error: null, ...ana },
  { id: 'h2', direction: 'send', kind: 'folder', name: 'Tour photos', result: 'delivered', message: '', files: 214, bytes: 1.84 * GB, savedTo: '', at: now - 3 * HOUR, error: null, ...bruno },
  { id: 'h3', direction: 'receive', kind: 'file', name: LONG_NAME, result: 'failed', message: LOST.message, files: 0, bytes: 0, savedTo: '', at: now - 26 * HOUR, error: LOST, ...ana },
  { id: 'h4', direction: 'send', kind: 'file', name: 'rehearsal-2026-11-09.wav', result: 'declined', message: '', files: 1, bytes: 512.4 * MB, savedTo: '', at: now - 3 * 24 * HOUR, error: null, ...ana },
  { id: 'h5', direction: 'receive', kind: 'folder', name: 'contracts', result: 'cancelled', message: '', files: 0, bytes: 0, savedTo: '', at: now - 6 * 24 * HOUR, error: null, ...bruno },
  { id: 'h6', direction: 'receive', kind: 'file', name: 'disk-image.iso', result: 'done', message: '', files: 1, bytes: 12.7 * GB, savedTo: '/home/ana/Downloads/disk-image.iso', at: now - 9 * 24 * HOUR, error: null, ...ana },
  { id: 'h7', direction: 'send', kind: 'file', name: 'budget-2027.ods', result: 'stopped', message: '', files: 1, bytes: 88 * 1000, savedTo: '', at: now - 12 * 24 * HOUR, error: null, peerName: 'Carla Méndez', peerKey: '0c9d4e12', peerId: pid('0c9d4e12') },
]
let history: HistoryItem[] = scene === 'empty' ? [] : sampleHistory

/** Ana's session with items in both directions, in every state a row can show. */
function busySession(patch: Partial<Session> = {}) {
  const s = session(patch)
  const t = (p: Partial<Transfer>) => transfer(s, p)
  transfers.push(
    t({ name: 'Tour photos', kind: 'folder', bytesTotal: 1.84 * GB, filesTotal: 214, started: now - 50_000 }),
    t({ name: LONG_NAME, bytesTotal: 41 * GB, started: now - 40_000 }),
    t({ name: 'rehearsal-2026-11-09.wav', state: 'queued', bytesTotal: 512.4 * MB, started: now - 30_000 }),
    t({ name: 'contracts', kind: 'folder', state: 'receiving', bytesDone: 0.74 * GB, bytesTotal: 1.2 * GB, filesTotal: 38, bytesPerSecond: 11.2 * MB, started: now - 90_000 }),
    t({ name: 'wedding-film.mov', state: 'preparing', bytesDone: 1.2 * GB, bytesTotal: 5.27 * GB, started: now - 60_000 }),
    t({ name: 'Archive 2019', kind: 'folder', state: 'preparing', bytesDone: 0, bytesTotal: 0, started: now - 55_000 }),
    t({ name: 'tour-master.wav', state: 'receiving', stalled: true, bytesDone: 0.31 * GB, bytesTotal: 1.1 * GB, bytesPerSecond: 0, started: now - 80_000 }),
    t({ name: 'press-kit.zip', state: 'failed', error: err('lost', 'prepare-stalled', 'Nothing came from the other computer while it prepared this item, so this app stopped waiting.'), started: now - 450_000, finished: now - 330_000 }),
    t({ name: 'site-plans.zip', state: 'verifying', bytesDone: 61 * MB, bytesTotal: 96 * MB, bytesPerSecond: 182 * MB, started: now - 120_000 }),
    t({ name: 'field-notes.pdf', state: 'done', bytesDone: 4.2 * MB, filesDone: 1, savedTo: '/home/ana/Downloads/field-notes.pdf', started: now - 300_000, finished: now - 240_000 }),
    t({ name: 'old-draft.docx', state: 'declined', bytesTotal: 220 * 1000, started: now - 400_000, finished: now - 380_000 }),
    t({ name: 'mixdown-v2.flac', state: 'withdrawn', bytesTotal: 640 * MB, started: now - 500_000, finished: now - 460_000 }),
    t({ direction: 'send', name: 'Invoices 2026', kind: 'folder', state: 'preparing', bytesDone: 30 * MB, bytesTotal: 140 * MB, filesTotal: 64, bytesPerSecond: 96 * MB, started: now - 5_000 }),
    t({ direction: 'send', name: 'cover-art.png', state: 'offered', bytesTotal: 8.8 * MB, started: now - 20_000 }),
    t({ direction: 'send', name: 'Raw footage', state: 'sending', stalled: true, bytesDone: 1.4 * GB, bytesTotal: 5.27 * GB, bytesPerSecond: 0, started: now - 250_000 }),
    t({ direction: 'send', name: 'Tour video, day 3.mov', state: 'sending', bytesDone: 2.1 * GB, bytesTotal: 3.6 * GB, bytesPerSecond: 24.6 * MB, started: now - 200_000 }),
    t({ direction: 'send', name: 'setlist.txt', state: 'delivered', bytesDone: 2 * 1000, bytesTotal: 2 * 1000, started: now - 600_000, finished: now - 590_000 }),
    t({ direction: 'send', name: 'demo-tape.mp3', state: 'declined', bytesTotal: 9.4 * MB, started: now - 700_000, finished: now - 650_000 }),
    t({ direction: 'send', name: 'stems', kind: 'folder', state: 'cancelled', bytesDone: 0.3 * GB, bytesTotal: 2.2 * GB, filesTotal: 12, started: now - 800_000, finished: now - 760_000 }),
    t({ direction: 'send', name: 'Photos (private)', kind: 'folder', state: 'failed', bytesTotal: 0, filesTotal: 0, error: err('source', 'source-unreadable', 'Could not read /home/ana/Pictures/private: permission denied.', '/home/ana/Pictures/private', 'permission denied'), started: now - 900_000, finished: now - 899_000 }),
  )
  return s
}

const waitingCode = (): Pairing => ({ id: 'p1', role: 'inviter', state: 'waiting', code: CODE, expires: Date.now() + 59 * 60_000 + 12_000, error: null, sessionId: '' })

// One frozen state per scene, so every screen can be looked at without the Go side.
const scenes: Record<string, () => void> = {
  live() {},
  idle() {},
  nulls() {
    window.setTimeout(() => ['share:sessions', 'share:history', 'share:dropped'].forEach((name) => emit(name, null)), 200)
  },
  crash() {},
  empty() {},
  code() {
    pairing = waitingCode()
  },
  'code-starting'() {
    pairing = { ...waitingCode(), state: 'starting', code: '', expires: 0 }
  },
  'code-expired'() {
    pairing = { ...waitingCode(), state: 'expired', code: '', expires: 0, error: err('refused', 'pair-expired', 'This code expired. Ask for a new one.') }
  },
  looking() {
    pairing = { id: 'p1', role: 'joiner', state: 'looking', code: '', expires: 0, error: null, sessionId: '' }
  },
  joining() {
    pairing = { id: 'p1', role: 'joiner', state: 'joining', code: '', expires: 0, error: null, sessionId: '' }
  },
  'pair-failed'() {
    pairing = { id: 'p1', role: 'joiner', state: 'failed', code: '', expires: 0, error: err('not-found', 'pair-not-found', 'Could not find the computer that made this code. Check that its app is open and shows the code, then try again.'), sessionId: '' }
  },
  session() {
    sessions = [busySession(), session({ ...bruno, state: 'reconnecting', unreachable: true }), session({ peerName: 'Carla Méndez', peerKey: '0c9d4e12', state: 'ended', reason: 'peer-closed' })]
  },
  own() {
    scenes.session()
  },
  'public-session'() {
    scenes.public()
    scenes.session()
  },
  'local-session'() {
    scenes.local()
    scenes.session()
  },
  'session-empty'() {
    history = []
    sessions = [session({})]
  },
  'session-history'() {
    sessions = [session({})]
  },
  reconnecting() {
    const s = session({ state: 'reconnecting', unreachable: true, since: now - 4 * 60_000 })
    transfers.push(
      transfer(s, { name: 'Tour photos', kind: 'folder', bytesTotal: 1.84 * GB, filesTotal: 214, error: LOST }),
      transfer(s, { direction: 'send', name: 'cover-art.png', state: 'offered', bytesTotal: 8.8 * MB }),
      transfer(s, { name: 'field-notes.pdf', state: 'done', bytesDone: 4.2 * MB, savedTo: '/home/ana/Downloads/field-notes.pdf', finished: now - 600_000 }),
    )
    sessions = [s]
  },
  ended() {
    const s = session({ state: 'ended', reason: 'peer-closed', since: now - 60_000 })
    transfers.push(
      transfer(s, { name: 'field-notes.pdf', state: 'done', bytesDone: 4.2 * MB, savedTo: '/home/ana/Downloads/field-notes.pdf', finished: now - 600_000 }),
      transfer(s, { name: 'Tour photos', kind: 'folder', state: 'stopped', bytesTotal: 1.84 * GB, filesTotal: 214, error: ENDED, finished: now - 60_000 }),
      transfer(s, { direction: 'send', name: 'cover-art.png', state: 'stopped', bytesTotal: 8.8 * MB, error: ENDED, finished: now - 60_000 }),
    )
    sessions = [s]
  },
  'ended-empty'() {
    history = []
    sessions = [session({ state: 'ended', reason: 'replaced' })]
  },
  notice() {
    const s = session({ notice: err('too-large', 'too-large', 'This item is larger than the download limit of 32 GiB. Nothing was saved.', '32 GiB') })
    transfers.push(transfer(s, { name: 'Tour photos', kind: 'folder', bytesTotal: 1.84 * GB, filesTotal: 214 }))
    sessions = [s]
  },
  'notice-limit'() {
    const s = session({ notice: err('limit', 'offer-limit', 'Declined: the other computer offered more items than this app takes at once.') })
    transfers.push(transfer(s, { name: 'Tour photos', kind: 'folder', bytesTotal: 1.84 * GB, filesTotal: 214 }))
    sessions = [s]
  },
  autosave() {
    sessions = [session({ autoSave: true })]
    transfers.push(transfer(sessions[0], { name: 'Tour photos', kind: 'folder', state: 'receiving', bytesDone: 0.4 * GB, bytesTotal: 1.84 * GB, filesTotal: 214, bytesPerSecond: 11 * MB }))
  },
  'no-folder'() {
    sessions = [session({ folder: '' })]
    transfers.push(transfer(sessions[0], {}))
  },
  hostile() {
    const s = session({ peerName: HOSTILE_PEER })
    transfers.push(
      transfer(s, { name: SPOOF_NAME }),
      transfer(s, { name: HOSTILE_NAME, state: 'done', bytesDone: 4.2 * MB, savedTo: `/home/ana/Downloads/${HOSTILE_NAME}` }),
    )
    sessions = [s]
    history = [{ id: 'x1', direction: 'receive', kind: 'file', name: SPOOF_NAME, result: 'done', message: '', files: 1, bytes: 4.2 * MB, savedTo: `/home/ana/Downloads/${SPOOF_NAME}`, at: now, error: null, peerName: HOSTILE_PEER, peerKey: 'a1b2c3d4', peerId: pid('a1b2c3d4') }, ...history]
  },
  many() {
    sessions = [
      busySession(),
      session({ ...bruno, state: 'reconnecting' }),
      session({ peerName: 'Carla Méndez', peerKey: '0c9d4e12', state: 'ended', reason: 'closed' }),
      session({ peerName: 'Kolo 5be20a77', peerKey: '5be20a77', state: 'connecting' }),
      session({ peerName: 'Studio laptop (second floor, by the window)', peerKey: '91aa0c3f' }),
      session({ peerName: 'Diego', peerKey: '3d0f6b18' }),
    ]
    transfers.push(transfer(sessions[5], { name: 'notes.md' }), transfer(sessions[5], { name: 'scan.pdf' }))
  },
  settings() {
    settings = { ...settings, displayName: 'Ana at the studio' }
  },
  starting() {
    network = { state: 'starting', message: '', entryNodes: 2, firstNode: '', mode: 'own', since: Date.now() }
  },
  public() {
    settings = { ...settings, mode: 'public', entryNodes: '', entryNodesFrom: 'public' }
    network = { ...network, mode: 'public', entryNodes: 2 }
  },
  'public-failed'() {
    const message = 'Could not reach the public network: timeout. Check your internet connection, or start Kolo Share with -bootstrap and the entry nodes of your own network, or with -local to share on this computer only.'
    settings = { ...settings, mode: 'public', entryNodes: '', entryNodesFrom: 'public' }
    network = { state: 'failed', message, error: err('network', 'public-unreachable', message, '', 'timeout'), entryNodes: 2, firstNode: '', mode: 'public' }
  },
  'net-failed'() {
    network = { state: 'failed', message: 'Could not reach the entry nodes (203.0.113.10:4000, entry.example.org:4000): timeout. Check the list given with -bootstrap.', error: err('network', 'bootstrap-failed', 'Could not reach the entry nodes (203.0.113.10:4000, entry.example.org:4000): timeout. Check the list given with -bootstrap.', '203.0.113.10:4000, entry.example.org:4000', 'timeout'), entryNodes: 2, firstNode: '', mode: 'own' }
  },
  local() {
    settings = { ...settings, mode: 'local', entryNodes: '', entryNodesFrom: 'local', localNode: '127.0.0.1:7451', firstNode: '127.0.0.1:7451' }
    network = { ...network, mode: 'local', localNode: '127.0.0.1:7451' }
  },
  // Scenes for the notch page: one thing each.
  'n-offer'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { name: 'Tour photos', kind: 'folder', bytesTotal: 1.84 * GB, filesTotal: 214 }), transfer(sessions[0], { name: 'b.pdf' }))
  },
  'n-offer-long'() {
    sessions = [session({ peerName: 'Studio laptop (second floor, by the window)' })]
    transfers.push(transfer(sessions[0], { name: LONG_NAME, bytesTotal: 41 * GB }))
  },
  'n-code'() {
    pairing = waitingCode()
  },
  'n-preparing'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { direction: 'send', name: 'Invoices 2026', kind: 'folder', state: 'preparing', bytesDone: 30 * MB, bytesTotal: 140 * MB, filesTotal: 64, bytesPerSecond: 96 * MB }))
  },
  'n-prepared-by-peer'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { name: 'wedding-film.mov', state: 'preparing', bytesDone: 1.2 * GB, bytesTotal: 5.27 * GB }))
  },
  'n-waiting'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { name: 'tour-master.wav', state: 'receiving', stalled: true, bytesDone: 0.31 * GB, bytesTotal: 1.1 * GB }))
  },
  'n-sending'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { direction: 'send', name: 'Tour video, day 3.mov', state: 'sending', bytesDone: 2.1 * GB, bytesTotal: 3.6 * GB, bytesPerSecond: 24.6 * MB }), transfer(sessions[0], { direction: 'send', name: 'x', state: 'offered' }))
  },
  'n-offered'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { direction: 'send', name: 'cover-art.png', state: 'offered', bytesTotal: 8.8 * MB }))
  },
  'n-queued'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { name: 'rehearsal.wav', state: 'queued', bytesTotal: 512 * MB }))
  },
  'n-receiving'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { name: 'contracts', kind: 'folder', state: 'receiving', bytesDone: 0.74 * GB, bytesTotal: 1.2 * GB, filesTotal: 38, bytesPerSecond: 11.2 * MB }))
  },
  'n-verifying'() {
    sessions = [session({})]
    transfers.push(transfer(sessions[0], { name: 'site-plans.zip', state: 'verifying', bytesDone: 61 * MB, bytesTotal: 96 * MB, bytesPerSecond: 182 * MB }))
  },
  'n-done'() {
    lastResult = transfer(session({}), { name: 'Tour photos', kind: 'folder', state: 'done', bytesDone: 1.84 * GB, bytesTotal: 1.84 * GB, filesTotal: 214, savedTo: '/home/ana/Downloads/Tour photos' })
  },
  'n-delivered'() {
    lastResult = transfer(session({}), { direction: 'send', name: 'setlist.txt', state: 'delivered', bytesDone: 2000, bytesTotal: 2000 })
  },
  'n-declined'() {
    lastResult = transfer(session({}), { direction: 'send', name: 'demo-tape.mp3', state: 'declined', bytesTotal: 9.4 * MB })
  },
  'n-withdrawn'() {
    lastResult = transfer(session({}), { name: 'mixdown-v2.flac', state: 'withdrawn', bytesTotal: 640 * MB })
  },
  'n-failed'() {
    lastResult = transfer(session({}), { name: 'site-plans.zip', state: 'failed', error: err('corrupt', 'offer-mismatch', 'What arrived did not match what was offered. Nothing was saved.') })
  },
  'n-hostile'() {
    sessions = [session({ peerName: HOSTILE_PEER })]
    transfers.push(transfer(sessions[0], { name: SPOOF_NAME }))
  },
}
;(scenes[scene] ?? scenes.live)()

const pause = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms))
const OPEN_SEND = ['preparing', 'offered', 'sending']
const RUNNING = ['preparing', 'sending', 'queued', 'receiving', 'verifying']
const FINAL = ['delivered', 'declined', 'cancelled', 'stopped', 'failed', 'done', 'withdrawn']

/** Recounts the offers each session waits on, as the service does, and sends the whole list. */
function syncSessions() {
  sessions = sessions.map((s) => ({ ...s, waiting: transfers.filter((x) => x.sessionId === s.id && x.direction === 'receive' && x.state === 'offered').length }))
  emit('share:sessions', sessions)
  emit('share:notch', notchState())
}
syncSessions()

function changeSession(id: string, patch: Partial<Session>) {
  sessions = sessions.map((s) => (s.id === id ? { ...s, ...patch } : s))
  syncSessions()
  return sessions.find((s) => s.id === id)!
}

function change(id: string, patch: Partial<Transfer>) {
  transfers = transfers.map((x) => (x.id === id ? { ...x, ...patch } : x))
  const next = transfers.find((x) => x.id === id)
  if (next) {
    emit('share:transfer', next)
    if (FINAL.includes(next.state)) {
      lastResult = next
      finish(next)
    }
    syncSessions()
  }
  return next
}

function finish(item: Transfer) {
  // Offers withdrawn or declined by this app before anyone answered are not in the history.
  if (item.state === 'withdrawn') {
    return
  }
  const s = sessions.find((x) => x.id === item.sessionId)
  history = [{ id: item.id, direction: item.direction, kind: item.kind, name: item.name, result: item.state, message: item.error?.message ?? '', files: item.filesTotal, bytes: item.bytesTotal, savedTo: item.savedTo, at: Date.now(), error: item.error, peerName: item.peerName, peerKey: s?.peerKey ?? '', peerId: s?.peerId ?? '' }, ...history].slice(0, 50)
  emit('share:history', history)
}

const alive = (id: string, ...states: string[]) => transfers.some((x) => x.id === id && states.includes(x.state))

async function runSend(id: string) {
  const item = transfers.find((x) => x.id === id)!
  for (let done = 0; done < item.bytesTotal && alive(id, 'preparing'); done += item.bytesTotal / 8) {
    change(id, { bytesDone: done, bytesPerSecond: 96 * MB })
    await pause(250)
  }
  if (!alive(id, 'preparing')) {
    return
  }
  change(id, { state: 'offered', bytesDone: 0, bytesPerSecond: 0 })
  await pause(2500)
  for (let done = 0; done < item.bytesTotal && alive(id, 'offered', 'sending'); done += item.bytesTotal / 20) {
    change(id, { state: 'sending', bytesDone: done, bytesPerSecond: 11.2 * MB })
    await pause(250)
  }
  if (alive(id, 'sending')) {
    change(id, { state: 'delivered', bytesDone: item.bytesTotal, bytesPerSecond: 0, finished: Date.now() })
  }
}

async function runReceive(id: string) {
  // One receive at a time per session: the next waits in "queued".
  const item = () => transfers.find((x) => x.id === id)!
  while (transfers.some((x) => x.id !== id && x.sessionId === item().sessionId && ['receiving', 'verifying'].includes(x.state)) && alive(id, 'queued')) {
    await pause(300)
  }
  const total = item().bytesTotal
  for (let done = 0; done < total && alive(id, 'queued', 'receiving'); done += total / 16) {
    change(id, { state: 'receiving', bytesDone: done, bytesPerSecond: 11.2 * MB })
    await pause(250)
  }
  for (let step = 0; step <= 10 && alive(id, 'receiving', 'verifying'); step++) {
    change(id, { state: 'verifying', bytesDone: (total * step) / 10, bytesPerSecond: 182 * MB })
    await pause(150)
  }
  if (alive(id, 'verifying')) {
    const s = sessions.find((x) => x.id === item().sessionId)
    change(id, { state: 'done', bytesDone: total, bytesPerSecond: 0, filesDone: item().filesTotal, savedTo: `${s?.folder || DOWNLOADS}/${item().name}`, finished: Date.now() })
  }
}

/** A peer of the live simulation that offers two items a few seconds after the session starts. */
async function peerOffers(s: Session) {
  await pause(3000)
  for (const patch of [{ name: 'field-notes.pdf' }, { name: 'Tour photos', kind: 'folder' as const, bytesTotal: 1.84 * GB, filesTotal: 214 }]) {
    if (sessions.find((x) => x.id === s.id)?.state !== 'connected') {
      return
    }
    const item = transfer(s, { ...patch, started: Date.now() })
    transfers = [...transfers, item]
    emit('share:transfer', item)
    syncSessions()
    if (sessions.find((x) => x.id === s.id)?.autoSave) {
      SaveOffer(item.id).catch(() => {})
    }
    await pause(1500)
  }
}

function addSession(peer: { peerName: string; peerKey: string }) {
  const s = session({ ...peer, state: 'connecting', since: Date.now(), started: Date.now() })
  sessions = [...sessions, s]
  syncSessions()
  window.setTimeout(() => {
    changeSession(s.id, { state: 'connected', since: Date.now() })
    peerOffers(s)
  }, 700)
  return s
}

function setPairing(next: Pairing) {
  pairing = next
  emit('share:pairing', pairing)
  emit('share:notch', notchState())
  if (['connected', 'expired', 'cancelled', 'failed'].includes(next.state)) {
    pairing = { id: '', role: '', state: '', code: '', expires: 0, error: null, sessionId: '' }
    emit('share:notch', notchState())
  }
}

// What the notch window shows: an incoming offer (oldest first), a running receive, a running send,
// a waiting pairing code, the last result.
function notchState() {
  const offers = transfers.filter((x) => x.direction === 'receive' && x.state === 'offered').sort((a, b) => a.started - b.started)
  const running = transfers.filter((x) => RUNNING.includes(x.state) || (x.direction === 'send' && x.state === 'offered'))
  const shown = offers[0] ?? running.find((x) => x.direction === 'receive') ?? running.find((x) => x.direction === 'send') ?? null
  const empty = { active: false, id: '', direction: '', kind: '', name: '', state: '', percent: -1, bytesDone: 0, bytesTotal: 0, bytesPerSecond: 0, hasCode: false, message: '', error: null, filesTotal: 0, others: 0, peerName: '', hasOffer: false }
  if (!shown && pairing.state === 'waiting') {
    return { ...empty, active: true, state: 'waiting', hasCode: true }
  }
  const item = shown ?? lastResult
  if (!item) {
    return empty
  }
  const active = !FINAL.includes(item.state)
  const measured = item.bytesTotal > 0 && ['preparing', 'sending', 'receiving', 'verifying'].includes(item.state)
  return {
    ...empty,
    active,
    id: item.id,
    direction: item.direction,
    kind: item.kind,
    name: item.name,
    state: item.state,
    percent: item.state === 'done' ? 100 : measured ? Math.min(100, (100 * item.bytesDone) / item.bytesTotal) : -1,
    bytesDone: item.bytesDone,
    bytesTotal: item.bytesTotal,
    bytesPerSecond: item.bytesPerSecond,
    message: item.error?.message ?? '',
    error: item.error,
    filesTotal: item.filesTotal,
    others: active ? offers.length + running.length - 1 : 0,
    peerName: item.peerName,
    hasOffer: item.direction === 'receive' && item.state === 'offered',
  }
}

export async function Notch() {
  return notchState()
}

// The service copies the code itself, so the notch page never holds it.
export async function NotchCopyCode() {
  if (pairing.state === 'waiting') {
    clipboard = pairing.code
  }
}

// The notch page passes the ID it showed, so a click acts on that item only.
export async function NotchSaveOffer(id: string) {
  await SaveOffer(id).catch(() => NotchOpenApp())
}

export async function NotchDeclineOffer(id: string) {
  await DeclineOffer(id).catch(() => {})
}

export async function NotchHide() {
  document.body.style.opacity = '0.25'
}

export async function NotchShow() {
  document.body.style.opacity = ''
}

export async function NotchOpenApp() {}

export async function SetShowInNotch(on: boolean) {
  settings = { ...settings, showInNotch: on }
  return settings
}

const busyRestart = () => sessions.length > 0 || pairing.state !== ''

export async function Reconnect() {
  if (busyRestart()) {
    reject(err('busy', 'busy-reconnect', 'Close every session and cancel your pairing code first.'))
  }
  network = { ...network, state: 'starting', message: '', error: null }
  emit('share:network', network)
  window.setTimeout(() => {
    network = { ...network, state: 'ready' }
    emit('share:network', network)
  }, 1500)
}

const info = () => ({ dataDir: settings.dataDir, version: '0.1.0', network: { ...network, seq: netSeq }, defaultSaveFolder: DOWNLOADS })

export async function Reveal(path: string) {
  if (!history.some((x) => x.result === 'done' && x.savedTo === path) && !transfers.some((x) => x.state === 'done' && x.savedTo === path)) {
    reject(err('bad-folder', 'reveal-refused', 'Kolo Share shows only what it saved.', path))
  }
}

export async function Start() {
  if (scene === 'starting' && !frozen) {
    window.setTimeout(() => emit('share:network', { ...network, state: 'ready' }), 2000)
  }
  return info()
}

export async function Info() {
  return info()
}

export async function PickFile() {
  return '/home/ana/Documents/field-notes.pdf'
}

export async function PickFolder() {
  return '/home/ana/Pictures/Tour photos'
}

export async function PickSaveFolder() {
  return '/home/ana/Documents/From Ana'
}

export async function CreatePairing() {
  if (pairing.state !== '') {
    reject(err('busy', 'busy-pairing', 'A code is already waiting. Use it or cancel it first.'))
  }
  if (network.state !== 'ready' && network.state !== 'starting') {
    reject(err('network', 'network-not-ready', 'The network is not ready yet. Wait a moment, or press Try again.'))
  }
  const id = nextId('p')
  setPairing({ id, role: 'inviter', state: 'starting', code: '', expires: 0, error: null, sessionId: '' })
  const made = { ...pairing }
  window.setTimeout(() => {
    if (pairing.id !== id) {
      return
    }
    setPairing({ ...pairing, state: 'waiting', code: CODE, expires: Date.now() + settings.pairTTL })
    if (!frozen) {
      // Someone pastes the code after a while.
      window.setTimeout(() => {
        if (pairing.id === id && pairing.state === 'waiting') {
          const s = addSession(bruno)
          setPairing({ ...pairing, state: 'connected', code: '', expires: 0, sessionId: s.id })
        }
      }, 8000)
    }
  }, 600)
  return made
}

export async function JoinPairing(code: string) {
  const text = code.replace(/\s+/g, '')
  if (!/^koloinv0[a-z2-7]{84}$/.test(text) && !/^KOLOINV0[A-Z2-7]{84}$/.test(text)) {
    reject(err('invalid-code', 'pair-invalid-code', 'This is not a pairing code. Copy the whole code, it starts with koloinv0.'))
  }
  if (text.toLowerCase() === CODE) {
    reject(err('invalid-code', 'pair-own-code', 'This is your own code. Send it to the other person.'))
  }
  if (pairing.state !== '') {
    reject(err('busy', 'busy-pairing', 'A code is already waiting. Use it or cancel it first.'))
  }
  const id = nextId('p')
  setPairing({ id, role: 'joiner', state: 'looking', code: '', expires: 0, error: null, sessionId: '' })
  const made = { ...pairing }
  if (!frozen) {
    window.setTimeout(() => pairing.id === id && setPairing({ ...pairing, state: 'joining' }), 1500)
    window.setTimeout(() => {
      if (pairing.id === id) {
        const s = addSession(ana)
        setPairing({ ...pairing, state: 'connected', sessionId: s.id })
      }
    }, 2500)
  }
  return made
}

export async function CancelPairing(id: string) {
  if (pairing.id === id) {
    setPairing({ ...pairing, state: 'cancelled', code: '', expires: 0 })
  }
}

export async function Pairing() {
  return pairing
}

export async function Sessions() {
  // crash: a session that is not an object, so the window's render throws and shows the error pane.
  return nulls ? null : scene === 'crash' ? [null] : sessions
}

const findSession = (id: string) => sessions.find((s) => s.id === id) ?? reject(err('cancelled', 'session-ended', 'This session is closed.'))

export async function CloseSession(id: string) {
  const s = findSession(id)
  if (s.state === 'ended') {
    sessions = sessions.filter((x) => x.id !== id)
    syncSessions()
    return
  }
  for (const x of transfers.filter((y) => y.sessionId === id && !FINAL.includes(y.state))) {
    change(x.id, { state: 'stopped', bytesPerSecond: 0, error: ENDED, finished: Date.now() })
  }
  changeSession(id, { state: 'ended', reason: 'closed', since: Date.now(), autoSave: false })
}

export async function SetSessionFolder(id: string, folder: string) {
  findSession(id)
  return changeSession(id, { folder })
}

export async function SetAutoSave(id: string, on: boolean) {
  const s = findSession(id)
  if (on && !s.folder) {
    reject(err('bad-folder', 'folder-missing', 'Choose a folder to save into.'))
  }
  return changeSession(id, { autoSave: on })
}

export async function Send(sessionId: string, path: string) {
  const s = findSession(sessionId)
  if (s.state === 'ended') {
    reject(ENDED)
  }
  if (transfers.filter((x) => x.sessionId === sessionId && x.direction === 'send' && OPEN_SEND.includes(x.state)).length >= 16) {
    reject(err('busy', 'busy-offers', '16 items already wait for an answer in this session. Wait for one first.', '16'))
  }
  const folder = !/\.\w+$/.test(path)
  const item = transfer(s, { direction: 'send', name: path.split('/').pop() ?? path, kind: folder ? 'folder' : 'file', state: 'preparing', bytesTotal: folder ? 1.84 * GB : 4.2 * MB, filesTotal: folder ? 214 : 1, started: Date.now() })
  transfers = [...transfers, item]
  emit('share:transfer', item)
  syncSessions()
  runSend(item.id)
  return item.id
}

export async function SaveOffer(id: string) {
  const item = transfers.find((x) => x.id === id)
  if (!item || item.state !== 'offered') {
    reject(err('refused', 'offer-gone', 'This item is not waiting for an answer any more.'))
  }
  const s = findSession(item.sessionId)
  if (!s.folder) {
    reject(err('bad-folder', 'folder-missing', 'Choose a folder to save into.'))
  }
  if (s.state !== 'connected') {
    reject(err('lost', 'session-offline', 'The other computer is offline. Wait until the session is connected again.'))
  }
  change(id, { state: 'queued', error: null })
  runReceive(id)
}

export async function DeclineOffer(id: string) {
  if (!alive(id, 'offered', 'queued')) {
    reject(err('refused', 'offer-gone', 'This item is not waiting for an answer any more.'))
  }
  change(id, { state: 'declined', finished: Date.now() })
}

export async function CancelTransfer(id: string) {
  const item = transfers.find((x) => x.id === id)
  if (item && !FINAL.includes(item.state)) {
    change(id, { state: item.direction === 'send' ? 'stopped' : 'cancelled', bytesPerSecond: 0, finished: Date.now() })
  }
}

export async function SetDisplayName(name: string) {
  if (Array.from(name).length > 48) {
    reject(err('config', 'name-too-long', 'A name has at most 48 characters.', '48'))
  }
  settings = { ...settings, displayName: name }
  return settings
}

// A frozen scene returns its finished transfers too, so the window opens on them.
export async function Transfers() {
  return nulls ? null : transfers
}

export async function History() {
  return nulls ? null : history
}

export async function ReportError(message: string, stack: string) {
  reports.push({ message, stack })
}

export async function ClearHistory() {
  history = []
  emit('share:history', history)
}

export async function Settings() {
  return settings
}
