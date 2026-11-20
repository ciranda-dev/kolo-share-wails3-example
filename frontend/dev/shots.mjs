// Screenshots of every state against the dev server and the mock, through Chrome's DevTools protocol.
// Usage: node dev/shots.mjs <out-dir> [base-url] [filter]. Needs Google Chrome; nothing to install.
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { openChrome, sleep } from './chrome.mjs'

const out = process.argv[2]
const base = process.argv[3] || 'http://127.0.0.1:9245'
const filter = process.argv[4] || ''
if (!out) {
  console.error('usage: node dev/shots.mjs <out-dir> [base-url] [filter]')
  process.exit(2)
}
// name: width, height, pixel ratio. The two wide sizes are where the layout has to prove it fills the window.
const SIZES = { default: [1000, 618, 2], wide: [1440, 900, 1], huge: [2000, 1240, 1], narrow: [800, 600, 1], min: [640, 520, 2] }
const click = (selector) => `document.querySelector(${JSON.stringify(selector)}).click()`
const setValue = (selector, text) => `(() => { const el = document.querySelector(${JSON.stringify(selector)}); Object.getOwnPropertyDescriptor(el.constructor.prototype, 'value').set.call(el, ${JSON.stringify(text)}); el.dispatchEvent(new Event('input', { bubbles: true })) })()`
const typeCode = (text) => setValue('#connect-code-field', text)
const connect = click('.connect__paste button[type=submit]')
const paste = click('.connect__paste .connect__actions .btn--ghost')
// The sidebar's rows are buttons, so a click opens a session at every size.
const openSession = (n = 0) => `document.querySelectorAll('.sess')[${n}].click()`
const historyButton = `document.querySelectorAll('.dock__btn')[0].click()`
const settingsButton = `document.querySelectorAll('.dock__btn')[1].click()`
const addButton = click('.add button')
const menuItem = (n) => `document.querySelectorAll('[role=menuitem]')[${n}].click()`
const openShow = [addButton, menuItem(0)]
const openJoin = [addButton, menuItem(1)]
const collapse = click('.side__brand .iconbtn')
const drag = `document.querySelector('[data-file-drop-target]').classList.add('file-drop-target-active')`
const drop = `window.mockEmit('share:dropped', ['/home/ana/Pictures/Tour photos'])`

// No text, tooltip or label on screen may hold a character without a visible shape.
const HIDDEN = `(() => { const bad = /[\\p{Cc}\\p{Cf}\\p{Zl}\\p{Zp}\\p{Co}\\p{Cs}]/u; const texts = [document.body.innerText.replace(/[\\n\\t]/g, ' ')]; for (const el of document.querySelectorAll('[title], [aria-label]')) { texts.push((el.getAttribute('title') ?? '').replace(/\\n/g, ' '), el.getAttribute('aria-label') ?? '') } return texts.some((x) => bad.test(x)) ? 'an invisible or direction-changing character is on screen' : '' })()`

// The layout rule of the window: content sits on the page and reaches its edges. Two checks, both on
// what is drawn (text, fields, buttons, the ring, anything with a border), not on empty containers.
// 1. No bordered, rounded box of page size (a panel, not a button) sits in the middle of its region with a band on each side.
// 2. From 1440px up, what is drawn spans at least 90% of the window and 90% of the main region.
const LAYOUT = `(() => {
  if (document.querySelector('.modal')) { return '' }
  const root = document.querySelector('.body')
  const main = document.querySelector('.main')
  const REGIONS = '.side, .connect__half, .connect__stage, .connect__foot, .session__head, .session__save, .session__body, .session__lists, .list, .session__history, .settings__region, .page__body, .bar, .band, .notice, .main, .body'
  const problems = []
  let left = Infinity, right = -Infinity, mainLeft = Infinity, mainRight = -Infinity
  for (const el of root.querySelectorAll('*')) {
    const r = el.getBoundingClientRect()
    const s = getComputedStyle(el)
    // Hidden things and the tooltip of a cut name are not part of the layout.
    if (r.width === 0 || r.height === 0 || s.visibility === 'hidden' || s.position === 'absolute' || el.closest('.sr-only')) { continue }
    const sides = ['Top', 'Right', 'Bottom', 'Left'].filter((side) => parseFloat(s['border' + side + 'Width']) > 0 && s['border' + side + 'Style'] !== 'none').length
    const text = [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim() !== '')
    if (sides === 4 && parseFloat(s.borderTopLeftRadius) > 0 && el.localName !== 'button') {
      const host = el.parentElement.closest(REGIONS).getBoundingClientRect()
      if (r.width >= host.width * 0.4 && r.left - host.left > 48 && host.right - r.right > 48) {
        problems.push('a bordered box floats in its region with bands of ' + Math.round(r.left - host.left) + 'px and ' + Math.round(host.right - r.right) + 'px: ' + el.className)
      }
    }
    if (sides === 4 || text || ['svg', 'input', 'select', 'textarea'].includes(el.localName)) {
      // Text is measured by its own box, not by the block that holds it.
      let box = r
      if (text && sides < 4) {
        const range = document.createRange()
        range.selectNodeContents(el)
        box = range.getBoundingClientRect()
      }
      left = Math.min(left, box.left)
      right = Math.max(right, box.right)
      if (main.contains(el)) {
        mainLeft = Math.min(mainLeft, box.left)
        mainRight = Math.max(mainRight, box.right)
      }
    }
  }
  if (window.innerWidth >= 1440) {
    const all = (right - left) / window.innerWidth
    const inner = (mainRight - mainLeft) / main.getBoundingClientRect().width
    if (all < 0.9) { problems.push('content spans ' + Math.round(all * 100) + '% of the window, under 90%') }
    if (inner < 0.9) { problems.push('content spans ' + Math.round(inner * 100) + '% of the main region, under 90%') }
  }
  return problems.join('; ')
})()`

/** name, mock scene, steps to run after load, language. */
const SHOTS = [
  ['connect-idle', 'idle', []],
  ['connect-code-starting', 'code-starting', []],
  ['connect-code', 'code', []],
  ['connect-code-copied', 'code', [click('.connect__row .btn')]],
  ['connect-code-expired', 'code-expired', openShow],
  ['connect-show-idle', 'idle', openShow],
  ['connect-join-idle', 'idle', openJoin],
  ['connect-pasted', 'idle', [...openJoin, paste]],
  ['connect-bad-code', 'idle', [...openJoin, typeCode('hello'), connect]],
  ['connect-old-code', 'idle', [...openJoin, typeCode('kolo-share:fwAAAeWwzXkT8Yqjotld3Q9sVb2mK7pLx4RtYh1uGc8nEoZ5aJ6wDf0iBvMXyUHCq'), connect]],
  ['connect-empty-code', 'idle', [...openJoin, connect]],
  ['connect-looking', 'looking', []],
  ['connect-joining', 'joining', []],
  ['connect-pair-failed', 'pair-failed', openJoin],
  ['connect-drop-first', 'idle', [drop]],
  ['session', 'session', [openSession()]],
  ['session-drag', 'session', [openSession(), drag]],
  ['session-long-name-focus', 'session', [openSession(), `document.querySelectorAll('.row .name.is-cut')[0].focus()`]],
  ['session-empty', 'session-empty', [openSession()]],
  ['session-empty-drag', 'session-empty', [openSession(), drag]],
  ['session-history', 'session-history', [openSession()]],
  ['session-reconnecting', 'reconnecting', [openSession()]],
  ['session-ended', 'ended', [openSession()]],
  ['session-ended-empty', 'ended-empty', [openSession()]],
  ['session-notice', 'notice', [openSession()]],
  ['session-notice-limit', 'notice-limit', [openSession()]],
  ['session-autosave', 'autosave', [openSession()]],
  ['session-no-folder', 'no-folder', [openSession(), click('.row__actions .btn')]],
  ['session-close-confirm', 'session', [openSession(), click('.session__head .pill')], 'en', 'default'],
  ['session-hostile', 'hostile', [openSession()]],
  ['session-reconnecting-rail', 'session', [openSession(1)]],
  ['rail-many', 'many', [openSession(5)]],
  ['sidebar-collapsed', 'session', [openSession(), collapse]],
  ['sidebar-collapsed-many', 'many', [openSession(0), collapse]],
  ['sidebar-add-menu', 'session', [openSession(), addButton]],
  ['sidebar-empty', 'idle', []],
  ['own-network', 'own', [openSession()]],
  ['public-network', 'public-session', [openSession()]],
  ['local-network', 'local-session', [openSession()]],
  ['history', 'idle', [historyButton]],
  ['history-empty', 'empty', [historyButton]],
  ['settings', 'settings', [settingsButton]],
  ['settings-name-error', 'idle', [settingsButton, setValue('#settings-name', 'A name that is far longer than the forty-eight characters a name may have'), click('.you button[type=submit]')]],
  ['settings-local', 'local', [settingsButton]],
  ['network-local', 'local', []],
  ['network-starting', 'starting', []],
  ['network-public', 'public', []],
  ['settings-public', 'public', [settingsButton]],
  ['network-public-failed', 'public-failed', []],
  ['network-failed', 'net-failed', []],
  ['es-connect-idle', 'idle', [], 'es'],
  ['es-sidebar-add-menu', 'session', [openSession(), addButton], 'es'],
  ['es-connect-code', 'code', [], 'es'],
  ['es-connect-pair-failed', 'pair-failed', [], 'es'],
  ['es-session', 'session', [openSession()], 'es'],
  ['es-session-reconnecting', 'reconnecting', [openSession()], 'es'],
  ['es-session-ended', 'ended', [openSession()], 'es'],
  ['es-settings', 'settings', [settingsButton], 'es'],
  ['es-history', 'idle', [historyButton], 'es'],
  ['pt-connect-idle', 'idle', [], 'pt'],
  ['pt-sidebar-add-menu', 'session', [openSession(), addButton], 'pt'],
  ['pt-connect-looking', 'looking', [], 'pt'],
  ['pt-connect-code', 'code', [], 'pt'],
  ['pt-session', 'session', [openSession()], 'pt'],
  ['pt-session-empty', 'session-empty', [openSession()], 'pt'],
  ['pt-session-notice', 'notice', [openSession()], 'pt'],
  ['pt-settings', 'settings', [settingsButton], 'pt'],
  ['pt-network-public-failed', 'public-failed', [], 'pt'],
]

const { send, evaluate, close } = await openChrome(['--hide-scrollbars', '--force-device-scale-factor=2'])
await send('Page.enable')
await send('Emulation.setFocusEmulationEnabled', { enabled: true })
let count = 0
const problems = []
try {
  for (const [name, scene, steps, lang = 'en', only] of SHOTS) {
    if (filter && !name.includes(filter)) {
      continue
    }
    for (const theme of ['dark', 'light']) {
      for (const [sizeName, [width, height, scale]] of Object.entries(SIZES)) {
        if (only && only !== sizeName) {
          continue
        }
        await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: scale, mobile: false })
        await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: theme }, { name: 'prefers-reduced-motion', value: 'reduce' }] })
        await send('Page.navigate', { url: `${base}/?mock=${scene}` })
        await sleep(350)
        await evaluate(`localStorage.removeItem('kolo-share.sidebar'); localStorage.setItem('kolo-share:prefs', JSON.stringify({ lang: ${JSON.stringify(lang)} }))`)
        await send('Page.navigate', { url: `${base}/?mock=${scene}&v=${count}` })
        for (let i = 0; i < 40 && !(await evaluate(`!!document.querySelector('.app')`)); i++) {
          await sleep(100)
        }
        await sleep(250)
        for (const step of steps) {
          await evaluate(step)
          await sleep(200)
        }
        // Anything wider than its box is a layout bug: report it next to the screenshot.
        const wide = await evaluate(`document.documentElement.scrollWidth > window.innerWidth ? 'page scrolls sideways' : ''`)
        if (wide) {
          problems.push(`${name} ${theme} ${sizeName}: ${wide}`)
        }
        const unseen = await evaluate(HIDDEN)
        if (unseen) {
          problems.push(`${name} ${theme} ${sizeName}: ${unseen}`)
        }
        const layout = await evaluate(LAYOUT)
        if (layout) {
          problems.push(`${name} ${theme} ${sizeName}: ${layout}`)
        }
        const shot = await send('Page.captureScreenshot', { format: 'png' })
        const dir = join(out, theme, sizeName)
        mkdirSync(dir, { recursive: true })
        writeFileSync(join(dir, `${name}.png`), Buffer.from(shot.data, 'base64'))
        count++
      }
    }
  }
  // The notch page: dark only, at the 660 x 92 content area of the notch window.
  const NOTCH = ['n-offer', 'n-offer-long', 'n-code', 'n-preparing', 'n-prepared-by-peer', 'n-waiting', 'n-offered', 'n-sending', 'n-queued', 'n-receiving', 'n-verifying', 'n-done', 'n-delivered', 'n-declined', 'n-withdrawn', 'n-failed', 'n-hostile']
  for (const lang of ['en', 'es', 'pt']) {
    for (const scene of NOTCH) {
      const name = `${lang === 'en' ? '' : `${lang}-`}${scene.slice(2)}`
      if (filter && !`notch-${name}`.includes(filter)) {
        continue
      }
      await send('Emulation.setDeviceMetricsOverride', { width: 660, height: 92, deviceScaleFactor: 2, mobile: false })
      await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }, { name: 'prefers-reduced-motion', value: 'reduce' }] })
      await send('Page.navigate', { url: `${base}/notch/?mock=${scene}` })
      await sleep(350)
      await evaluate(`localStorage.setItem('kolo-share:prefs', JSON.stringify({ lang: ${JSON.stringify(lang)} }))`)
      await send('Page.navigate', { url: `${base}/notch/?mock=${scene}&v=${count}` })
      for (let i = 0; i < 40 && !(await evaluate(`!!document.querySelector('.notch__text')`)); i++) {
        await sleep(100)
      }
      await sleep(250)
      const wide = await evaluate(`document.documentElement.scrollWidth > window.innerWidth || document.documentElement.scrollHeight > window.innerHeight ? 'content is larger than the notch area' : ''`)
      if (wide) {
        problems.push(`notch ${name}: ${wide}`)
      }
      const unseen = await evaluate(HIDDEN)
      if (unseen) {
        problems.push(`notch ${name}: ${unseen}`)
      }
      const shot = await send('Page.captureScreenshot', { format: 'png' })
      mkdirSync(join(out, 'notch'), { recursive: true })
      writeFileSync(join(out, 'notch', `${name}.png`), Buffer.from(shot.data, 'base64'))
      count++
    }
  }
} finally {
  await close()
}
console.log(`${count} screenshots in ${out}`)
if (problems.length > 0) {
  console.log(problems.join('\n'))
  process.exitCode = 1
}
