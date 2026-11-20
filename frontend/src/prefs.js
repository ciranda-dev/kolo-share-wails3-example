const KEY = 'kolo-share:prefs'

/** What this window remembers on this computer: language, theme and the last folder saved into. */
export function loadPrefs() {
  try {
    const value = JSON.parse(window.localStorage.getItem(KEY) ?? '{}')
    return value && typeof value === 'object' ? value : {}
  } catch {
    return {}
  }
}

/** Saves the preferences; a webview without storage just forgets them. */
export function savePrefs(prefs) {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(prefs))
  } catch {
    // nothing to do: the preferences last for this run only
  }
}
