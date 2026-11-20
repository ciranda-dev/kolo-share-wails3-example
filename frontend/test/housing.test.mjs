import test from 'node:test'
import assert from 'node:assert/strict'
import { housingOf, showHousing } from '../src/housing.js'

test('anything but two counts of pixels is no housing', () => {
  for (const search of ['', '?top=32', '?housing=200', '?top=0&housing=200', '?top=-3&housing=200', '?top=3.5&housing=200', '?top=x&housing=200', '?top=32&housing=99999', '?top=1e2&housing=200']) {
    assert.deepEqual(housingOf(search), { top: 0, width: 0 }, search)
  }
  assert.deepEqual(housingOf('?mock=n-offer&top=38&housing=185'), { top: 38, width: 185 })
})

test('the styles get the housing as CSS variables', () => {
  const set = {}
  showHousing({ style: { setProperty: (k, v) => { set[k] = v } } }, { top: 32, width: 200 })
  assert.deepEqual(set, { '--housing-top': '32px', '--housing-width': '200px' })
})
