import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const policy = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'"

for (const page of ['../index.html', '../notch/index.html']) {
  test(`${page} carries the content security policy and nothing inline`, () => {
    const html = readFileSync(new URL(page, import.meta.url), 'utf8')
    assert.ok(html.includes(`<meta http-equiv="Content-Security-Policy" content="${policy}" />`))
    assert.equal(/<script(?![^>]*\bsrc=)|<style|\sstyle=|\son\w+=/.test(html), false)
  })
}

test('the dev server serves the pages without the policy, the build keeps it', async () => {
  const { loadConfigFromFile } = await import('vite')
  const { config } = await loadConfigFromFile({ command: 'serve', mode: 'development' }, new URL('../vite.config.js', import.meta.url).pathname)
  const dev = config.plugins.find((p) => p?.name === 'dev-without-policy')
  assert.equal(dev.apply, 'serve')
  const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
  const served = dev.transformIndexHtml.handler(html)
  assert.equal(served.includes('Content-Security-Policy'), false)
  assert.ok(served.includes('<title>Kolo Share</title>'))
})
