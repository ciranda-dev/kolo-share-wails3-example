import test from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { hasChrome, openChrome, sleep } from '../dev/chrome.mjs'

// Both windows, served with dev/mock.ts in place of Go (loopback only) in a headless Chrome: null lists never leave a
// blank window, and an error that nobody caught shows the error pane, which fills the window, reaches ReportError
// and copies.
const visible = (selector) => `(() => { const el = document.querySelector(${JSON.stringify(selector)}); return !!el && el.getClientRects().length > 0 })()`
const fills = `(() => { const r = document.querySelector('.crash').getBoundingClientRect(); return r.left === 0 && r.top === 0 && r.width === innerWidth && r.height === innerHeight })()`

test('the windows show a pane on an error, never a blank window', { skip: !hasChrome() && 'Google Chrome is not here', timeout: 120000 }, async () => {
  const { createServer } = await import('vite')
  const server = await createServer({
    configFile: fileURLToPath(new URL('../vite.config.js', import.meta.url)),
    logLevel: 'silent',
    server: { host: '127.0.0.1', port: 0, strictPort: false },
  })
  await server.listen()
  const base = `http://127.0.0.1:${server.httpServer.address().port}`
  const { send, evaluate, until, close } = await openChrome(['--lang=en-US'])
  const open = async (path, ready) => {
    await send('Page.navigate', { url: base + path })
    assert.ok(await until(ready), `${path}: ${ready} never showed`)
  }
  const press = (text) => evaluate(`[...document.querySelectorAll('button')].find((b) => b.textContent.trim() === ${JSON.stringify(text)}).click()`)
  try {
    await send('Page.enable')
    await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false })

    // Every list as null, from the methods and the events: the window shows, with no error pane.
    await open('/?mock=nulls', visible('.connect'))
    await sleep(300)
    assert.ok(await evaluate(visible('.side')), 'the sidebar shows')
    assert.equal(await evaluate(`document.querySelector('.crash') === null`), true, 'no error pane')

    // An error outside a render: the pane over the window, which Dismiss gives back.
    await evaluate(`setTimeout(() => { throw new Error('an event handler failed') })`)
    assert.ok(await until(visible('.crash')), 'the pane shows')
    assert.match(await evaluate(`document.querySelector('.crash__text').textContent`), /^Error: an event handler failed/)
    assert.ok(await evaluate(fills), 'the pane fills the window')
    assert.ok(await until(`window.mockReports.some((r) => r.message === 'Error: an event handler failed')`), 'the error reached ReportError')
    await press('Dismiss')
    assert.ok(await until(`document.querySelector('.crash') === null`), 'Dismiss hides the pane')
    assert.ok(await evaluate(visible('.connect')), 'the window goes on where it was')

    // A child that throws in its render: the pane in place of the window, with the component stack, and Copy.
    await open('/?mock=crash', visible('.crash'))
    assert.equal(await evaluate(`document.querySelector('.crash').getAttribute('role')`), 'alert')
    assert.equal(await evaluate(`document.querySelector('.crash h1').textContent`), 'This window stopped on an error')
    assert.equal(await evaluate(`document.querySelector('.app') === null`), true, 'the pane is in place of the window')
    assert.equal(await evaluate(`[...document.querySelectorAll('.crash button')].some((b) => b.textContent === 'Dismiss')`), false, 'no Dismiss after a render error')
    assert.ok(await evaluate(fills), 'the pane fills the window')
    const text = await evaluate(`document.querySelector('.crash__text').textContent`)
    assert.match(text, /^TypeError: /)
    assert.match(text, /at App/)
    assert.ok(await until(`window.mockReports.some((r) => r.message.startsWith('TypeError: ') && /at App/.test(r.stack))`), 'the error reached ReportError')
    await press('Copy the error')
    assert.ok(await until(`window.mockClipboard() === document.querySelector('.crash__text').textContent`), 'Copy copies the text')
    assert.ok(await until(`document.querySelector('.crash__status').textContent === 'Copied'`))

    // The notch window: a rejected promise nobody caught shows the pane there too, at the notch's size.
    await send('Emulation.setDeviceMetricsOverride', { width: 660, height: 92, deviceScaleFactor: 1, mobile: false })
    await open('/notch/?mock=n-offer', visible('.notch'))
    await evaluate(`void Promise.reject(new Error('the notch failed'))`)
    assert.ok(await until(visible('.crash.is-compact')), 'the notch pane shows')
    assert.ok(await evaluate(fills), 'the notch pane fills the window')
    assert.ok(await until(`window.mockReports.some((r) => r.message === 'Error: the notch failed')`), 'the notch error reached ReportError')
  } finally {
    await close()
    await server.close()
  }
})
