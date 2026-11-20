# Go API and events

The window is a web page that calls Go services through Wails bindings and listens to events.

## ShareService methods

- `Start`, `Info`, `Settings`, `Reconnect`, `SetDisplayName(name)`
- Pairing: `CreatePairing`, `JoinPairing(code)`, `CancelPairing(id)`, `Pairing`
- Sessions: `Sessions`, `CloseSession(id)` (a second call dismisses an ended session), `SetSessionFolder(id, folder)`, `SetAutoSave(id, on)` (it saves at most half the free space the folder has when it is turned on, then turns itself off with the notice `autosave-limit`)
- Items: `Send(sessionId, path)`, `SaveOffer(id)`, `DeclineOffer(id)`, `CancelTransfer(id)`
- `Reveal(path)`: only a path a receive saved
- `Transfers`, `History`, `ClearHistory`
- `PickFile`, `PickFolder`, `PickSaveFolder`
- For the notch and the tray: `Notch`, `NotchCopyCode`, `NotchSaveOffer(id)`, `NotchDeclineOffer(id)`, `NotchShow`, `NotchHide`, `NotchOpenApp`, `SetShowInNotch(on)`
- `ReportError(message, stack)`: both windows send an error they could not handle; it is written to the terminal as a `frontend error:` line, cleaned and cut to 8192 characters

The network has no method to change it: it comes from flags or environment variables only (see [configuration](configuration.md)). `Info().network` and `Settings` say which one runs in `mode`: `public`, `own` (`-bootstrap` or `-first`) or `local` (`-local`). `entryNodes` of `Settings` lists the entry nodes of your own network (`own` mode; it is empty in `public` mode, where the window does not list the public entry nodes, and in `local` mode) and `entryNodesFrom` is `public`, `flag`, `env`, `local` or empty (only `-first`); `localNode` is set only in `local` mode and `firstNode` in the window that runs an entry node. The network state is `starting`, `ready` or `failed`; a public network that cannot be reached is `failed` with the reason `public-unreachable`. `Reconnect` refuses while a session or a pairing is open.

## Events

Events go to one window each.

| Event | Window | What it carries |
|---|---|---|
| `share:pairing` | main | the pairing: `starting`, `waiting` (with the code) for the one who shows it; `looking`, `joining` for the one who pastes it; then `connected`, `expired`, `cancelled` or `failed` |
| `share:sessions` | main | every session: `connecting`, `connected`, `reconnecting`, `ended` with its reason (`closed`, `peer-closed`, `replaced`, `app-closed`, `peer-protocol`) |
| `share:transfer` | main | an item and its state: `preparing`, `offered`, `sending`, then `delivered`, `declined`, `cancelled`, `stopped` or `failed` for a send; `preparing` (the other side announced it and is still storing it; there is nothing to answer yet), `offered`, `queued`, `receiving`, `verifying`, then `done`, `declined`, `cancelled`, `withdrawn`, `stopped` or `failed` for a receive. The same `id` goes from `preparing` to `offered`. In `preparing` on a send, `bytesDone` and `bytesTotal` count bytes read and stored on this computer; in `sending`, bytes the peer downloaded; on a receive in `preparing`, what the sender said (`bytesTotal` 0 until it knows the size). `stalled` is true while a `sending` or `receiving` item moved no byte for 10 seconds, and `bytesPerSecond` is then 0 |
| `share:history` | main | the history; each item has the peer's name, short key and `peerId` (the whole key) |
| `share:network` | main | the state of the network, with each relay's state in `relays` |
| `share:dropped` | main | the paths dropped on the window |
| `share:notch` | notch | what the notch shows, while its setting is on |

No pairing code ever reaches the notch page: it gets `hasCode`, and `NotchCopyCode` copies the code in Go. `Pairing` returns the code to the main window only. An incoming offer reaches the notch as its sender, name, kind and size, with `hasOffer`; `NotchSaveOffer(id)` and `NotchDeclineOffer(id)` take the ID of the offer the notch page showed, so a click never acts on an item that took its place.

## Errors

Every error the window gets is a `*ShareError`:

- `Reason`: a stable ID for each sentence (`pair-used`, `pair-not-found`, `offer-mismatch`, `peer-protocol`, `corrupt`, `too-large`, `unsafe-name`, `lost`, and so on, all in `errors.go`);
- `Kind`: its group;
- `Detail` and `Cause`: the values in the sentence (for `no-space-prepare`, `no-space-download`, `no-space-save` and `no-space-both`, the room needed and the room free, as text such as "108 GB"; they are `kind` `disk` and match `ErrNoSpace`);
- `Message`: the sentence in English.

A window in another language translates by `Reason`, and `errors.Is` works on the cause.

Every list a method returns or an event carries is a JSON list, `[]` when empty, never `null`. A window that hits an error no code handled shows it on the whole window with a Copy button (`frontend/src/Crash.jsx`) instead of a blank window, and sends it to `ReportError`.
