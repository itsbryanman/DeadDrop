# Dead Drop

Get files from someone's browser onto your computer without either of you
signing up for anything.

```
$ deaddrop receive

Dead Drop is receiving files
  Network:  Trusted LAN — HTTP, not encrypted
  Link:     http://192.168.1.25:8080/r#Xw3qDQMoLwXdJb4Srf8qMg
  Saving:   ~/Downloads/deaddrop/20261013-200655-af1a8f772f757ebe/
  Limit:    2 GiB/file, 5 GiB/session, 20 files, one upload at a time
  Expires:  30 minutes (at 20:36)

  [QR code]

Waiting for files. Press Ctrl+C to end this session.
✓ photo.jpg                   8.3 MiB    saved
✓ project-notes.pdf           1.2 MiB    saved
Session ended. Received 2 files (9.5 MiB) in ~/Downloads/deaddrop/20261013-200655-af1a8f772f757ebe
```

You start a session, share the link or let them scan the QR code, they drop
files on the page, and the files land in a fresh folder. Ctrl+C or the
timer ends it. The sender doesn't install anything.

It's one Go binary with the page embedded. There's no config file, no
account and no database. Nothing persists between runs except the files you
received.

Dead Drop is deliberately small. If you want a general-purpose file server,
look at [copyparty](https://github.com/9001/copyparty) or
[CasaDrop](https://github.com/chicohaager/casadrop) instead.

## Install

Needs Go 1.24 or newer.

```
go install github.com/itsbryanman/DeadDrop/cmd/deaddrop@latest
```

Or clone and run `make build`, which puts the binary in `bin/`.

## Usage

```
deaddrop receive [flags]

  --dir PATH              Destination base (default ~/Downloads/deaddrop)
  --expires DURATION      Session lifetime, e.g. 10m or 2h (default 30m, max 24h)
  --max-file SIZE         Per-file limit (default 2GiB)
  --max-total SIZE        Total for the session (default 5GiB)
  --max-uploads N         Maximum completed files (default 20)
  --port PORT             Listen port (default 8080 on LAN, random with --tunnel)
  --bind ADDRESS          Listen on this address instead of the detected LAN one
  --tunnel cloudflare     Serve over HTTPS through a Cloudflare quick tunnel
  --name TEXT             Title shown on the upload page (not verified)
  --json                  Machine-readable events, one JSON object per line
  --json-include-secret   Include the secret link in --json output
```

Sizes use binary units (`500MiB`, `2GiB`). Decimal units like `GB` are
rejected rather than guessed at.

### Across networks

```
deaddrop receive --tunnel cloudflare
```

This needs [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
installed. Dead Drop starts it against a loopback-only listener, waits until
the public `trycloudflare.com` address actually serves the page, then
prints the link. When the session ends, the `cloudflared` process it started
is stopped. Other `cloudflared` processes are left alone.

### Scripting

`--json` prints events (`session_started`, `upload_started`,
`upload_progress`, `upload_saved`, `upload_failed`, `warning`, `error`,
`session_ended`) one per line. The link holds the secret, so it's left out
unless you also pass `--json-include-secret`.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Session ended normally (Ctrl+C or expiry) |
| 1 | Unexpected runtime error |
| 2 | Bad command line |
| 3 | Network problem: no LAN address, port in use, listener failed |
| 4 | Tunnel problem: `cloudflared` missing, failed to start, or died |
| 5 | Destination folder unusable |

## What it does and doesn't protect

Please read this before using it for anything sensitive.

- **On a LAN the link is plain HTTP.** Anyone who can watch traffic on that
  network can see the files and the access key. Use it on networks you
  trust, or use `--tunnel cloudflare`.
- **The tunnel is HTTPS to Cloudflare, not end to end.** Cloudflare
  terminates TLS and can see metadata and content. Quick tunnels are meant
  for testing and have no uptime guarantee.
- **The link is the password.** It has a 128-bit random key in the URL
  fragment. Anyone who has the link can upload until the session ends.
  Screenshots, chat history and browser history can all keep a copy.
- **Received files are not scanned.** "Saved" means written to disk, nothing
  more.
- **Your computer has to stay online** for the whole transfer.
- If the process is killed hard (SIGKILL, power loss), half-finished uploads
  can be left as `.partial-*` files. Dead Drop tells you about them at the
  next start but won't delete them, since another session might own them.

See [docs/SECURITY.md](docs/SECURITY.md) for the full threat model.

## Platforms

Linux (amd64, arm64), macOS (Intel, Apple Silicon) and Windows (amd64).
CI runs the unit tests on all three, the subprocess integration tests on
Linux and macOS, and the browser tests on Linux.

## Development

```
make test     # unit + integration tests
make race     # same, with the race detector
make vet      # go vet and gofmt check
make e2e      # browser tests with Playwright (needs Node 22)
make dist     # cross-compiled binaries in dist/
```

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) describes how the pieces fit
together, and [docs/TEST-PLAN.md](docs/TEST-PLAN.md) lists what's covered
and what still needs a manual check.

## License

MIT
