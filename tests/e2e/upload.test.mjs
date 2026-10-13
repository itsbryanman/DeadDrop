// Browser end-to-end tests. Build the binary first (make build), then:
//   cd tests/e2e && npm ci && npx playwright install chromium && npm test
// DEADDROP_BIN overrides the binary path.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { mkdtempSync, writeFileSync, readFileSync, readdirSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = dirname(fileURLToPath(import.meta.url));
const bin = process.env.DEADDROP_BIN || resolve(here, '../../bin/deaddrop');

let browser;
before(async () => { browser = await chromium.launch(); });
after(async () => { await browser?.close(); });

function startDrop(...extra) {
  const dir = mkdtempSync(join(tmpdir(), 'deaddrop-e2e-'));
  const child = spawn(bin, ['receive', '--bind', '127.0.0.1', '--port', '0', '--dir', dir,
    '--json', '--json-include-secret', ...extra], { stdio: ['ignore', 'pipe', 'inherit'] });
  const events = [];
  const waiters = [];
  createInterface({ input: child.stdout }).on('line', (line) => {
    let ev;
    try { ev = JSON.parse(line); } catch { return; }
    events.push(ev);
    for (const w of [...waiters]) if (w.match(ev)) { waiters.splice(waiters.indexOf(w), 1); w.resolve(ev); }
  });
  const waitFor = (name) => new Promise((resolveEv, reject) => {
    const hit = events.find((e) => e.event === name);
    if (hit) return resolveEv(hit);
    const timer = setTimeout(() => reject(new Error(`timed out waiting for ${name}`)), 15000);
    waiters.push({ match: (e) => e.event === name, resolve: (e) => { clearTimeout(timer); resolveEv(e); } });
  });
  const stop = () => new Promise((res) => {
    if (child.exitCode !== null) return res(child.exitCode);
    child.once('exit', (code) => res(code));
    child.kill('SIGINT');
  });
  return { dir, child, events, waitFor, stop };
}

function fixture(name, bytes) {
  const d = mkdtempSync(join(tmpdir(), 'deaddrop-fixture-'));
  const p = join(d, name);
  writeFileSync(p, bytes);
  return p;
}

function sessionDir(dir) {
  const entries = readdirSync(dir);
  assert.equal(entries.length, 1);
  return join(dir, entries[0]);
}

test('uploads files and strips the key from the address bar', async () => {
  const drop = startDrop('--name', 'E2E receiver');
  try {
    const { link } = await drop.waitFor('session_started');
    const token = link.split('#')[1];
    const page = await browser.newPage();
    await page.goto(link);
    await page.waitForSelector('#view-ready:not([hidden])');

    assert.ok(!page.url().includes('#'), 'fragment should be removed');
    assert.ok(!(await page.content()).includes(token), 'token must not be in the DOM');
    assert.equal(await page.textContent('#session-name-text'), 'E2E receiver');

    const a = fixture('photo one.jpg', Buffer.alloc(300 * 1024, 7));
    const b = fixture('notes.txt', 'hello from the browser\n');
    await page.setInputFiles('#file-input', [a, b]);
    await page.waitForFunction(() => document.querySelectorAll('.item[data-state="saved"]').length === 2);

    const saved = sessionDir(drop.dir);
    assert.deepEqual(readdirSync(saved).sort(), ['notes.txt', 'photo one.jpg']);
    assert.ok(readFileSync(join(saved, 'photo one.jpg')).equals(readFileSync(a)));
    assert.equal(await page.textContent('#files-left'), '18');

    // Same name again is refused, never overwritten.
    await page.setInputFiles('#file-input', [b]);
    await page.waitForSelector('.item[data-state="rejected"]');
    assert.match(await page.textContent('.item[data-state="rejected"] .item-status'), /already received/);
    await page.close();
  } finally {
    assert.equal(await drop.stop(), 0);
  }
});

test('explains a missing or wrong key', async () => {
  const drop = startDrop();
  try {
    const { link } = await drop.waitFor('session_started');
    const base = link.split('#')[0];

    let page = await browser.newPage();
    await page.goto(base);
    await page.waitForSelector('#view-blocked:not([hidden])');
    assert.match(await page.textContent('#blocked-title'), /missing its access key/);
    await page.close();

    page = await browser.newPage();
    await page.goto(base + '#' + 'A'.repeat(22));
    await page.waitForSelector('#view-blocked:not([hidden])');
    assert.match(await page.textContent('#blocked-title'), /doesn.t work/);
    await page.close();
  } finally {
    await drop.stop();
  }
});

test('rejects oversized files before sending them', async () => {
  const drop = startDrop('--max-file', '1KiB');
  try {
    const { link } = await drop.waitFor('session_started');
    const page = await browser.newPage();
    await page.goto(link);
    await page.waitForSelector('#view-ready:not([hidden])');
    await page.setInputFiles('#file-input', [fixture('big.bin', Buffer.alloc(4096))]);
    await page.waitForSelector('.item[data-state="rejected"]');
    assert.match(await page.textContent('.item-status'), /Too large/);
    assert.ok(!drop.events.some((e) => e.event === 'upload_started'), 'nothing should reach the server');
    await page.close();
  } finally {
    await drop.stop();
  }
});

test('shows when the drop has closed', async () => {
  const drop = startDrop('--expires', '3s');
  try {
    const { link } = await drop.waitFor('session_started');
    const page = await browser.newPage();
    await page.goto(link);
    await page.waitForSelector('#view-ready:not([hidden])');
    await page.waitForFunction(() => document.querySelector('#expires').textContent === 'closed', null, { timeout: 10000 });
    assert.equal(await page.isDisabled('#choose'), true);
    await page.close();
  } finally {
    await drop.stop();
  }
});

test('is keyboard reachable and fits a phone screen', async () => {
  const drop = startDrop();
  try {
    const { link } = await drop.waitFor('session_started');
    const ctx = await browser.newContext({ viewport: { width: 360, height: 740 }, isMobile: true, hasTouch: true });
    const page = await ctx.newPage();
    await page.goto(link);
    await page.waitForSelector('#view-ready:not([hidden])');
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
    assert.equal(overflow, false, 'no horizontal scroll');
    await page.keyboard.press('Tab');
    assert.equal(await page.evaluate(() => document.activeElement?.id), 'choose');
    await ctx.close();
  } finally {
    await drop.stop();
  }
});
