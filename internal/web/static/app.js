(function () {
  'use strict';

  var MAX_QUEUE = 200;
  var BUSY_RETRIES = 100;
  var POLL_MS = 15000;
  var MAX_NAME_BYTES = 255;

  // The access key travels in the URL fragment so it never reaches the
  // server in a request line, logs or Referer. Read it once, keep it only in
  // memory, and drop it from the address bar right away.
  var fragment = location.hash.length > 1 ? location.hash.slice(1) : '';
  if (location.hash) {
    try { history.replaceState(null, '', location.pathname); } catch (e) { /* ignore */ }
  }
  var token = /^[A-Za-z0-9_-]{22}$/.test(fragment) ? fragment : '';
  var hadFragment = fragment !== '';
  fragment = '';

  var $ = function (id) { return document.getElementById(id); };
  var els = {
    loading: $('view-loading'),
    blocked: $('view-blocked'),
    blockedTitle: $('blocked-title'),
    blockedDetail: $('blocked-detail'),
    ready: $('view-ready'),
    sessionName: $('session-name'),
    sessionNameText: $('session-name-text'),
    dropzone: $('dropzone'),
    choose: $('choose'),
    input: $('file-input'),
    limits: $('limits'),
    expires: $('expires'),
    filesLeft: $('files-left'),
    spaceLeft: $('space-left'),
    queueSection: $('queue-section'),
    queue: $('queue'),
    announcer: $('announcer')
  };

  var session = null;      // last /api/session response
  var clockSkew = 0;       // serverTime - local time, ms
  var closed = false;
  var items = [];
  var active = null;
  var nextId = 1;

  // ---------- formatting ----------

  function formatSize(n) {
    var units = [['TiB', 1099511627776], ['GiB', 1073741824], ['MiB', 1048576], ['KiB', 1024]];
    for (var i = 0; i < units.length; i++) {
      var u = units[i];
      if (n >= u[1]) {
        var v = n / u[1];
        var s = (n % u[1] === 0) ? String(v) : (v >= 100 ? v.toFixed(0) : v.toFixed(1));
        return s + ' ' + u[0];
      }
    }
    return n + ' B';
  }

  function formatRemaining(ms) {
    if (ms <= 0) return 'closed';
    var s = Math.floor(ms / 1000);
    var h = Math.floor(s / 3600);
    var m = Math.floor((s % 3600) / 60);
    var sec = s % 60;
    var pad = function (x) { return (x < 10 ? '0' : '') + x; };
    return h > 0 ? h + ':' + pad(m) + ':' + pad(sec) : m + ':' + pad(sec);
  }

  function announce(text) {
    els.announcer.textContent = '';
    window.setTimeout(function () { els.announcer.textContent = text; }, 30);
  }

  // ---------- views ----------

  function show(view) {
    [els.loading, els.blocked, els.ready].forEach(function (v) { v.hidden = v !== view; });
    els.loading.setAttribute('aria-busy', view === els.loading ? 'true' : 'false');
  }

  function block(title, detail) {
    closed = true;
    els.blockedTitle.textContent = title;
    els.blockedDetail.textContent = detail;
    show(els.blocked);
    els.blockedTitle.focus();
  }

  function closeDrop(message) {
    if (closed) return;
    closed = true;
    items.forEach(function (it) {
      if (it.state === 'queued' || it.state === 'waiting') setState(it, 'failed', 'Not sent: the drop closed.');
    });
    setDisabled(true);
    els.expires.textContent = 'closed';
    els.limits.textContent = message;
    announce(message);
  }

  function setDisabled(disabled) {
    els.choose.disabled = disabled;
    els.input.disabled = disabled;
    els.dropzone.classList.toggle('is-disabled', disabled);
  }

  // ---------- session ----------

  function api(path) {
    return fetch(path, {
      headers: { 'Authorization': 'Bearer ' + token },
      cache: 'no-store',
      credentials: 'omit',
      referrerPolicy: 'no-referrer'
    });
  }

  function loadSession(initial) {
    return api('/api/session').then(function (res) {
      if (res.status === 401) {
        block('This link doesn’t work', 'Its access key is wrong or has been replaced. Ask the person who sent it for a new link.');
        return;
      }
      if (res.status === 403) {
        if (initial) block('This drop has closed', 'The receiver ended the session or it expired. Ask them to start a new one.');
        else closeDrop('This drop has closed.');
        return;
      }
      if (!res.ok) throw new Error('status ' + res.status);
      return res.json().then(function (s) {
        session = s;
        clockSkew = Date.parse(s.serverTime) - Date.now();
        render();
        if (initial) {
          show(els.ready);
        }
      });
    }).catch(function () {
      if (initial) {
        block('Can’t reach the receiver', 'Make sure you’re on the same network as the computer that shared this link, then reload the original link.');
      }
    });
  }

  function remaining() {
    if (!session) return { files: 0, bytes: 0 };
    return {
      files: Math.max(0, session.maxUploads - session.receivedFiles),
      bytes: Math.max(0, session.maxTotalBytes - session.receivedBytes)
    };
  }

  function render() {
    if (!session) return;
    if (session.name) {
      els.sessionNameText.textContent = session.name;
      els.sessionName.hidden = false;
    }
    var r = remaining();
    els.filesLeft.textContent = String(r.files);
    els.spaceLeft.textContent = formatSize(r.bytes);
    els.filesLeft.classList.toggle('is-low', r.files === 0);
    els.spaceLeft.classList.toggle('is-low', r.bytes === 0);
    if (!closed) {
      els.limits.textContent = 'Up to ' + formatSize(session.maxFileBytes) + ' per file · one at a time';
    }
    tick();
  }

  function tick() {
    if (!session || closed) return;
    var ms = Date.parse(session.expiresAt) - (Date.now() + clockSkew);
    els.expires.textContent = formatRemaining(ms);
    els.expires.classList.toggle('is-low', ms < 60000);
    if (ms <= 0) closeDrop('This drop has closed.');
  }

  // ---------- queue ----------

  function utf8Length(s) {
    return new Blob([s]).size;
  }

  // Mirrors the server's checks so obviously bad names fail before upload.
  // The server remains the authority.
  function nameProblem(name) {
    if (!name) return 'This file has no name.';
    if (/[\/\\]/.test(name)) return 'File names can’t contain slashes.';
    if (/[\u0000-\u001f\u007f-\u009f]/.test(name)) return 'This file name has hidden control characters. Rename it and try again.';
    if (/[<>:"|?*]/.test(name)) return 'File names can’t contain < > : " | ? or *. Rename it and try again.';
    if (/[​-‏‪-‮⁦-⁩؜﻿]/.test(name)) return 'This file name has invisible formatting characters. Rename it and try again.';
    if (name === '.' || name === '..' || name.indexOf('.partial-') === 0) return 'This file name is reserved. Rename it and try again.';
    if (/[ .]$/.test(name) || /^ /.test(name)) return 'File names can’t start with a space or end with a space or dot.';
    if (utf8Length(name.normalize ? name.normalize('NFC') : name) > MAX_NAME_BYTES) return 'This file name is too long.';
    return '';
  }

  function sizeProblem(file) {
    if (!session) return '';
    var r = remaining();
    var pending = 0;
    items.forEach(function (it) {
      if (it.state === 'queued' || it.state === 'waiting' || it.state === 'uploading') pending += it.file.size;
    });
    if (file.size > session.maxFileBytes) return 'Too large. Files can be up to ' + formatSize(session.maxFileBytes) + '.';
    if (file.size > r.bytes - pending) return 'Not enough room left in this drop.';
    return '';
  }

  function countPending() {
    return items.filter(function (it) {
      return it.state === 'queued' || it.state === 'waiting' || it.state === 'uploading';
    }).length;
  }

  function addFiles(files, folderNames) {
    if (closed || !session) return;
    els.queueSection.hidden = false;
    (folderNames || []).forEach(function (name) {
      addItem({ name: name, size: 0 }, 'rejected', 'Folders can’t be sent. Zip the folder and send the .zip instead.');
    });
    var added = 0;
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      if (items.length >= MAX_QUEUE) {
        announce('Too many files at once. Send the rest in a second batch.');
        break;
      }
      var problem = nameProblem(f.name) || sizeProblem(f);
      if (!problem && countPending() >= remaining().files) {
        problem = 'This drop can’t take any more files.';
      }
      addItem(f, problem ? 'rejected' : 'queued', problem || 'Waiting');
      if (!problem) added++;
    }
    if (added) announce(added === 1 ? '1 file queued.' : added + ' files queued.');
    pump();
  }

  function addItem(file, state, status) {
    var it = { id: nextId++, file: file, state: state, status: status, sent: 0, busyRetries: 0 };
    var li = document.createElement('li');
    li.className = 'item';
    li.innerHTML =
      '<span class="item-icon" aria-hidden="true"></span>' +
      '<div class="item-main"><div class="item-name"></div><div class="item-status"></div></div>' +
      '<span class="item-size"></span>' +
      '<progress max="100" value="0" hidden></progress>' +
      '<div class="item-actions" hidden><button type="button" class="link-button">Try again</button></div>';
    li.querySelector('.item-name').textContent = file.name;
    li.querySelector('.item-size').textContent = formatSize(file.size);
    li.querySelector('progress').setAttribute('aria-label', 'Upload progress for ' + file.name);
    li.querySelector('.link-button').addEventListener('click', function () { retry(it); });
    it.el = li;
    items.push(it);
    els.queue.appendChild(li);
    setState(it, state, status);
    return it;
  }

  var ICONS = { queued: '·', waiting: '…', uploading: '↑', saved: '✓', failed: '!', rejected: '×' };

  function setState(it, state, status) {
    it.state = state;
    it.status = status;
    var li = it.el;
    li.setAttribute('data-state', state);
    li.querySelector('.item-icon').textContent = ICONS[state] || '';
    li.querySelector('.item-status').textContent = status;
    var bar = li.querySelector('progress');
    bar.hidden = state !== 'uploading';
    li.querySelector('.item-actions').hidden = !(state === 'failed' && it.retryable && !closed);
  }

  function retry(it) {
    if (closed) return;
    var problem = sizeProblem(it.file);
    if (problem) {
      setState(it, 'rejected', problem);
      return;
    }
    it.retryable = false;
    it.busyRetries = 0;
    setState(it, 'queued', 'Waiting');
    pump();
  }

  function pump() {
    if (active || closed) return;
    for (var i = 0; i < items.length; i++) {
      if (items[i].state === 'queued') {
        upload(items[i]);
        return;
      }
    }
  }

  function parseError(xhr) {
    try {
      var body = JSON.parse(xhr.responseText);
      if (body && body.message) return body;
    } catch (e) { /* fall through */ }
    return { error: 'unknown', message: 'Something went wrong (' + xhr.status + ').' };
  }

  function upload(it) {
    active = it;
    var file = it.file;
    var xhr = new XMLHttpRequest();
    it.xhr = xhr;
    setState(it, 'uploading', 'Sending… 0%');
    var bar = it.el.querySelector('progress');
    bar.value = 0;

    xhr.open('POST', '/api/upload');
    xhr.setRequestHeader('Authorization', 'Bearer ' + token);
    xhr.setRequestHeader('X-File-Name', encodeURIComponent(file.name.normalize ? file.name.normalize('NFC') : file.name));
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');

    xhr.upload.onprogress = function (e) {
      if (!e.lengthComputable || !e.total) return;
      var pct = Math.min(100, Math.floor((e.loaded / e.total) * 100));
      bar.value = pct;
      it.el.querySelector('.item-status').textContent = 'Sending… ' + pct + '%';
    };

    var finish = function () {
      active = null;
      it.xhr = null;
      pump();
    };

    xhr.onload = function () {
      var status = xhr.status;
      if (status === 201) {
        setState(it, 'saved', 'Saved on the receiver’s computer');
        announce(file.name + ' saved.');
        if (session) {
          session.receivedFiles += 1;
          session.receivedBytes += file.size;
          render();
        }
        finish();
        return;
      }
      var err = parseError(xhr);
      if (status === 429 && it.busyRetries < BUSY_RETRIES) {
        it.busyRetries++;
        var wait = parseInt(xhr.getResponseHeader('Retry-After'), 10);
        wait = isNaN(wait) ? 3 : Math.min(Math.max(wait, 1), 30);
        setState(it, 'waiting', 'Someone else is sending a file. Retrying shortly…');
        active = null;
        window.setTimeout(function () {
          if (it.state === 'waiting' && !closed) {
            setState(it, 'queued', 'Waiting');
            pump();
          }
        }, wait * 1000);
        return;
      }
      if (status === 401) {
        setState(it, 'failed', 'The access key was rejected.');
        finish();
        block('This link doesn’t work', 'Its access key is wrong or has been replaced. Ask the person who sent it for a new link.');
        return;
      }
      if (status === 403 && err.error === 'closed') {
        setState(it, 'failed', err.message);
        finish();
        closeDrop('This drop has closed.');
        return;
      }
      it.retryable = status === 408 || status === 400 && err.error === 'interrupted' || status >= 500;
      setState(it, status === 409 || status === 413 || status === 400 && err.error === 'bad_name' ? 'rejected' : 'failed', err.message);
      announce(file.name + ': ' + err.message);
      finish();
      loadSession(false);
    };

    xhr.onerror = function () {
      it.retryable = true;
      setState(it, 'failed', 'Connection lost. Check you’re still on the same network.');
      announce(file.name + ' failed: connection lost.');
      finish();
      loadSession(false);
    };

    xhr.onabort = function () {
      setState(it, 'failed', 'Cancelled.');
      finish();
    };

    xhr.send(file);
  }

  // ---------- input ----------

  function collectDrop(dt) {
    var files = [];
    var folders = [];
    if (dt.items && dt.items.length) {
      for (var i = 0; i < dt.items.length; i++) {
        var item = dt.items[i];
        if (item.kind !== 'file') continue;
        var entry = item.webkitGetAsEntry ? item.webkitGetAsEntry() : null;
        if (entry && entry.isDirectory) {
          folders.push(entry.name);
          continue;
        }
        var f = item.getAsFile();
        if (f) files.push(f);
      }
    } else if (dt.files) {
      files = Array.prototype.slice.call(dt.files);
    }
    return { files: files, folders: folders };
  }

  function wireInput() {
    var depth = 0;
    var dz = els.dropzone;

    els.choose.addEventListener('click', function (e) {
      e.stopPropagation();
      if (!closed) els.input.click();
    });
    dz.addEventListener('click', function () {
      if (!closed) els.input.click();
    });
    els.input.addEventListener('change', function () {
      addFiles(Array.prototype.slice.call(els.input.files || []));
      els.input.value = '';
    });

    var hasFiles = function (e) {
      var types = e.dataTransfer && e.dataTransfer.types;
      return types && Array.prototype.indexOf.call(types, 'Files') !== -1;
    };

    // Accept drops anywhere on the page so a near miss doesn't make the
    // browser navigate away to the dropped file.
    document.addEventListener('dragenter', function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth++;
      if (!closed) dz.classList.add('is-dragging');
    });
    document.addEventListener('dragleave', function (e) {
      if (!hasFiles(e)) return;
      depth = Math.max(0, depth - 1);
      if (depth === 0) dz.classList.remove('is-dragging');
    });
    document.addEventListener('dragover', function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = closed ? 'none' : 'copy';
    });
    document.addEventListener('drop', function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      dz.classList.remove('is-dragging');
      if (closed) return;
      var got = collectDrop(e.dataTransfer);
      addFiles(got.files, got.folders);
    });

    window.addEventListener('beforeunload', function (e) {
      if (active || items.some(function (it) { return it.state === 'queued' || it.state === 'waiting'; })) {
        e.preventDefault();
        e.returnValue = '';
      }
    });
  }

  // ---------- start ----------

  function start() {
    if (!token) {
      if (hadFragment) {
        block('This link is incomplete', 'The access key at the end of the link looks damaged. Copy the whole link again, or scan the QR code.');
      } else {
        block('This link is missing its access key', 'Open the full link you were sent, or scan the QR code on the receiver’s screen. This page’s address on its own won’t work.');
      }
      return;
    }
    wireInput();
    loadSession(true).then(function () {
      window.setInterval(tick, 1000);
      window.setInterval(function () { if (!closed) loadSession(false); }, POLL_MS);
    });
  }

  start();
})();
