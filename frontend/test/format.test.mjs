import test from 'node:test'
import assert from 'node:assert/strict'
import { errorText, formatBinary, formatBytes, formatRate, formatWhen, fraction, middleCut, percent } from '../src/format.js'

test('formatBytes uses decimal units and short numbers', () => {
  assert.equal(formatBytes(0), '0 B')
  assert.equal(formatBytes(999), '999 B')
  assert.equal(formatBytes(1000), '1.0 KB')
  assert.equal(formatBytes(4_200_000), '4.2 MB')
  assert.equal(formatBytes(12_700_000_000), '12.7 GB')
  assert.equal(formatBytes(1_840_000_000), '1.84 GB')
  assert.equal(formatBytes(512_400_000), '512 MB')
  assert.equal(formatBytes(2.4e15), '2.4 PB')
  assert.equal(formatBytes(9.9e20), '990,000 PB')
})

test('formatBytes never rounds up into four digits', () => {
  assert.equal(formatBytes(999_600), '1.0 MB')
  assert.equal(formatBytes(999_400), '999 KB')
})

test('formatBytes survives bad input and follows the language', () => {
  assert.equal(formatBytes(-5), '0 B')
  assert.equal(formatBytes(NaN), '0 B')
  assert.equal(formatBytes(undefined), '0 B')
  assert.equal(formatBytes(12_700_000_000, 'pt-BR'), '12,7 GB')
  assert.equal(formatBytes(12_700_000_000, 'es-ES'), '12,7 GB')
})

test('formatRate and formatBinary', () => {
  assert.equal(formatRate(8_400_000), '8.4 MB/s')
  assert.equal(formatRate(0), '0 B/s')
  assert.equal(formatBinary(32 * 1024 ** 3), '32 GiB')
  assert.equal(formatBinary(0), '0 B')
})

test('fraction and percent stay in range', () => {
  assert.equal(fraction(1, 0), 0)
  assert.equal(fraction(-1, 10), 0)
  assert.equal(fraction(20, 10), 1)
  assert.equal(fraction(5, 10), 0.5)
  assert.equal(percent(999, 1000), 99)
  assert.equal(percent(1000, 1000), 100)
  assert.equal(percent(0, 0), 0)
})

test('middleCut keeps the ending and counts whole characters', () => {
  assert.equal(middleCut('short.txt', 20), 'short.txt')
  const cut = middleCut('a-very-long-file-name-that-goes-on-and-on.tar.zst', 24)
  assert.equal(Array.from(cut).length, 24)
  assert.ok(cut.endsWith('tar.zst'))
  assert.ok(cut.includes('\u2026'))
  assert.equal(Array.from(middleCut('\u{1F600}'.repeat(50), 10)).length, 10)
  assert.equal(middleCut(undefined, 10), '')
})

test('formatWhen shows the time today and the day before that', () => {
  const now = new Date(2026, 10, 16, 15, 0)
  assert.match(formatWhen(new Date(2026, 10, 16, 14, 2).getTime(), 'en', now), /2:02/)
  assert.match(formatWhen(new Date(2026, 10, 9, 14, 2).getTime(), 'en', now), /Nov 9/)
  assert.equal(formatWhen('nonsense', 'en', now), '')
})

test('errorText reads what a rejected call threw', () => {
  assert.equal(errorText(new Error('Choose a folder to save into.')), 'Choose a folder to save into.')
  assert.equal(errorText('plain'), 'plain')
  assert.equal(errorText({ message: 'from an object' }), 'from an object')
})
