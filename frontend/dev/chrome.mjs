// Headless Google Chrome driven through its DevTools protocol, for dev/shots.mjs and test/crash.test.mjs. Needs Google
// Chrome; nothing to install. Its profile lives in a temp directory, removed by close().
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

export const CHROME = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

export const hasChrome = () => existsSync(CHROME)

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** Starts Chrome with flags and returns send (a protocol call), evaluate, until (waits up to 10 s for an expression) and close. */
export async function openChrome(flags = []) {
  const profile = mkdtempSync(join(tmpdir(), 'kolo-share-chrome-'))
  const chrome = spawn(CHROME, ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run', ...flags, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] })
  const wsUrl = await new Promise((resolve, reject) => {
    let text = ''
    chrome.stderr.on('data', (chunk) => {
      text += chunk
      const m = text.match(/DevTools listening on (ws:\/\/\S+)/)
      if (m) {
        resolve(m[1])
      }
    })
    chrome.on('exit', () => reject(new Error('Chrome stopped before it was ready')))
  })
  const port = new URL(wsUrl).port
  const targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json()
  const ws = new WebSocket(targets.find((x) => x.type === 'page').webSocketDebuggerUrl)
  await new Promise((resolve) => ws.addEventListener('open', resolve))
  let seq = 0
  const waiting = new Map()
  ws.addEventListener('message', (event) => {
    const msg = JSON.parse(event.data)
    if (msg.id && waiting.has(msg.id)) {
      waiting.get(msg.id)(msg)
      waiting.delete(msg.id)
    }
  })
  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = ++seq
      waiting.set(id, (msg) => (msg.error ? reject(new Error(`${method}: ${msg.error.message}`)) : resolve(msg.result)))
      ws.send(JSON.stringify({ id, method, params }))
    })
  const evaluate = async (expression) => {
    const result = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
    if (result.exceptionDetails) {
      throw new Error(`${expression.slice(0, 80)}: ${result.exceptionDetails.exception?.description ?? result.exceptionDetails.text}`)
    }
    return result.result.value
  }
  const until = async (expression) => {
    for (let i = 0; i < 100; i++) {
      if (await evaluate(expression)) {
        return true
      }
      await sleep(100)
    }
    return false
  }
  const close = async () => {
    ws.close()
    chrome.kill()
    await sleep(300)
    rmSync(profile, { recursive: true, force: true })
  }
  return { send, evaluate, until, close }
}
