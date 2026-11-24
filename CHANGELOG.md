# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-11-24

### Fixed

- A receive over a path slower than about 8.5 KB/s stopped at about 4.9 KB and never moved: kolo-sync cut each answer of about 1 MiB off for its average speed and asked for it again from its first byte. kolo-sync now cuts a message off only when 16 KiB of it did not arrive within 60 seconds, or when a whole answer averaged under 500 bytes a second, so a slow path finishes.
- The progress of a receive, and the sender's view of it, moved once per answer of about 1 MiB (it was the size of the store, so it started at the 4.9 KB of an empty store): it now counts the bytes taken off the connection and moves with every packet.
- Cancel while a received item was written into the folder could be ignored: a cancel that came during the copy of its last file, which a file of up to 64 MiB makes in one piece without a check, let the item be saved and end as done. The item is now not put in the folder once its receive was cancelled.

### Added

- A receive that moves at under 8 KB/s says "The connection is slow" with the speed it measures, instead of looking frozen.

### Changed

- `-bootstrap`, `-relay` and `-relay-file` no longer stop at 16 entries, as kolo-swarm no longer does. Duplicate and clashing relays are still refused.
- Up to 118 sessions may be open at once, not 16: as many as fit kolo-transport's default `MaxMemory` of 1 GiB with their windows and their growth, now that kolo-swarm has no topic cap. The session swarm takes `MaxPeers` 236, `MaxConns` 150 and `MaxDialed` 268, and the growth budget is 914.5 MiB.
- The default `-find-timeout` with relays is 72 s instead of 195 s: kolo-swarm now tries the relays once kolo-dht's hole punch window (12 s) passed, not only after the direct attempt failed (up to 135 s), so a code pasted on a computer that needs a relay finds the other one sooner.

## [0.1.0] - 2026-11-20

### Added

- First release of kolo-share-wails3-example.
