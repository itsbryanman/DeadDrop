# Test plan

## Automated

`make test` / `make race` (CI runs these on Linux, macOS and Windows; the
`tests/integration` suite is Unix-only):

- **cli**: defaults, every flag, rejection of malformed values (decimal
  units, zero sizes, durations over 24h, non-IP `--bind`, LAN `--bind` with
  `--tunnel`, control characters in `--name`), help and version.
- **units**: size parsing and formatting.
- **session**: token encoding and constant-time check, rejection of
  malformed tokens, the single slot, reservation and refund on abort,
  committed bytes never refunded, unknown length when the quota is full,
  empty files, file-count cap, expiry vs. explicit close.
- **storage**: accepted and rejected filenames (traversal, separators,
  devices, controls, bidi, overlong UTF-8, length), collision keys,
  never-overwrite including case variants and files placed by someone else,
  16 concurrent commits to one name (exactly one wins), the copy fallback,
  cleanup on discard and close, stale scan reports without deleting,
  directory and file modes.
- **httpserver**: public shell leaks nothing, security headers on every
  route, 404 for unknown paths, 401 for every bad `Authorization` shape,
  happy path with non-ASCII names, empty file, bad names, 409 collision,
  413 declared and streamed (and exactly at the limit), quota across files,
  file-count cap, 429 + `Retry-After` while busy, client abort cleans up the
  partial and reservation, stalled body gets 408 within the stall timeout,
  session close cancels an in-flight upload, cross-origin rejection, tunnel
  origin allowed, `/healthz` refuses forwarded requests.
- **network**: picks the physical home network over Docker, bridges and VPNs,
  warns about alternatives, fails on public-only or link-local-only hosts.
- **terminal**: JSON output omits the secret unless asked, text events,
  QR output decodes back to the encoder's matrix in both colour modes.
- **tunnel** (with a fake `cloudflared`): URL extraction, waiting for the
  probe, early exit with its log, timeout, SIGTERM to the whole process
  group (a forked helper is gone too), SIGKILL when SIGTERM is ignored,
  missing binary.
- **integration** (real binary): exit codes, port in use (3), upload then
  Ctrl+C, Ctrl+C mid-upload leaves no partial and no empty folder, expiry,
  JSON without the secret.

`make e2e` (Playwright, Chromium):

- Key stripped from the address bar and absent from the DOM, two files
  uploaded and byte-compared on disk, duplicate rejected.
- Missing key and wrong key screens.
- Oversized file rejected in the browser without contacting the server.
- Expiry countdown disables the page.
- Keyboard focus reaches "Choose files"; no horizontal scroll at 360px.

## Manual, before tagging a release

Linux results below are from a run on an XFCE desktop with real Firefox
140 and Chromium 152 windows driven by mouse and keyboard, a dummy LAN
interface, and a bandwidth-limited proxy to slow transfers down.

| Check | Linux | macOS | Windows |
|---|---|---|---|
| `deaddrop receive` picks the right Wi-Fi/Ethernet address | ✓ (skips docker/libvirt/tailscale) | | |
| Phone on the same Wi-Fi scans the QR and uploads a photo | | | |
| iOS Safari and Android Chrome file pickers | | | |
| Drag and drop in Firefox, Safari, Chrome | ✓ Firefox, Chromium (from Thunar) | | |
| Dropping a folder shows the zip hint | ✓ | | |
| 2 GiB upload completes; progress is smooth | ✓ 1 GiB, byte-identical | | |
| Wi-Fi drops mid-upload: partial removed, retry works | | | |
| `--tunnel cloudflare` from a phone on mobile data | ✓ desktop browser over the tunnel | | |
| Ctrl+C during a tunnel session leaves no `cloudflared` behind | ✓ (unrelated cloudflared untouched) | | |
| `--dir` on an exFAT USB stick (copy fallback) | | | |
| Screen reader announces queued and saved files | | | |
| QR is readable on light and dark terminal themes | ✓ decodes from a raw xfce4-terminal screenshot | | |
| Two browsers at once: second waits, then sends | ✓ | | |
| Closing the tab mid-upload warns, then cleans up | ✓ | | |
| Ctrl+C mid-upload, including with stdout piped | ✓ | | |
| Pasting the link into a tab already on /r | ✓ | | |
| Keyboard only: Tab to "Choose files", Enter opens picker | ✓ | | |
