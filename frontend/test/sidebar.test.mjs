import test from 'node:test'
import assert from 'node:assert/strict'
import { SIDEBAR_KEY, readCollapsed, writeCollapsed } from '../src/sidebar.js'
import { menuStep, trapStep } from '../src/focus.js'

const memory = () => {
  const data = new Map()
  return { getItem: (k) => data.get(k) ?? null, setItem: (k, v) => data.set(k, String(v)), data }
}
const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }

test('the sidebar is expanded until it was collapsed, and the choice is remembered', () => {
  const store = memory()
  assert.equal(readCollapsed(store), false)
  writeCollapsed(true, store)
  assert.equal(store.data.get(SIDEBAR_KEY), 'collapsed')
  assert.equal(readCollapsed(store), true)
  writeCollapsed(false, store)
  assert.equal(readCollapsed(store), false)
})

test('without storage, or with storage that throws, the sidebar works and is expanded', () => {
  assert.equal(readCollapsed(null), false)
  assert.equal(readCollapsed(broken), false)
  assert.doesNotThrow(() => writeCollapsed(true, broken))
  assert.doesNotThrow(() => writeCollapsed(true, null))
  assert.equal(readCollapsed(), false, 'no localStorage in this process')
})

test('Tab stays inside a dialog: it wraps from the last element to the first and back', () => {
  assert.equal(trapStep(2, 3, false), 0)
  assert.equal(trapStep(0, 3, true), 2)
  assert.equal(trapStep(1, 3, false), -1, 'inside, the browser moves it')
  assert.equal(trapStep(-1, 3, false), 0, 'from outside it comes in')
  assert.equal(trapStep(-1, 3, true), 2)
  assert.equal(trapStep(0, 0, false), -1)
})

test('the arrow keys of the menu wrap, Home and End jump, other keys do nothing', () => {
  assert.equal(menuStep(0, 2, 'ArrowDown'), 1)
  assert.equal(menuStep(1, 2, 'ArrowDown'), 0)
  assert.equal(menuStep(0, 2, 'ArrowUp'), 1)
  assert.equal(menuStep(-1, 2, 'ArrowDown'), 0)
  assert.equal(menuStep(1, 2, 'Home'), 0)
  assert.equal(menuStep(0, 2, 'End'), 1)
  assert.equal(menuStep(0, 2, 'a'), -1)
  assert.equal(menuStep(0, 0, 'ArrowDown'), -1)
})

test('the color of a session follows its id: the same id gets the same color, and the initial is a capital', async () => {
  const { avatarColor, initial, AVATAR_COLORS } = await import('../src/avatars.js')
  assert.equal(avatarColor('a1b2c3d4'), avatarColor('a1b2c3d4'))
  assert.ok(AVATAR_COLORS.includes(avatarColor('7f3e9c21')))
  assert.equal(initial('  ana'), 'A')
  assert.equal(initial(''), '?')
})
