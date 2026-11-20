# Security

## What the pairing code protects, and what it does not

- While it waits, the pairing code is the only thing someone needs to connect to you. The first person who pastes it becomes your session peer; then it stops working. Send it over a channel you trust, and press Cancel on a code you think leaked. It also ends after `-pair-ttl` (1 hour by default, at most 30 days), when you close its dialog and when the example quits, and it is never written to disk, the history, a log line, the tray tooltip or the notch page.
- It holds no address. It holds a fingerprint of a key made for this one pairing and a 256-bit secret. The network sees only a topic derived one way from it, and the request travels sealed with HPKE inside a kolo-secure connection (kolo-invite README, "Security").
- After pairing, both examples accept only each other's example key for the session. kolo-secure checks that key on every connection: kolo-transport's TLS 1.3, plus a kolo-id signature over the connection's TLS exporter value. kolo-blobs encrypts the file content under keys derived from a random key per file.
- The entry nodes (DHT servers) that store the session topic see both example keys and both IPv4 addresses, and when the session is connected. They cannot read the topic's secret or the files. The same holds for the pairing topic while the code waits. On the public network these are the servers Ciranda runs; with `-bootstrap` they are yours, and with `-local` the entry node is on this computer.
- A relay (`-relay`) carries a connection only when the two examples cannot connect directly. The connection stays kolo-secure from one example to the other, with the same key checks as a direct one, so the relay copies bytes it cannot read: not the files, their names or their keys. It sees each example's key and address, and who connects to whom, when and how much. A test checks it: the bytes a relay passes for a session never hold the file's text or a key of its entries.
- The other person sees the names, sizes and number of files of what you offer, and your display name. While a large item is still being prepared they already see its name, its size (for a file) and how far the preparation got; they can pull nothing before it is offered and they have said Save.
- Pasting a code proves nothing about who the person is. Each session shows the other side's fingerprint, and Settings shows yours: 128 bits of the key, in 8 groups of 4 characters. Compare all of it over a channel you trust (a call, in person). Faking a matching fingerprint needs a second preimage of 128 bits, about 2^128 work, the target kolo-invite sets for the fingerprint in its codes. The 8-character short key next to a name is only a label: it is too short to compare, since a key that matches it can be made ahead of time.
- The example claims nothing more than this.

## What the other person can do, and the answer

| They try | The example |
|---|---|
| a flood of offers | at most 16 waiting per session and 60 new ones per minute; the rest are declined without a popup and kept out of the history; offers open the notch at most once per 6 seconds, and the window keeps at most 50 finished items per session |
| offers without end before SYNCED | more than 16 ends the session as a protocol error |
| a flood of announcements of items being prepared | at most 16 cards per session, 60 new ones per minute, updates counted per minute too, nothing for an item over `-max-download`; a field out of range ends the session as a protocol error; a card that hears nothing for 2 minutes ends |
| a connection that drops or stalls in the middle of a download | what was verified stays in the private store and the pull continues from it; a limit or an integrity error still ends the item, and so does `-resume-for` without a byte |
| an offer withdrawn just before you click Save in the notch | Save acts on the item the notch showed, by its ID: if it is gone, nothing is saved and the example opens |
| huge sizes or many files in an offer | declined at once over `-max-download` or `-max-files`; the real tree is bounded by the signed heads before a byte is pulled |
| an offer that lies about its name or size | "What arrived did not match what was offered", nothing saved |
| bad chunks | kolo-blobs checks every chunk; nothing saved |
| a name such as `../x`, `a/b` or with control characters | declined, or cleaned before it is saved (see below) |
| a name or a path too long for this computer | declined at the offer for the top name, and for the rest right after the small metadata log is pulled and before any content, with the length of the first name over the limit and never the name |
| malformed control messages | the session ends with "The other computer sent something this example does not understand" |
| a session that never ends | allowed: it costs one topic and keep-alives. Close it at any time |
| filling your disk with auto-save | auto-save is off by default, per session, and starts off in every session; once on, it saves at most half of the free space the folder had when you turned it on, then turns itself off, and the next item waits for your click (turn it on again for a new budget) |
| an offer that says it is 1 TiB, to fill your disk | the disk is checked before the first byte, for the copy in the data folder and the saved files, and the offer is refused with how much it needs and how much is free; what a sender gives is also bounded by the offered size, so a sender that never stops is cut off |
| a stranger who learns the session topic | refused: only the paired key is admitted. Before that refusal the connection costs memory, see [memory](#memory) |

## Consent before anything is written

Nothing is written into your folder before you click Save, or before an item arrives in a session where you turned auto-save on. Until then the example holds only the offer in memory; during a receive the data sits in the private `transfers/` folder of the data directory, and your folder gets only the `.part` entry described below.

## Local network

With `-local`, the entry node of the [local network](configuration.md#local-network) listens on `127.0.0.1` only, and so does the example: nothing is reachable from other computers. Programs on this computer can reach it, as they can any entry node; they still need a pairing code to connect to you.

## Checks before anything is saved

- Before a byte is pulled, the signed heads of the two logs are checked against the limits (see [configuration.md](configuration.md)).
- As soon as the metadata log (the names and sizes, a few hundred bytes per file) is pulled, and before one byte of content, the whole tree is checked against the offer, the limits and this computer: the number of files and folders, the bytes, every name, and its length. A name of more than 255 bytes on macOS and Linux (the top name leaves room for ` (99)`), and a path, with the folder you chose, over the system's limit (1023 bytes on macOS, 4095 on Linux, 32767 UTF-16 units on Windows) are refused with a sentence that gives the length of the first one over the limit and its limit, never the name. The check is the receiver's, since the sender does not know what system saves the item. The tree is checked again as it is written.
- In each name, control and format characters (Unicode Cc and Cf, such as the bidi controls that make `invoice‮gpj.exe` look like `invoiceexe.jpg`) become `_`. Then kolo-files' `LocalPath` refuses what this system cannot save safely.
- Every limit is checked before the data it bounds is used, and every refusal is a typed error with a sentence in the window.

## How files are written

- Everything is written under an `os.Root` of the folder you chose, with `Mkdir` and `O_EXCL`: no name can leave that folder, no link in it is followed, and two names that are one name on this disk fail instead of overwriting.
- The download is written under a temporary `.kolo-share-<hex>.part` name in that folder, then renamed onto the item's name, or "name (2)" when that name is taken, which is reserved first.
- If anything fails or you cancel, both are removed. If the example is killed or the computer stops during a receive, the next start of the example removes the `.part` entry.

## Received files are marked as downloaded

The system checks them before they open:

- macOS: the `com.apple.quarantine` attribute on every file and folder, so Gatekeeper asks before a received app runs.
- Windows: a `Zone.Identifier` stream with zone 3 on every file, for SmartScreen and Office Protected View.
- Linux has no common mark.

A disk that cannot hold the mark (FAT, some network shares) gets none, and the example logs one line.

"Show in folder" selects the saved item in Finder (`open -R`) or Explorer (`explorer /select`), and on Linux opens its folder: it never opens or runs the item.

## Files that are sent

The folder you send is read after it was scanned, and a program that can write in that folder could put a link where a file was. The example opens each file, and refuses it if the path is a link by then or no longer names the file it opened, so it never sends a file through a link planted after the scan.

## The pairing code and the clipboard

"Copy pairing code" (the tray and the notch) puts the code on the clipboard, which every program can read and clipboard managers keep. When that pairing ends (connected, cancelled or expired), the example empties the clipboard if it still holds that code; if you copied something else since, it is left alone. A clipboard manager that already kept the code keeps it, and the code is single use and ends then.

## Memory

At start the example sets a soft memory limit for the Go runtime: half of the physical memory, never under 512 MiB, and 2 GiB when the memory cannot be read. Set the standard `GOMEMLIMIT` variable (for example `GOMEMLIMIT=3GiB`, or `off`) and the example leaves the limit to it. The limit is soft: near it the runtime collects garbage harder, which keeps a busy example from growing without need, but it does not stop memory that is held. The numbers in [receive windows and connections](configuration.md#receive-windows-and-connections) are what a hostile peer can hold: with the public network as the default the example is one UDP socket open to the internet, and a host that is not paired can make it hold memory (about 812 MiB per connection and 4 connections per address in the worst case, from the transport's own figures) until the system ends the example. The connection counts, `MaxConnsPerIP` and the window sizes bound this; they do not remove it.

## Your identity

The identity is `identity.key` in the data directory, mode 0600, never printed. The data directory is 0700; an existing one open to other users is refused. On Windows the mode check does not apply; keep the data directory inside your user profile, the default.

`pending.json`, `settings.json` and `history.json` are written to a temporary file, flushed to the disk, renamed over the old one, and the folder is flushed too, so after a power loss each holds its old content or its new one, never an empty file that would hide a `.part` entry from the next start.

## Reporting a problem

Report a security issue as [SECURITY.md](../SECURITY.md) says.
