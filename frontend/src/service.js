/**
 * The one door to the Go service. In the app it is the generated Wails bindings; a dev server opened
 * with ?mock in the URL gets dev/mock.ts instead, which a production build never includes.
 */
const mocked = import.meta.env.DEV && new URLSearchParams(window.location.search).has('mock')
const backend = mocked ? import('../dev/mock.ts') : import('./wails.js')

/** Calls a method of ShareService by name. */
export async function call(name, ...args) {
  const api = await backend
  return api[name](...args)
}

/** Listens to a service event; the callback gets the payload. Returns the function that stops it. */
export function on(name, callback) {
  let off = null
  let stopped = false
  backend.then((api) => {
    if (!stopped) {
      off = api.Events.On(name, (event) => callback(event.data))
    }
  })
  return () => {
    stopped = true
    if (off) {
      off()
    }
  }
}

/** Puts text on the clipboard. */
export async function copyText(text) {
  const api = await backend
  return api.Clipboard.SetText(text)
}

/** Reads the text on the clipboard. */
export async function pasteText() {
  const api = await backend
  return api.Clipboard.Text()
}
