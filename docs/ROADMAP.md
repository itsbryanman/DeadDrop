# Roadmap

## v0.1 (this release)

`deaddrop receive` on the LAN or through a Cloudflare quick tunnel.

## Before v0.2

Hand it to a few people who don't write software and watch them use it.
`send` waits until `receive` is clearly useful.

## v0.2: `deaddrop send PATH`

Serve a file or folder for temporary download, with the same link model,
expiry and terminal output. The session and server types already keep
receive-specific logic out of the shared parts so `send` can sit next to it.

## Maybe later

- `deaddrop clean --older-than 7d` for leftover partials, with a lock file
  so it can't touch a live session.
- Collision strategy flag (`--on-collision rename`, giving `name (2).ext`).
- Resumable uploads for very large files on flaky links.
- Optional local TLS with a QR-encoded certificate fingerprint.

## Not planned

Accounts, dashboards, a relay server, persistent settings, browser-to-browser
transfers, malware scanning.
