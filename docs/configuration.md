# Configuration

Every setting is a flag or an environment variable; a flag wins. A bad setting makes the window show the reason. The window has no network form: Settings only shows the network (public, your own or this computer only), the entry nodes of your own network (not the public ones), the relays and where they come from.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `-data` | `KOLO_SHARE_DATA` | `<user config dir>/kolo-share` | the data directory (see below) |
| `-bootstrap` | `KOLO_BOOTSTRAP` | the [public network](#networks) | the entry nodes of your own network, a comma list of `host:port` as for kolo-swarm's `ParseNodes`: IPv4 or a host name, at most 16. Used alone: the public network is not touched |
| `-local` | `KOLO_SHARE_LOCAL` | off | a [network on this computer only](#local-network): `1` or `true` turns it on, `0` or `false` off; the flag wins over the variable. Refused together with `-bootstrap` or `-first` |
| `-first` | `KOLO_SHARE_FIRST` | none | run the first entry node of a test network on this loopback IP:PORT, such as `127.0.0.1:4000`, as your own network. For local testing only |
| `-listen` | `KOLO_SHARE_LISTEN` | `127.0.0.1:0` when every entry node is on loopback, else `0.0.0.0:0` | local IPv4 IP:PORT |
| `-max-download` | `KOLO_SHARE_MAX_DOWNLOAD` | `1099511627776` (1 TiB) | bytes one received item may download: a safety bound that you can raise or lower, not a size the example is tuned for (see below and [very large items](how-it-works.md#very-large-items)) |
| `-max-files` | `KOLO_SHARE_MAX_FILES` | `100000` | files and folders one item may hold, on both sides (see below) |
| `-relay` | `KOLO_SHARE_RELAYS` | none | relays, `host:port@key` with the relay's key as 64 hex characters, as for kolo-swarm's `ParseRelays`; repeat the flag or give a comma list; at most 16 in all, duplicates dropped. `KOLO_SHARE_RELAYS` is read only when neither `-relay` nor `-relay-file` is given |
| `-relay-file` | none | none | a file of relays, one `host:port@key` per line, `#` starts a comment; repeatable, added to `-relay`. A regular file of at most 64 KiB; a missing or unreadable one "cannot be read" |
| `-find-timeout` | `KOLO_SHARE_FIND_TIMEOUT` | `1m`, `72s` with relays | how long a pasted code looks for the computer that made it. With relays the default covers kolo-dht's hole punch window of 12 s, after which the relays are tried too, and one relay dial of 60 s |
| `-lookup-interval` | `KOLO_SHARE_LOOKUP_INTERVAL` | `5s` | time between lookups of a topic, and the longest wait before a dial that failed is tried again, in a pairing and in a session |
| `-pair-ttl` | `KOLO_SHARE_PAIR_TTL` | `1h` | how long a pairing code works, from `1s` to `720h` (see below) |
| `-resume-for` | `KOLO_SHARE_RESUME_FOR` | `30m` | how long a download waits for bytes that stopped before it fails, from `1s` to `24h` (see below) |
| `-congestion` | `KOLO_SHARE_CONGESTION` | `bbr` | how the connection to a paired computer paces what it sends: `reno`, `cubic` or `bbr` (see [congestion control](#congestion-control)) |
| `-quiet-transfers` | `KOLO_SHARE_QUIET_TRANSFERS` | off | no `transfer:` log line every 5 seconds while a transfer runs, on the sending and the receiving side; the line at the end of each pull stays. `1` or `true` turns it on, `0` or `false` off; the flag wins over the variable (see [what a transfer log line says](how-it-works.md#what-a-transfer-log-line-says)) |

- A `-congestion` other than `reno`, `cubic` or `bbr`, a size setting of 0, a `-pair-ttl` or `-resume-for` of 0, a `-pair-ttl` under 1s or over 720h, and a `-resume-for` under 1s or over 24h, is refused: leave it out for the default.
- Kolo is IPv4 only: an IPv6 entry node, relay or listen address is refused.
- Names follow the kolo-files limits: at most 765 bytes per name and 2048 levels. The computer that saves an item applies its own limits too, and refuses the item before the download starts (see [security](security.md#checks-before-anything-is-saved)): a name of at most 255 bytes on macOS and Linux (255 UTF-16 units on Windows), and a whole path, the folder included, under 1024 bytes on macOS, 4096 on Linux and 32767 UTF-16 units on Windows.

## Networks

The example joins one of three networks, chosen when it starts:

| Network | How | Who can connect |
|---|---|---|
| Public (default) | no flag | computers anywhere, through the public entry nodes Ciranda runs: free, with no uptime guarantee while Kolo is a beta |
| Your own | `-bootstrap` (or `KOLO_BOOTSTRAP`) with your entry nodes, or `-first` | computers that list the same entry nodes |
| This computer only | `-local` (or `KOLO_SHARE_LOCAL=1`) | windows of the example on this computer |

- The public network has entry nodes only: there is no public relay and no public pin, and the example never defaults to one. A relay is needed only behind strict NATs, and you run your own (`-relay`, [your own servers](own-servers.md#run-a-relay)).
- With `-bootstrap` the example uses your list alone. `-local` with `-bootstrap` or `-first` is refused with a message that names both; so is a `-bootstrap` with no entry node.
- If the public network cannot be reached when the example starts, the window shows an error that names the three ways to choose a network, with Try again. The example never falls back to another network.
- Two examples on different networks never meet. Settings shows the network each one uses.

## Local network

`-local` runs a network on this computer:

- The window that opens first runs the entry node on `127.0.0.1:7451`. The port is fixed so every window finds it without a flag; it sits next to kolo-node's 7447 and 7448 and below the ephemeral port ranges, so no random port takes it.
- A window that finds the port taken joins that entry node. Windows started at once agree: exactly one gets the port.
- It listens on loopback only: other computers cannot reach it, so it works only between windows on one computer. For two computers, use the public network or list your own entry nodes (see below).
- If the window that runs the entry node closes, the sessions already connected keep working, but a new pairing fails. Close the sessions and press Try again in one window: it takes the port over and the others follow when they reconnect too.
- If another program holds the port, the window says so: close that program and press Try again, or start the example without `-local` (the public network) or with `-bootstrap`.

## Pairing code lifetime

`-pair-ttl` defaults to 1 hour: kolo-invite asks for a short lifetime and uses one hour in its example, and it is the shortest the board example offers. It may be from 1s to 720h (30 days, the longest invite lifetime of the chat and board examples; kolo-invite itself sets no maximum). A code ends earlier at its first use, at Cancel and when the example quits, and it is never written to disk, so nothing survives a restart. Settings shows the lifetime, read only.

## Waiting for a lost connection

A receive that loses its connection, or that moves no byte, keeps what it already verified and tries again by itself (see [how it works](how-it-works.md#when-the-connection-is-lost-or-slow)). `-resume-for` is how long it waits since the last byte arrived before it fails with "Nothing came from the other computer for 30 min, so the transfer stopped". The default of 30 minutes covers a laptop that sleeps, a network that changes or a sender that restarts its connection, and a sender that left does not hold a download for a day. `Settings.resumeFor` carries the bound, in milliseconds. Cancel works at any time.

## Download size and file count

- **`-max-download`** is checked when an item is offered, counted while it downloads (the download's store may hold a little more than the item itself, which the count allows for) and checked again before it saves, so a sender cannot fill your disk with one item. The default is 1 TiB, so an item of 100 GB or more is taken; lower it to keep a small disk safe, raise it past 1 TiB for larger items. Saving takes as much again, until the downloaded copy is removed. The disk is checked before the first byte of a send or a receive: see [very large items](how-it-works.md#very-large-items).
- **`-max-files`**: replaying a volume takes about 211 + 344 bytes per node (kolo-files) and kolo-store about 250 bytes per open entry, so the default keeps a receive near 300 MB of memory. The content log is bounded by it: 4 entries per file, 2 per 256 KiB of the download limit (kolo-blobs' smallest chunk) and 64 more.

## Stores of large items

An item of 64 MiB or more is stored (on the sender) and received (on the receiver) with a tuned kolo-store: a 256 MiB memtable and no compression. Smaller items keep the default options, so a session with many small files stays light. These are fixed in the example (`tunedStoreFrom`, `tunedMemTable` and `maxTunedStores` in `store.go`), not settings.

- **Memory**: a tuned store can use up to twice its memtable, 512 MiB, while the item is open. The example keeps at most 2 tuned stores open at once, so they add at most 1 GiB. A third large item uses the default options (a 4 MiB memtable) and is slower to prepare, not refused.
- **Disk and durability**: ciphertext does not compress, so the store is about the size of the item and a fifth to a quarter more while it is written (measured 1.2 times for a 4 GiB item). The commits of a store are grouped: they are queued (at most 64 MiB) and written to the disk in batches of up to 32 MiB, one sync for each batch, in the order they were made, so a crash leaves a store that holds fewer entries, never a gap. The example does not keep a store across a restart, so a crash loses nothing it would have used; the folder of a store is removed when its item ends.

## What can slow a transfer

No rate limit, sleep or per-item deadline sits on the data path. The caps that exist are for safety, with the number and the reason, so you can see that only the link, the disk and the round trips of the pull decide the speed:

| Cap | Value | Why | Slows a transfer? |
|---|---|---|---|
| `-max-download` | 1 TiB | a sender that never stops cannot fill the disk | no, a size bound |
| `-max-files` | 100,000 files and folders | memory of a volume (about 555 bytes a node) and inodes | no, a count bound |
| items waiting per session and direction | 16 | kolo-sync serves 16 streams per connection | no |
| receives at once per session | 4 | each has its own streams; more adds streams, not speed | no on one link |
| sessions | 16 | one topic each | no |
| tuned stores at once | 2 (a third item uses default options) | memory: up to 2 x 256 MiB a store | a third large item prepares more slowly |
| queue of a store's commits | 64 MiB, written in batches of up to 32 MiB | memory; a commit waits for the disk past it | only when the disk is the limit |
| sealing goroutines of a send (kolo-blobs) | 2, at most 5 chunks in flight (about 10 MiB) | memory; the chunker feeds about 2 GB/s, so two keep up with it | no |
| goroutines that write the saved files | 4, each with a 4 MiB buffer; a large file in ranges of 64 MiB | memory and the disk's queue | no |
| readers of the first index of a log (kolo-blobs) | 4, in windows of 64 entries | memory (4 entries of up to 4 MiB) | no |
| offers per minute, per session | 60 | a flood of offers; an item is one offer | no |
| size of a message of kolo-sync | 4 MiB, answers filled to about 1 MiB | memory of the peer | no: the pull streams the answers (see below) |
| time of a kolo-sync message | 60 s to start, then each 16 KiB of it within 60 s, and the whole of it written at 500 bytes a second on average (about 36 minutes for an answer of 1 MiB) | a silent or trickling peer | only below 500 bytes a second |
| `-resume-for` | 30 min, 1 s to 24 h | a sender that left | ends a stalled transfer, never a running one |
| a card of an item being prepared | gives up after 2 min without a word; the sender speaks every 30 s, also during the scan of 100,000 files | a sender that left | no |
| receive windows of kolo-transport | 256 KiB at the start, grown for the paired peer to 4 MiB per stream and 8 MiB per connection (`transport.go`) | memory of the connections of strangers and of the growth (see below) | a stream moves one window per round trip: about 335 Mbit/s at 100 ms once grown, 21 at the start |
| the disk | free space checked before the first byte | see [very large items](how-it-works.md#very-large-items) | no |

What decides the speed of a large item is the pull: kolo-sync asks once for the entries and gives credit as it reads, so the peer sends answers of about 1 MiB one after the other and the link stays full; the pull verifies and stores each answer in order. It keeps from 2 to `InFlight` (8, its default; the example leaves it) requests of 2 MiB of credit waiting, as many as twice the measured round trip times the speed asks for, and never holds more than 6 MiB of answers that are not stored. What is left to limit it is the link, the transport's window, the sender's uplink and the receiver's store; the `transfer:` line of each side says which ([how to read it](how-it-works.md#what-a-transfer-log-line-says)).

## Receive windows and connections

The session connection carries files, so the example turns on kolo-transport's window growth for its session swarm (kolo-transport's guide, "Faster transfers for trusted peers"). Every connection starts with 256 KiB per stream and 256 KiB per connection, kolo-transport's default stream window for both, so a connection nobody marked never grows. kolo-swarm marks a connection once the swarm's `Allow` has admitted its peer, and the example's `Allow` admits only the key of an open session, the paired peer (a test checks that, and that no other key is marked). A marked connection doubles its windows while the example reads them fast, up to 4 MiB per stream and 8 MiB per connection (`grownStreamWindow` and `grownConnWindow` in `transport.go`; kolo-transport's own defaults for them). A pull moves at most one window per round trip, so a stream's ceiling is about 335 Mbit/s at 100 ms and 84 at 400 ms once grown, and 21 and 5 at the start. These numbers are fixed in the example, not settings. The pairing swarms keep kolo-transport's defaults: a pairing sends a few hundred bytes.

The ceilings only matter above about 100 ms of round trip on a fast link. At 20 to 35 ms, the window over the round trip is 100 MB/s and more (4 MiB at 35 ms is about 120 MB/s), above any link a home or an office has, so a larger ceiling adds nothing there and only raises the memory budget below. Over 100 ms and a link of 100 Mbit/s or more the window is what holds a stream back: kolo-sync's streamed pull over a simulated link with no rate limit, 192 MiB, reached 26 MB/s at 100 ms and 13 at 200 ms with 4 and 8 MiB, and 46 and 23 with 8 MiB per stream and 16 MiB per connection. Raising them is a change of the two constants in `transport.go`, `grownStreamWindow` and `grownConnWindow`, and a rebuild; `grownMemory` follows the connection ceiling, and every number below is computed for 4 and 8 MiB, so a larger ceiling raises the budget and the worst cases with it.

The initial windows apply to every connection of the session swarm's socket, strangers included, since the swarm's one socket takes the connections before it knows whose they are. So the example also sets the swarm's own counts, which fit what it can have, a peer per session (16) and one more while a peer reconnects: `MaxPeers` 32, `MaxHandshakes` 16 and kolo-secure's `MaxPending` 16 give kolo-transport `MaxConns` 48 and `MaxDialed` 64 (kolo-swarm's defaults, for 64 peers, are 112 and 144). `MaxIncomingStreams` stays 16, which kolo-sync needs for 16 open offers, and kolo-transport's `MaxConnsPerIP` stays 4.

The growth is shared out of one budget, `MaxGrownMemory`: 16 times the growth of one connection (8 MiB minus 256 KiB), 124 MiB, so each of the 16 paired peers can reach the ceiling at once. A step of growth that the budget cannot pay is refused and the connection goes on with the window it has; the budget returns when a connection ends. A connection of a stranger never takes from it.

- **Window budget**: (48 + 64) connections times 256 KiB is 28 MiB of unread data at the start, plus at most 124 MiB of growth, 152 MiB in all, far under kolo-transport's default `MaxMemory` of 1 GiB (a test checks it, and kolo-swarm derives `MaxMemory` itself, so its check passes).
- **Normal use**: one connection per paired peer, so at most 16 connections hold 128 MiB between them (16 times 8 MiB), and only while every one of them is pulling at full speed. A peer that sends large frames, as the example does, costs about 1.2 heap bytes per unread byte, so that is about 154 MiB of heap.
- **Worst case of a stranger**, from kolo-transport's measured factors (docs/limits.md of kolo-transport: about 96 heap bytes retained per byte of window when streams are filled with one-byte frames, and about 176 per unread byte): `2 x MaxIncomingStreams x 256 KiB x 96` is 768 MiB plus `256 KiB x 176`, 44 MiB, about 812 MiB for one connection, and about 89 GiB for the 112 connections. It needs 28 hostile addresses (4 connections each), and a connection that fails the handshake, the hello or `Allow` is closed.
- **Worst case of a paired peer**: a marked connection at its ceiling can hold `2 x 16 x 4 MiB x 96`, 12 GiB, plus `8 MiB x 176`, 1.4 GiB, about 13.4 GiB, and the budget allows 16 of them: about 214 GiB, if all 16 paired peers were hostile and sent one-byte frames. Pair only with people you would trust with that. Set `GOMEMLIMIT` a little under the memory you give the example if the computer is shared with something that cannot lose memory.

## Congestion control

The connection to a paired computer paces what it sends with BBR by default, because the example only moves files the person asked to move. kolo-transport's other controller, Reno, treats every lost packet as a full queue and halves its window, so a link that also loses packets at random (a mobile or a Wi-Fi link) runs far under its speed; BBR estimates the link's rate and does not slow down for isolated losses. Measured by kolo-transport with 1 percent loss on 20 Mbit/s and 120 ms, Reno reads 1.8 Mbit/s and BBR 18.9. [The speed of a pull](how-it-works.md#the-speed-of-a-pull) has the measurement of this example.

`-congestion reno` (or `KOLO_SHARE_CONGESTION=reno`) turns it off; `cubic` is kolo-transport's third controller and is no faster than Reno under random loss. Any other value is refused. The setting applies to the connection of the session swarm, on the side that sends the data (both examples set it for their own sending); the pairing swarms stay on Reno.

BBR keeps a standing queue of 25 to 150 ms in the bottleneck of the path and takes bandwidth from other flows when that queue is shallow: on a 20 Mbit/s link shared with a Reno flow and a queue of 100 ms or less, a BBR flow left the Reno one under 0.5 Mbit/s (kolo-transport's measurement, docs/limits.md and its guide). A transfer on a shared home or office link can slow a call or a download of someone else on it while it runs. Set `reno` if that matters more than the speed of the transfer. Fairness against other stacks was not measured, and nothing here says BBR helps where the loss is not random.

## Relays

Two computers that cannot connect directly, for example both behind strict NATs, can still reach each other through a [kolo-relay](https://github.com/ciranda-dev/kolo-relay) server, such as one run with `kolo-node relay` ([your own servers](own-servers.md#run-a-relay)). The list is empty by default, and kolo-swarm ships none. Give them with the flags or the variable:

```sh
./bin/kolo-share -relay relay1.example.org:7448@<key in hex> -relay-file relays.txt
```

- Both of the example's swarms use the list: the one of a pairing and the one of the sessions. A connection is tried directly first; the relays are tried too when the direct connection fails, or when it has not connected after kolo-dht's hole punch window (12 s), and a direct connection that comes later replaces the relayed one.
- Two computers meet through a relay only when both list it.
- The example holds a place at its relays only while it shows a pairing code or has a session open.
- A relay operator can read nothing of what it carries (the connection is encrypted end to end between the two examples), but learns your key, your address, and who connects to whom, when and how much.

A relay with an allow list (kolo-node's `-allow-file`, which lists the keys that may hold a place there) must allow your example's key: the example prints it in the terminal, in the `kolo-id` line, once the network is up. The pairing waits under a key made for the code, which an allow list cannot name, so with an allow list a pairing never goes through the relay: it works only when the computer that shows the code is reachable directly, and Settings says that a code cannot wait there. Settings shows each relay's state and, when it holds no place, why.

## Where data lives

On Windows the data directory is not checked for who can open it, since the check is of Unix permissions: it relies on the access list of the folder, which a folder inside your user profile (`%LOCALAPPDATA%`, the default) inherits as private. Keep `-data` inside your profile.

The data directory holds `identity.key` (mode 0600), `settings.json` (your display name and the notch setting), `history.json`, `pending.json`, and `transfers/` while transfers run. No pairing code, session secret or session is ever written there. It is created with mode 0700. An existing data directory open to other users is refused, and so is one whose `transfers` folder this example did not make.

## Your own network

For a private network between computers, both examples list the same entry nodes: kolo-dht servers that both can reach, such as two [kolo-node](https://github.com/ciranda-dev/kolo-node) servers of your own. The first node of a new network starts with `-first`, and the others bootstrap from it, each with an identity file given as an argument:

```sh
dir=$(mktemp -d)
kolo-node run -listen 0.0.0.0:7447 -identity "$dir/a/node.key" -first
kolo-node run -listen 0.0.0.0:7448 -identity "$dir/b/node.key" -bootstrap node1.example.org:7447
```

Then give the list to the example:

```sh
KOLO_BOOTSTRAP=node1.example.org:7447,node2.example.org:7448 ./bin/kolo-share
```

With entry nodes that are not on loopback the example listens on `0.0.0.0:0`, so the other computer can connect. UDP must not be blocked between the two.
