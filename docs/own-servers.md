# Your own servers

What Kolo Share needs from the network, and how to run it yourself.

## What the example uses

| Server | Kolo Share | Point the example at it with |
|---|---|---|
| Entry node (`kolo-node run`) | the public ones by default; your own for a private network | `-bootstrap HOST:PORT[,HOST:PORT...]` or `KOLO_BOOTSTRAP` |
| Relay (`kolo-node relay`) | optional | `-relay HOST:PORT@KEY` (repeatable, or a comma list), `-relay-file FILE`, or `KOLO_SHARE_RELAYS` when neither flag is given |
| Pin (`kolo-pin run`) | not used | nothing |

With no network flag the example uses the public network: entry nodes Ciranda runs, free, with no uptime guarantee while Kolo is a beta. They are entry nodes only: there is no public relay and no public pin, and the example never defaults to one. To keep a private network, run entry nodes yourself and give them with `-bootstrap`; the example then uses only your list. `-local` keeps everything on one computer. See [try it](try-it.md).

## When you need one

- **Your own entry node**: for a network that only your team uses, or when you do not want to depend on the public network.
- **A relay**: needed only when two computers cannot connect directly, and you run your own. Two computers behind strict (symmetric) NATs, or behind a firewall that drops unknown UDP, cannot connect directly. Both examples list the same relay, and the pairing, the session and its files go through it when the direct connection fails. The connection stays encrypted end to end between the two examples: the relay copies bytes it cannot read. It stores nothing, and it sees each example's key, its address, and who connects to whom, when and how much.
- **A pin**: keeps encrypted data for people who are online at different times, such as a chat. A Kolo Share session moves files only while both examples are open, so it uses none.

## Run an entry node

Run it on a host both computers reach: a public IPv4 address, such as a VPS, or a router that forwards UDP port 7447 to it.

Install kolo-node.

```sh
go install github.com/ciranda-dev/kolo-node/cmd/kolo-node@latest
```

The first entry node. It creates its key file with mode 0600 on the first run and keeps it for the next runs.

```sh
kolo-node run -listen 0.0.0.0:7447 -identity "$HOME/kolo-node/node.key" -first
```

A second entry node on another host, so the network stays up when one stops. Replace `<node-1>` with the first node's address.

```sh
kolo-node run -listen 0.0.0.0:7447 -identity "$HOME/kolo-node/node.key" -bootstrap <node-1>:7447
```

Each computer: check that the node answers. It prints `ok <node-1>:7447`.

```sh
kolo-node check <node-1>:7447
```

Each computer: start the example with the same list.

```sh
./bin/kolo-share -bootstrap <node-1>:7447,<node-2>:7447
```

`KOLO_BOOTSTRAP=<node-1>:7447,<node-2>:7447` does the same; a flag wins over the variable. The [kolo-node deploy guide](https://github.com/ciranda-dev/kolo-node/blob/main/docs/deploy.md) has a systemd unit and the firewall rules for a server.

## Run a relay

On a host both computers reach, with its own key file and its own UDP port. It logs its key as `key` in its `identity` line when it starts: that is the `<relay-key>` below, 64 hex characters.

```sh
kolo-node relay -listen 0.0.0.0:7448 -identity "$HOME/kolo-node/relay.key"
```

Without `-allow-file` the relay is open to any key and logs `open relay`. `-allow-file` lists the keys that may hold a place at the relay, which is what lets an app receive connections through it; any key may still dial through it. List each example's key (the example prints it in the terminal, in the `kolo-id` line), one per line.

```sh
kolo-node relay -listen 0.0.0.0:7448 -identity "$HOME/kolo-node/relay.key" -allow-file "$HOME/kolo-node/allow.txt"
```

A pairing code waits under a key made for that one code, which no allow list can name. So with `-allow-file` a pairing never goes through the relay: it works only when the computer that shows the code is reachable directly, and Settings says that a code cannot wait there. Allowing more keys does not change that. The sessions of the listed apps do go through it.

Each computer: check that the relay answers. It prints `ok relay ...`.

```sh
kolo-node check -relay <relay-host>:7448@<relay-key>
```

Each computer: start the example with the same entry nodes and relay.

```sh
./bin/kolo-share -bootstrap <node-1>:7447,<node-2>:7447 -relay <relay-host>:7448@<relay-key>
```

`KOLO_SHARE_RELAYS` takes the same list; a flag wins over the variable. Settings shows the relays in use, where they came from, and the state of each: ready, not in use, or why it holds no place (a wrong key, not allowed, a code that cannot wait there, full, unreachable). The terminal logs an error the first time a relay gives it. A place that ends with the relay's lifetime is taken again without a message.

With kolo-relay's defaults, a relay ends a relayed connection after 1 GiB in one direction or after 1 hour, and reads at most 1 MiB a second from each example. For larger items, start the relay with higher `-relay-max-session-bytes`, `-relay-max-session-duration` and `-relay-rate-bytes`.

## Ports

- Entry node: UDP 7447 in, in the host's firewall and in the provider's. With ufw: `sudo ufw allow 7447/udp`.
- Relay: UDP 7448 in, the same way. With ufw: `sudo ufw allow 7448/udp`.
- The example: UDP on a port the system picks; `-listen 0.0.0.0:PORT` sets a fixed one to open in a firewall.
- No TCP port is used.
