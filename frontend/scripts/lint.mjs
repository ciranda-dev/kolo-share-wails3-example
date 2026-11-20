// Checks the sources without any dependency: no em dash anywhere, every page carries a content security
// policy and nothing inline that the policy would block, and every copy key that the code asks for exists,
// and every key that exists is asked for.
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { messages } from '../src/i18n.js'

const root = join(fileURLToPath(import.meta.url), '..', '..')
const files = []
const walk = (dir) => {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) {
      walk(path)
    } else if (/\.(jsx?|mjs|ts|css|html)$/.test(name)) {
      files.push(path)
    }
  }
}
for (const dir of ['src', 'dev', 'notch', 'test', 'scripts']) {
  walk(join(root, dir))
}
files.push(join(root, 'index.html'))

const problems = []
const used = new Set()
for (const file of files) {
  const text = readFileSync(file, 'utf8')
  const name = relative(root, file)
  text.split('\n').forEach((line, i) => {
    if (line.includes(String.fromCharCode(0x2014))) {
      problems.push(`${name}:${i + 1}: em dash`)
    }
  })
  if (name.endsWith('.html') && !/<meta http-equiv="Content-Security-Policy" content="default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self';/.test(text)) {
    problems.push(`${name}: no content security policy`)
  }
  if (name.endsWith('.html') && /<script(?![^>]*\bsrc=)|<style|\sstyle=|\son\w+=/.test(text)) {
    problems.push(`${name}: inline script, style or handler (a content security policy would block it)`)
  }
  if (!name.startsWith('src/') || name.startsWith('src/messages/')) {
    continue
  }
  // Any quoted dotted word counts as a use: keys are also picked with a condition inside t(...).
  for (const m of text.matchAll(/'([a-z][\w]*(?:\.[\w]+)+)'/g)) {
    used.add(m[1])
  }
  for (const m of text.matchAll(/\bt\(\s*'(\w+)'/g)) {
    used.add(m[1])
  }
  // Keys built from a state, a result or a reason: `state.${x}` uses every key under that prefix.
  for (const m of text.matchAll(/`([a-z][\w]*\.[\w.]*)\$\{/g)) {
    used.add(`${m[1]}*`)
  }
}

const keys = Object.keys(messages.en)
const has = (key) => keys.includes(key) || keys.includes(`${key}.one`)
for (const key of used) {
  if (!key.endsWith('*') && !has(key)) {
    problems.push(`copy key '${key}' is used but not in src/messages/en.js`)
  }
}
const prefixes = [...used].filter((k) => k.endsWith('*')).map((k) => k.slice(0, -1))
for (const key of keys) {
  const base = key.replace(/\.(one|other)$/, '')
  if (!used.has(key) && !used.has(base) && !prefixes.some((p) => key.startsWith(p))) {
    problems.push(`copy key '${key}' is never used`)
  }
}

if (problems.length > 0) {
  console.error(problems.join('\n'))
  process.exit(1)
}
console.log(`lint: ${files.length} files, ${keys.length} copy keys, no problems`)
