import test from 'node:test'
import assert from 'node:assert/strict'
import { list } from '../src/lists.js'

test('list reads null, a missing value or a non-list as an empty list', () => {
  assert.deepEqual(list(null), [])
  assert.deepEqual(list(undefined), [])
  assert.deepEqual(list({}), [])
  const sessions = [{ id: '1' }]
  assert.equal(list(sessions), sessions)
})
