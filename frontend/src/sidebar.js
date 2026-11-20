/** The key under which the window remembers that the sidebar is collapsed. */
export const SIDEBAR_KEY = 'kolo-share.sidebar'

const store = () => {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

/** Whether the sidebar was left collapsed. Storage that is missing or throws means "expanded". */
export function readCollapsed(storage = store()) {
  try {
    return storage?.getItem(SIDEBAR_KEY) === 'collapsed'
  } catch {
    return false
  }
}

/** Remembers the choice; the window works the same when storage is missing or throws. */
export function writeCollapsed(collapsed, storage = store()) {
  try {
    storage?.setItem(SIDEBAR_KEY, collapsed ? 'collapsed' : 'expanded')
  } catch {
    // the choice is kept for this run only
  }
}
