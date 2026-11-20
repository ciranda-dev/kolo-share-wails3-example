import test from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { hasChrome, openChrome, sleep } from '../dev/chrome.mjs'

// The shell of the window, served with dev/mock.ts in place of Go (loopback only) in a headless Chrome: the sidebar
// collapses and remembers it; the "+" menu and its two dialogs take the keyboard and give the focus back; the actions of
// a session are all reachable; the network is one quiet line with no address.
const visible = (selector) => `(() => { const el = document.querySelector(${JSON.stringify(selector)}); return !!el && el.getClientRects().length > 0 })()`

test('the sidebar collapses, the add menu and its dialogs use the keyboard, the session actions stay', { skip: !hasChrome() && 'Google Chrome is not here', timeout: 120000 }, async () => {
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
  const rect = (selector) => evaluate(`(() => { const r = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect(); return { left: r.left, right: r.right, width: r.width } })()`)
  const click = async (x, y) => {
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y })
    await send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 })
    await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 })
  }
  const CODES = { Enter: 13, Escape: 27, Tab: 9, ArrowDown: 40, ArrowUp: 38, Home: 36, End: 35 }
  const key = async (k, modifiers = 0) => {
    await send('Input.dispatchKeyEvent', { type: 'keyDown', key: k, code: k, modifiers, windowsVirtualKeyCode: CODES[k], ...(k === 'Enter' ? { text: '\r' } : {}) })
    await send('Input.dispatchKeyEvent', { type: 'keyUp', key: k, code: k, modifiers, windowsVirtualKeyCode: CODES[k] })
  }
  const focused = `document.activeElement.textContent.trim()`
  const inBox = `document.querySelector('.modal__box').contains(document.activeElement)`
  const plus = `document.querySelector('.add button')`
  const onPlus = `document.activeElement === ${plus}`
  const toggle = `document.querySelector('[aria-controls="sessions-panel"]')`
  const sessions = `document.querySelectorAll('.sess').length === 3`
  try {
    await send('Page.enable')
    await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false })

    // The shell: the mark, SESSIONS with "+", a row per session with its state, and a card with History, Settings and the network.
    await open('/?mock=session', sessions)
    assert.equal(await evaluate(`document.querySelector('.side__title').textContent`), 'Sessions')
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('.sess')].map((b) => [b.querySelector('.sess__name').textContent, b.querySelector('.sess__line').textContent])`), [['Ana Ribeiro', 'Connected'], ['Bruno', 'Offline'], ['Carla Méndez', 'Closed']])
    assert.equal(await evaluate(`document.querySelector('.sess__pill').textContent`), '2', 'the items that wait')
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('.dock__btn')].map((b) => b.textContent.trim())`), ['History', 'Settings'])
    assert.equal(await evaluate(`document.querySelector('.dock__net').textContent.trim()`), 'Your own network · 1 connected')
    assert.equal(await evaluate(`document.querySelector('header') === null`), true, 'no bar over the window: the sidebar has the brand')
    await evaluate(`document.querySelector('.sess').click()`)
    assert.ok(await until(visible('.session__head')))
    assert.equal(await evaluate(`document.querySelector('.sess.is-active .sess__name').textContent`), 'Ana Ribeiro', 'the active row')
    assert.equal(await evaluate(`document.querySelector('.sess.is-active').getAttribute('aria-current')`), 'page')

    // The actions of the session are all there: Close session, the folder and auto-save, choosing, Save, Decline, Cancel.
    assert.equal(await evaluate(`document.querySelector('.session__head .pill').textContent`), 'Close session')
    assert.equal(await evaluate(`document.querySelector('.session__head .status').textContent`), 'Connected')
    assert.match(await evaluate(`document.querySelector('.session__head .fingerprint').textContent`), /^Fingerprint a1b2 /)
    assert.equal(await evaluate(`document.querySelector('.session__save .btn').textContent`), 'Change')
    assert.equal(await evaluate(`document.querySelector('.session__save [role=switch]') !== null`), true)
    const buttons = await evaluate(`[...document.querySelectorAll('.session__body .btn')].map((b) => b.textContent.trim())`)
    for (const name of ['Choose a file', 'Choose a folder', 'Save', 'Decline', 'Cancel']) {
      assert.ok(buttons.includes(name), `${name} is reachable: ${buttons.join(', ')}`)
    }
    await evaluate(`document.querySelector('.session__head .pill').click()`)
    assert.ok(await until(visible('.modal__box')), 'Close session asks first')
    assert.equal(await evaluate(`document.querySelector('.modal__title').textContent`), 'Close session')
    await key('Escape')
    assert.ok(await until(`document.querySelector('.modal') === null`), 'Escape keeps the session open')
    assert.equal(await evaluate(`document.querySelector('.session__head') !== null`), true)

    // The sidebar collapses to a rail of avatars: state, aria, the width given to the main column, the names in the
    // tooltips, the choice kept across a reload, and the keyboard.
    assert.equal(await evaluate(`${toggle}.getAttribute('aria-expanded')`), 'true')
    assert.equal(await evaluate(`${toggle}.getAttribute('aria-label')`), 'Collapse the sidebar')
    const open0 = { side: (await rect('.side')).width, main: (await rect('.main')).width }
    await evaluate(`${toggle}.focus()`)
    await key('Enter')
    assert.ok(await until(`document.querySelector('.side.is-collapsed') !== null`), 'Enter on the toggle collapses it')
    assert.equal(await evaluate(`${toggle}.getAttribute('aria-expanded')`), 'false')
    assert.equal(await evaluate(`${toggle}.getAttribute('aria-label')`), 'Expand the sidebar')
    await sleep(300)
    const shut = { side: (await rect('.side')).width, main: (await rect('.main')).width }
    assert.ok(shut.side <= 80 && shut.main > open0.main + (open0.side - 80) - 2, `the main column takes the freed width: ${JSON.stringify({ open0, shut })}`)
    assert.equal(await evaluate(`getComputedStyle(document.querySelector('.sess__text')).display`), 'none', 'only the avatars show')
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('.sess')].map((b) => b.getAttribute('aria-label'))`), ['Ana Ribeiro, connected', 'Bruno, offline', 'Carla Méndez, closed'], 'the name and the state are the label')
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('.dock__btn')].map((b) => [b.getAttribute('aria-label'), b.title])`), [['History', 'History'], ['Settings', 'Settings']], 'History and Settings keep their names')
    assert.equal(await evaluate(`localStorage.getItem('kolo-share.sidebar')`), 'collapsed', 'the choice is stored')
    assert.equal(await evaluate(`${toggle}.matches(':focus-visible')`), true, 'the toggle keeps a visible focus')
    assert.equal(await evaluate(`document.querySelector('.dock__text').textContent`), 'Your own network · 1 connected', 'the network line stays for a screen reader')
    await open('/?mock=session', sessions)
    assert.ok(await evaluate(`document.querySelector('.side.is-collapsed') !== null`), 'a reload keeps it collapsed')
    await evaluate(`${toggle}.click()`)
    assert.ok(await until(`document.querySelector('.side.is-collapsed') === null`))
    assert.equal(await evaluate(`localStorage.getItem('kolo-share.sidebar')`), 'expanded')
    // Storage that throws: the window still works, expanded.
    await send('Page.addScriptToEvaluateOnNewDocument', { source: `Object.defineProperty(window, 'localStorage', { get() { throw new Error('blocked') } })` })
    await open('/?mock=session', sessions)
    assert.equal(await evaluate(`document.querySelector('.side.is-collapsed') === null`), true, 'without storage it is expanded')
    await evaluate(`${toggle}.click()`)
    assert.ok(await until(`document.querySelector('.side.is-collapsed') !== null`), 'and it still collapses')
    await send('Page.navigate', { url: 'about:blank' })

    // The menu under "+": two rows with an icon, a title and a quiet line; arrows, Home, End, Enter, Escape and a click
    // outside; the focus goes back to the button.
    await open('/?mock=session', sessions)
    assert.equal(await evaluate(`${plus}.getAttribute('aria-haspopup')`), 'menu')
    assert.equal(await evaluate(`${plus}.getAttribute('aria-label')`), 'New connection')
    assert.equal(await evaluate(`document.querySelector('.popover') === null`), true, 'closed at first')
    await evaluate(`${plus}.click()`)
    assert.ok(await until(visible('.popover')), 'the menu opens')
    assert.equal(await evaluate(`${plus}.getAttribute('aria-expanded')`), 'true')
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('[role="menuitem"]')].map((i) => [i.querySelector('.choice__title').textContent, i.querySelector('.choice__sub').textContent.length > 0, i.querySelector('.choice__icon svg') !== null])`), [['Show a code', true, true], ['Paste a code', true, true]])
    assert.match(await evaluate(focused), /^Show a code/, 'the first row has the focus')
    await key('ArrowDown')
    assert.match(await evaluate(focused), /^Paste a code/)
    await key('ArrowDown')
    assert.match(await evaluate(focused), /^Show a code/, 'the arrows wrap')
    await key('ArrowUp')
    assert.match(await evaluate(focused), /^Paste a code/)
    await key('Home')
    assert.match(await evaluate(focused), /^Show a code/)
    await key('End')
    assert.match(await evaluate(focused), /^Paste a code/)
    await key('Escape')
    assert.ok(await until(`document.querySelector('.popover') === null`), 'Escape closes the menu')
    assert.equal(await evaluate(onPlus), true, 'the focus is back on the button')
    assert.equal(await evaluate(`${plus}.getAttribute('aria-expanded')`), 'false')
    await key('ArrowDown')
    assert.ok(await until(visible('.popover')), 'ArrowDown on the button opens it too')
    await click(900, 500)
    assert.ok(await until(`document.querySelector('.popover') === null`), 'a click outside closes the menu')
    assert.equal(await evaluate(`document.querySelector('.modal') === null`), true, 'and opens nothing')

    // Show a code: the dialog, with the focus inside and kept there; Escape and a click outside close it and give the
    // focus back to "+".
    await evaluate(`${plus}.focus()`)
    await key('ArrowDown')
    assert.ok(await until(visible('.popover')))
    await key('Enter')
    assert.ok(await until(visible('.modal__box')), 'Enter on Show a code opens its dialog')
    assert.equal(await evaluate(`document.querySelector('.popover') === null`), true, 'the menu is gone')
    assert.equal(await evaluate(`document.querySelector('.modal__title').textContent`), 'Show a code')
    assert.equal(await evaluate(`document.querySelector('.modal__box').getAttribute('aria-modal')`), 'true')
    assert.equal(await evaluate(`document.querySelector('.modal__box').getAttribute('aria-labelledby') === document.querySelector('.modal__title').id`), true)
    assert.equal(await evaluate(`document.querySelector('.modal .btn').textContent`), 'Make a code', 'the existing form')
    assert.equal(await evaluate(inBox), true, 'the focus starts inside')
    for (let i = 0; i < 6; i++) {
      await key('Tab')
      assert.equal(await evaluate(inBox), true, `Tab ${i + 1} stays inside`)
    }
    await key('Tab', 8)
    assert.equal(await evaluate(inBox), true, 'Shift+Tab stays inside')
    await key('Escape')
    assert.ok(await until(`document.querySelector('.modal') === null`), 'Escape closes the dialog')
    assert.equal(await evaluate(onPlus), true, 'the focus is back on "+"')

    // Paste a code: the field has the focus; a click outside closes the dialog and gives the focus back.
    await evaluate(`${plus}.click()`)
    assert.ok(await until(visible('.popover')))
    await evaluate(`document.querySelectorAll('[role=menuitem]')[1].click()`)
    assert.ok(await until(visible('.modal__box')))
    assert.equal(await evaluate(`document.querySelector('.modal__title').textContent`), 'Paste a code')
    assert.equal(await evaluate(`document.activeElement === document.querySelector('#connect-code-field')`), true, 'the code field has the focus')
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('.modal .btn')].map((b) => b.textContent)`), ['Paste', 'Connect'], 'the existing form')
    await click(900, 40)
    assert.ok(await until(`document.querySelector('.modal') === null`), 'a click outside closes the dialog')
    assert.equal(await evaluate(onPlus), true, 'the focus is back on "+"')
    await evaluate(`${plus}.click()`)
    await evaluate(`document.querySelectorAll('[role=menuitem]')[1].click()`)
    assert.ok(await until(visible('.modal__box')))
    await evaluate(`document.querySelector('.modal .iconbtn').click()`)
    assert.ok(await until(`document.querySelector('.modal') === null`), 'the close button closes the dialog')
    assert.equal(await evaluate(onPlus), true)

    // With no session the page offers the same two ways, and the focus goes back to the button that opened the dialog.
    await open('/?mock=idle', visible('.connect'))
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('.connect__half .btn')].map((b) => b.textContent)`), ['Show a code', 'Paste a code'])
    await evaluate(`document.querySelectorAll('.connect__half .btn')[1].click()`)
    assert.ok(await until(visible('.modal__box')))
    await key('Escape')
    assert.ok(await until(`document.querySelector('.modal') === null`))
    assert.equal(await evaluate(`document.activeElement === document.querySelectorAll('.connect__half .btn')[1]`), true, 'the focus is back on the button of the page')
    assert.equal(await evaluate(`document.querySelector('.side__empty').textContent.length > 0`), true, 'no sessions says so')

    // A code that waits shows in its dialog, and closing the dialog cancels it, as leaving its page did.
    await open('/?mock=code', visible('.modal__box'))
    assert.equal(await evaluate(`document.querySelector('.modal__title').textContent`), 'Show a code')
    assert.ok(await evaluate(visible('.connect__code')), 'the code shows')
    await key('Escape')
    assert.ok(await until(`document.querySelector('.modal') === null`))
    await evaluate(`${plus}.click()`)
    await evaluate(`document.querySelectorAll('[role=menuitem]')[0].click()`)
    assert.ok(await until(`document.querySelector('.modal .btn')?.textContent === 'Make a code'`), 'the code was cancelled: the dialog offers a new one')
    assert.equal(await evaluate(`document.querySelector('.connect__code') === null`), true)

    // The network is one line and no address: the public network, a network of your own, this computer only.
    for (const [scene, text] of [['public', 'Public network · Ready'], ['local', 'This computer only · Ready'], ['idle', 'Your own network · Ready']]) {
      await open(`/?mock=${scene}`, visible('.dock__net'))
      assert.equal(await evaluate(`document.querySelector('.dock__net').textContent.trim()`), text, scene)
      assert.equal(await evaluate(`/\\d+\\.\\d+\\.\\d+\\.\\d+/.test(document.querySelector('.side').textContent)`), false, `${scene}: no address in the sidebar`)
    }

    // History and Settings still open from the card and close with the button that opened them.
    await open('/?mock=idle', visible('.dock__btn'))
    await evaluate(`document.querySelectorAll('.dock__btn')[0].click()`)
    assert.ok(await until(visible('.page--history')), 'History opens')
    assert.equal(await evaluate(`document.querySelectorAll('.dock__btn')[0].getAttribute('aria-pressed')`), 'true')
    await key('Escape')
    assert.ok(await until(`document.querySelector('.page--history') === null`), 'Escape closes History')
    await evaluate(`document.querySelectorAll('.dock__btn')[1].click()`)
    assert.ok(await until(visible('#settings-name')), 'Settings opens')

    // A narrow window: the sidebar shrinks and the page does not scroll sideways.
    await send('Emulation.setDeviceMetricsOverride', { width: 800, height: 600, deviceScaleFactor: 1, mobile: false })
    await open('/?mock=session', sessions)
    await evaluate(`document.querySelector('.sess').click()`)
    assert.ok(await until(visible('.session__head')))
    assert.ok((await rect('.side')).width <= 250, 'the sidebar is narrow at 800 px')
    assert.equal(await evaluate(`document.documentElement.scrollWidth <= innerWidth`), true, 'nothing scrolls sideways')
  } finally {
    await close()
    await server.close()
  }
})
