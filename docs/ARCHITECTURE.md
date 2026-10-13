# Architecture

## Components

```
[browser]
   | LAN: plain HTTP          tunnel: HTTPS to Cloudflare's edge
   v
[cloudflared]  (only with --tunnel cloudflare; talks to us over loopback)
   v
[httpserver]   routes, security headers, bearer auth, origin check
   v
[session]      single upload slot, quota reservations, expiry
   v
[storage]      .partial file -> fsync -> no-replace commit
   v
~/Downloads/deaddrop/<session-id>/report.pdf
```

| Package | Job |
|---|---|
| `cmd/deaddrop` | Entry point. Parses flags, picks the address, wires everything up, handles shutdown. |
| `internal/cli` | Flag parsing and validation, help text, exit codes. |
| `internal/session` | The secret, the deadline, the upload slot and the byte/file accounting. |
| `internal/httpserver` | The HTTP API and the upload state machine. |
| `internal/storage` | Filename rules, partial files, no-overwrite commits, stale-partial scan. |
| `internal/network` | Picks a LAN address and warns about VPNs or ambiguous setups. |
| `internal/tunnel` | Runs and supervises `cloudflared`. |
| `internal/terminal` | Text and JSON output, QR rendering. |
| `internal/web` | The embedded page (HTML, CSS, JS, no build step). |

## Listening

LAN mode binds to **one** detected private IPv4 address, not `0.0.0.0`.
Detection skips loopback, link-local, down interfaces and virtual ones
(Docker, libvirt, VirtualBox and so on), prefers names like `en*`, `eth*`
and `wl*`, and prefers `192.168/16` over `10/8` over `172.16/12`. VPN
interfaces aren't picked but do trigger a warning. If nothing fits, the
command fails with exit code 3 and lists what it found. `--bind` overrides
all of this; `--bind 0.0.0.0` is allowed with a warning.

Tunnel mode binds `127.0.0.1` on a random port. `--bind` must be loopback
in tunnel mode so a session can't accidentally be exposed on the LAN too.

## HTTP contract

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/r` | none | The page. Contains no session data. |
| GET | `/assets/app.css`, `/assets/app.js` | none | Embedded assets. |
| GET | `/api/session` | bearer | Title, expiry, limits, counters. |
| POST | `/api/upload` | bearer | One file as the raw request body. |
| GET | `/healthz` | loopback, not forwarded | Readiness check before starting the tunnel. |
| anything else | | | 404 |

Every response carries `Cache-Control: no-store`, `Referrer-Policy:
no-referrer`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, a
CSP restricted to `'self'` with `frame-ancestors 'none'`, and same-origin
COOP/CORP. There are no CORS headers.

Uploads send the filename in `X-File-Name`, percent-encoded UTF-8 (what
`encodeURIComponent` produces), because browsers can't reliably send raw
UTF-8 in header values. The body is the file bytes. There's no multipart
parsing.

| Status | When |
|---|---|
| 201 | Saved. Body: `{"name": ..., "size": ...}` |
| 400 | Bad filename, or the body was cut short |
| 401 | Missing or wrong token |
| 403 | Session closed, file limit reached, or cross-origin |
| 408 | No body bytes for 60 seconds |
| 409 | Name already received |
| 413 | Over the per-file or session limit |
| 429 | Another upload is in progress (`Retry-After` is set) |
| 507 | Not enough disk space |
| 500 | Other I/O errors |

Error bodies are `{"error": "<code>", "message": "<text for people>"}`.

## Token handshake

1. 16 bytes from `crypto/rand`, base64url without padding (22 characters).
2. Link: `http://IP:PORT/r#TOKEN` or `https://HOST/r#TOKEN`.
3. The page reads `location.hash`, keeps the token in a JS variable, and
   immediately calls `history.replaceState` to drop the fragment from the
   address bar and the current history entry.
4. API calls send `Authorization: Bearer TOKEN`. No cookies, no storage, no
   query strings.
5. The server decodes strictly, checks the length and compares with
   `crypto/subtle.ConstantTimeCompare`. Malformed and wrong tokens get the
   same 401.

The session ID (the folder name) is generated separately and is not secret.

## Upload lifecycle

```
Idle -> Admitted -> Streaming -> Syncing -> Committing -> Complete
                        \            \           \
                         `-----------`-----------`--> Failed / Cancelled
```

1. **Before admission** (cheap checks, nothing reserved): method, token,
   origin, session open, filename valid, declared `Content-Length` under the
   per-file limit, name not already taken.
2. **Admission** (`session.Admit`): take the single upload slot first, so a
   second sender gets 429 without its body touching the disk. Then, under
   the session mutex: session open, file count, and quota. The reservation
   is `min(per-file limit, session bytes remaining)`. That's the most this
   upload can write, and it's held as `reservedBytes`.
3. **Streaming**: the body goes through `io.LimitReader(body, limit+1)` into
   a fresh `.partial-<random>` file (mode 0600, opened through `os.Root`).
   Reading `limit+1` bytes means the upload is too big, giving 413. Before
   each read the connection's read deadline moves forward by 60 seconds, so
   a dead sender is cut off but a slow one isn't. When the session ends,
   the read deadline is set to now, which unblocks the read straight away.
4. **Syncing**: `fsync` the partial file, then close it.
5. **Committing**: claim the name in an in-memory set keyed by case-folded
   NFC (so `A.txt` and `a.txt` collide everywhere), then hard-link the
   partial file to the final name. `link(2)` and `CreateHardLink` both fail
   atomically if the target exists, so an existing file is never replaced,
   even when two commits race. Then remove the partial file and `fsync` the
   directory. On filesystems without hard links (FAT, exFAT, some network
   shares) it falls back to `O_EXCL` create plus copy. That's still
   no-replace, but the file is visible while it's being copied.
6. **Accounting**: `Ticket.Commit(n)` turns the reservation into
   `completedBytes += n` (the actual size). Every other way out goes
   through `defer ticket.Abort()`, which releases the reservation and the
   slot. Both are idempotent and run under the same mutex. Committed bytes
   are never refunded.

After a failure the handler sets `Connection: close` and expires the read
deadline. Otherwise net/http tries to drain the rest of the body, and a
stalled sender would hang that drain.

## Filenames

`storage.CleanName` rejects rather than repairs. It refuses empty names,
invalid UTF-8 (overlong encodings included), names over 255 bytes, `/` and
`\`, C0/C1 controls, `< > : " | ? *`, bidi and zero-width formatting
characters, `.` and `..`, the `.partial-` prefix, leading spaces, trailing
spaces or dots, and Windows device names (`CON`, `nul.txt`, `COM1`, ...). The
rules are the same on every OS, so the result doesn't depend on where the
receiver runs. Accepted names are NFC-normalised.

## Shutdown

`signal.NotifyContext` (SIGINT/SIGTERM) is the parent of the session's
`context.WithDeadline`. When either fires:

1. The session is marked closed and any active upload's context is
   cancelled.
2. Signal handling is reset, so a second Ctrl+C kills the process at once.
   SIGPIPE is ignored for the whole run: in `deaddrop receive --json | jq`,
   Ctrl+C also stops `jq`, and without this the next write to stdout would
   kill deaddrop before it removed in-progress uploads.
3. `http.Server.Shutdown` waits up to 10 seconds for handlers to finish.
4. Any partial files left over are removed.
5. `cloudflared` gets SIGTERM to its process group (`taskkill /T` by PID on
   Windows), then SIGKILL after 5 seconds.
6. The session folder is removed if it's empty, and the summary counts only
   committed files.

## Recovery

Every run gets a new folder named `<timestamp>-<random>`. At startup Dead
Drop looks one level down in the base directory for `.partial-*` files and
reports how many there are and how big. It never deletes them, because a
concurrent `deaddrop` could own them. A `deaddrop clean` command may come
later.
