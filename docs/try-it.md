# Try it

Build once; it makes `bin/kolo-share`:

```sh
GOWORK=off wails3 build
```

Each window takes its own data folder with `-data`. The example creates it with mode 0700; do not create it yourself, since a folder open to other users is refused.

## Two computers, public network

With no network flag the example connects to the public network, which Ciranda runs. It is free and has no uptime guarantee while Kolo is a beta. It has entry nodes only: there is no public relay and no public pin. Settings says the example runs on the public network and does not list its entry nodes.

```sh
./bin/kolo-share
```

On the first computer click Show a code, then Copy code. On the second paste it in Paste a code and click Connect. The two computers can be on different networks. If the public network cannot be reached (no internet, or no entry node answers), the window says so and names the three ways to choose a network: the public one, your own with `-bootstrap`, or this computer only with `-local`. The example never changes network by itself, so pick one and start it again.

## Same computer

`-local` (or `KOLO_SHARE_LOCAL=1`) runs a [local network](configuration.md#local-network): the window that opens first runs an entry node on `127.0.0.1:7451`, and the others join it. Start the windows in any order, or at once. It cannot be combined with `-bootstrap` or `-first`.

Each window takes its own data folder with `-data`. The example creates it with mode 0700; do not create it yourself, since a folder open to other users is refused.

Terminal 1: Alice.

```sh
./bin/kolo-share -local -data /tmp/kolo-share-demo/alice
```

Terminal 2: Bob.

```sh
./bin/kolo-share -local -data /tmp/kolo-share-demo/bob
```

In Alice's window click Show a code, then Copy code. In Bob's window paste it in Paste a code and click Connect. Both windows open the session: drop files or folders on either window, and the other side clicks Save (into Downloads, or the folder you choose with Change). Send as many as you like, in both directions, until one of you clicks Close session.

If the first window closes, the sessions already connected keep working, but a new code cannot be made or pasted; close the sessions and press Try again in one window, and it runs the entry node from then on.

If port 7451 is taken, run the same test network with explicit flags, without `-local`.

Terminal 1:

```sh
./bin/kolo-share -data /tmp/kolo-share-demo/alice -first 127.0.0.1:4000
```

Terminal 2:

```sh
./bin/kolo-share -data /tmp/kolo-share-demo/bob -bootstrap 127.0.0.1:4000
```

Live reload of the window:

```sh
KOLO_SHARE_DATA=/tmp/kolo-share-demo/dev KOLO_SHARE_LOCAL=1 GOWORK=off wails3 dev
```

## Your own network, on the same network

With `-bootstrap` the example uses only your entry nodes and never the public network. The local network listens on loopback only, so another computer cannot reach it. Computer A runs a [kolo-node](https://github.com/ciranda-dev/kolo-node) entry node on all its addresses (`0.0.0.0`), and the examples on A and B point at A's LAN IP. Replace `<ip-of-computer-a>` with that IP.

Computer A: install kolo-node.

```sh
go install github.com/ciranda-dev/kolo-node/cmd/kolo-node@latest
```

Computer A: run the entry node. It creates its identity file with mode 0600 on the first run, at the path you give, and keeps it for the next runs.

```sh
kolo-node run -listen 0.0.0.0:7447 -identity "$HOME/kolo-node/node.key" -first
```

Find A's LAN IP with `ipconfig getifaddr en0` on macOS, `hostname -I` on Linux, or `ipconfig` on Windows (the IPv4 address).

Allow UDP port 7447 on A. macOS asks once whether kolo-node may accept incoming connections. On Linux:

```sh
sudo ufw allow 7447/udp
```

On Windows:

```sh
netsh advfirewall firewall add rule name="kolo-node" dir=in action=allow protocol=UDP localport=7447
```

Computer A and computer B: check the entry node. It prints `ok <ip-of-computer-a>:7447`.

```sh
kolo-node check <ip-of-computer-a>:7447
```

Computer A and computer B: start the example.

```sh
./bin/kolo-share -bootstrap <ip-of-computer-a>:7447
```

The two examples then connect to each other directly, on UDP ports the system picks. A firewall that drops incoming UDP to the example can stop that: allow the example when macOS or Windows asks. With ufw on Linux, allow UDP from the LAN:

```sh
sudo ufw allow proto udp from 192.168.1.0/24 to any
```

## Your own network, on different networks

The same steps, with `<ip-of-computer-a>` being A's public address or name. A needs a public address, such as a VPS ([kolo-node on a VPS](https://github.com/ciranda-dev/kolo-node/blob/main/docs/deploy.md)), or a port forward of UDP 7447 on its router to A. The entry node helps the two examples punch through their NATs. Two computers behind strict (symmetric) NATs connect only through a relay both list with `-relay`. What the example can use, and how to run entry nodes and a relay, is in [your own servers](own-servers.md).

## Other ways to choose the network

- Nothing: the public network.
- `-bootstrap HOST:PORT[,HOST:PORT...]` for one run, your own network.
- `KOLO_BOOTSTRAP=HOST:PORT` in the environment does the same.
- `-local` or `KOLO_SHARE_LOCAL=1`: this computer only.

Settings shows the network, the entry nodes of your own network (never the public ones) and where they come from, read only.

Every flag is in [configuration](configuration.md).

## It does not connect

1. `kolo-node check <ip-of-computer-a>:7447` prints `ok` on both computers. If not, the node is not running, the address is wrong or UDP 7447 is blocked.
2. Both examples are on the same network: Settings shows it in each window. The public network is the default, `-bootstrap` lists your own entry nodes, and `-local` is a network on one computer that no other computer reaches. Two examples on different choices never meet.
3. The window that shows the code stays open, and the other pastes all of it before it expires (1 hour, `-pair-ttl`). A pasted code looks for the computer that made it for 1 minute (`-find-timeout`). "This code was already used" means someone pasted it first: make a new one.
4. A session that says "Cannot reach Bob directly" keeps trying while it is open. On different networks, start both examples with the same relay (`-relay`, see [your own servers](own-servers.md#run-a-relay)), or try from another network (a phone hotspot).
5. A session that shows offline after the other example quit or crashed stays so until you close it: a new pairing with the same person replaces it.
