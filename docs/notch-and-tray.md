# Notch and tray

The **notch** is the black cut-out at the top of some Mac screens, around the camera. The example can show an incoming item and the running transfer in a small window there. The **tray** is the small icon area of the system: the menu bar on macOS, the notification area on Windows, the tray on Linux.

## Tray (macOS, Windows, Linux)

- The menu has Open Kolo Share, the status line, Copy pairing code while your code waits, the notch setting (macOS only), and Quit.
- The status line says "Waiting for your pairing code to be used", "Connected to 2 computers", "Ana wants to send you photo.jpg, 3 MiB" or the running transfer. While an item waits, the tooltip says the same, with only the sender, the name and the size, and the icon is the active one.
- Clicking the icon shows or hides the window.
- Closing the window quits the example.
- On Linux the tray is a D-Bus service: in a session without a D-Bus session bus the example runs without a tray icon and logs one line.

## Notch window (macOS only)

On a Mac with a notch, a second window made with Wails' `NewNotchWindow` shows, in this order: an incoming item waiting for an answer (oldest first), with Save, Decline and Open app; else the running receive or send with its name, percent and speed; else "Waiting for the other person to paste the code" with a Copy button; else the last result.

- It is hidden at start, opens when an item arrives (at most once per 6 seconds) or a transfer starts, stays while an item waits for an answer, and hides 6 seconds after the last transfer ends.
- Save and Decline act on the item the notch showed, by its ID. Save saves into the session's folder; without one, or when that item is gone, it opens the example.
- Cmd+Alt+Shift+S shows it again.
- On Windows and Linux the notch window does nothing.

## Turn it off

"Show transfers in the notch", in Settings or in the menu bar menu, turns the notch window off.

## What reaches the notch

The notch window gets only `share:notch` events, and only while its setting is on. No pairing code ever reaches the notch page: it gets `hasCode`, and `NotchCopyCode` copies the code in Go. An item shows only its sender, name, kind and size.

## Build tag

The example is built with public macOS APIs, so the notch page is opaque instead of transparent. Wails' private WebKit transparency needs `wails3 build -tags private_mac_apis`, which this repository does not use.
