# Security

## What Dead Drop is for

Moving files from a browser to your own computer for a short time, on a
network you trust, or through Cloudflare when you accept what that means.
It's not an anonymity tool and it doesn't do end-to-end encryption.

## Assets

- The files being sent.
- The bearer token, which lets anyone who has it upload.
- Your disk: space, and the integrity of existing files.

## Threats and mitigations

| Threat | Mitigation | Residual risk |
|---|---|---|
| Someone on the LAN sniffs traffic | None in LAN mode. It's HTTP. | **They can read the files and the token.** Use trusted networks or the tunnel. |
| Someone guesses the token | 128-bit random token, constant-time compare | None in practice |
| Token leaks via Referer, server logs or proxies | Token is in the URL fragment, which browsers don't send. `Referrer-Policy: no-referrer`. Nothing token-related is logged. `--json` leaves the link out unless asked. | Browser history may keep the fragment before the script clears it. Screenshots and copied links keep it. |
| Token left visible on screen | Script removes the fragment with `replaceState` on load and keeps the token only in memory | The original link is wherever it was shared |
| Path traversal or tricky names | Strict `CleanName`, `os.Root` for partial files, no client paths ever interpreted | None known |
| Overwriting existing files | Hard-link commit fails atomically if the name exists; in-memory case/normalisation-insensitive set; tested under concurrent commits | Copy fallback on link-less filesystems exposes a partly written file during the copy |
| Filling the disk | Per-file and session byte caps, file count cap, bounded reader, single upload slot, advisory free-space check, ENOSPC handled | Other processes can still fill the disk |
| Slowloris and stalled bodies | 10s header timeout, 16 KiB header cap, 60s idle body deadline, 60s keep-alive idle | Up to one stalled upload at a time holds the slot for 60s |
| Cross-site request from another page | Bearer header can't be set cross-origin without CORS (none is offered); `Sec-Fetch-Site` and `Origin` checks | None known |
| Clickjacking | `frame-ancestors 'none'`, `X-Frame-Options: DENY` | None |
| XSS via filenames or title | Names rendered with `textContent` only; strict CSP (`script-src 'self'`, no inline) | None known |
| Malicious file content | Out of scope. Files aren't scanned or opened. | **Treat received files like any download.** |
| Impersonation via `--name` | The page labels it as set by the receiver and unverified | People may still trust it |
| Cloudflare sees traffic | Documented. TLS ends at Cloudflare's edge. | Cloudflare can see metadata and content |
| Killing someone else's `cloudflared` | Only the child we started is signalled, by process group or PID, never by name | None |
| `/healthz` reachable through the tunnel | Requests carrying `Cf-Connecting-Ip`, `Cf-Ray` or `X-Forwarded-For` get 404. It returns only `ok`. | None |

## What persists

No config, no accounts, no session state. Received files stay on disk, of
course. If the process is killed hard, `.partial-*` files can be left
behind. They're reported at next start and never deleted automatically.

## Reporting a vulnerability

Please open a GitHub security advisory on this repository rather than a
public issue.
