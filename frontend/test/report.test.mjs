import test from 'node:test'
import assert from 'node:assert/strict'
import { MAX_REPORT, reportParts, reportText } from '../src/report.js'

test('a report is the name and message, then the stack and the component stack, the head said once', () => {
  const error = new TypeError('x is null')
  const parts = reportParts(error, '\n    at Rail\n    at App')
  assert.equal(parts.message, 'TypeError: x is null')
  assert.ok(!parts.stack.includes('TypeError: x is null'))
  assert.match(parts.stack, /at Rail\n {4}at App$/)
  assert.match(reportText(parts), /^TypeError: x is null\n/)
  assert.deepEqual(reportParts('plain'), { message: 'Error: plain', stack: '' })
  assert.equal(reportText(reportParts(null)), 'Error: null')
})

test('a report is cut to MAX_REPORT characters', () => {
  const parts = reportParts(new Error('é'.repeat(MAX_REPORT * 2)))
  assert.equal(Array.from(parts.message).length, MAX_REPORT)
  assert.equal(Array.from(reportText(parts)).length, MAX_REPORT)
})
