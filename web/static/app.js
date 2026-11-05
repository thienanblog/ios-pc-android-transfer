'use strict';

// A 2x2 HEIC picture. If this browser can show it, it can show HEIC photos.
const HEIC_PROBE = 'data:image/heic;base64,AAAAHGZ0eXBoZWljAAAAAG1pZjFoZWljbWlhZgAAAXxtZXRhAAAAAAAAACFoZGxyAAAAAAAAAABwaWN0AAAAAAAAAAAAAAAAAAAAACJpbG9jAAAAAERAAAEAAQAAAAABoAABAAAAAAAAACcAAAAjaWluZgAAAAAAAQAAABVpbmZlAgAAAAABAABodmMxAAAAAA5waXRtAAAAAAABAAAA/GlwcnAAAADcaXBjbwAAAHVodmNDAQNwAAAAkAAAAAAAHvAA/P34+AAADwNgAAEAGEABDAH//wNwAAADAJAAAAMAAAMAHroCQGEAAQApQgEBA3AAAAMAkAAAAwAAAwAeoCCBBZbqrprm4CGgwIAAAAyAAAADAIRiAAEABkQBwXPBiQAAABNjb2xybmNseAABAA0ABoAAAAAUaXNwZQAAAAAAAABAAAAAQAAAAChjbGFwAAAAAgAAAAEAAAACAAAAAf///8IAAAAC////wgAAAAIAAAAQcGl4aQAAAAADCAgIAAAAGGlwbWEAAAAAAAAAAQABBYECAwWEAAAAL21kYXQAAAAjKAGvBBITjOD46cJoP91R/FqVtJ7yftCrK+NJn3exGJKp/6A=';
const HEVC_TYPES = ['video/mp4; codecs="hvc1.1.6.L93.B0"', 'video/mp4; codecs="hev1.1.6.L93.B0"'];
const CHUNK = 8 * 1024 * 1024;
const NATIVE_IMAGES = new Set(['jpeg', 'png', 'gif', 'webp']);

const $ = (s, el = document) => el.querySelector(s);
const state = {
  info: null,
  caps: { heic: false, hevc: false },
  files: [],
  listSig: '',
  selected: new Set(),
  busy: new Map(), // file name -> status text while converting/downloading
};

const isIOS = /iP(hone|ad|od)/.test(navigator.userAgent) ||
  (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch (_) { /* private mode */ }
  return null;
}

async function api(path, opts = {}) {
  const headers = Object.assign({ 'X-PhotoBridge': '1' }, opts.headers || {});
  const res = await fetch(path, Object.assign({ credentials: 'same-origin' }, opts, { headers }));
  let body = null;
  try { body = await res.json(); } catch (_) { /* not json */ }
  if (res.status === 401) setConn('bad', 'Mất kết nối, hãy quét lại mã QR');
  return { res, body: body || {} };
}

function setConn(kind, text) {
  const c = $('#conn');
  c.className = 'chip ' + kind;
  c.textContent = text;
}

function fmtSize(n) {
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + ' ' + u[i];
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- What can this device open? ----------

function detectHEIC() {
  return new Promise((resolve) => {
    const img = new Image();
    const t = setTimeout(() => resolve(false), 3000);
    img.onload = () => { clearTimeout(t); resolve(img.naturalWidth > 0); };
    img.onerror = () => { clearTimeout(t); resolve(false); };
    img.src = HEIC_PROBE;
  });
}

async function detectHEVC() {
  const v = document.createElement('video');
  let ok = HEVC_TYPES.some((t) => v.canPlayType(t) === 'probably');
  if (!ok && navigator.mediaCapabilities && navigator.mediaCapabilities.decodingInfo) {
    try {
      const r = await navigator.mediaCapabilities.decodingInfo({
        type: 'file',
        video: { contentType: HEVC_TYPES[0], width: 1920, height: 1080, bitrate: 8000000, framerate: 30 },
      });
      ok = !!r.supported;
    } catch (_) { /* unsupported query */ }
  }
  return ok;
}

function renderCaps() {
  const yes = (b) => (b ? 'có' : 'không');
  $('#caps').textContent = `Trình duyệt này mở được ảnh HEIC: ${yes(state.caps.heic)} · video HEVC: ${yes(state.caps.hevc)}`;
}

// ---------- Start ----------

async function init() {
  const mode = $('#mode');
  const imgFmt = $('#img-format');
  mode.value = store('pb.mode') || 'auto';
  imgFmt.value = store('pb.imgFormat') || 'jpg';
  mode.addEventListener('change', () => store('pb.mode', mode.value));
  imgFmt.addEventListener('change', () => store('pb.imgFormat', imgFmt.value));

  if (isIOS) document.querySelectorAll('.ios-only').forEach((el) => { el.hidden = false; });

  const [heic, hevc] = await Promise.all([detectHEIC(), detectHEVC()]);
  state.caps = { heic, hevc };
  renderCaps();

  const { res, body } = await api('/api/info');
  if (!res.ok) return;
  state.info = body;
  setConn('ok', 'Đã kết nối');
  $('#version').textContent = body.version || '';
  if (body.isLocal) setupPC(body);

  setupUpload();
  setupList();
  await refresh();
  setInterval(() => { if (!document.hidden) refresh(); }, 4000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
}

function setupPC(info) {
  $('#pc-panel').hidden = false;
  document.querySelectorAll('.pc-only').forEach((el) => { el.hidden = false; });
  $('#data-dir').textContent = info.dataDir || '';
  const urls = info.urls || [];
  const show = (i) => {
    $('#qr').src = '/api/qr?i=' + i;
    // Show the address without the access key; the key only lives in the QR.
    $('#url-text').textContent = urls[i] ? urls[i].split('?')[0] : '';
  };
  if (urls.length === 0) {
    $('#url-text').textContent = 'Không tìm thấy địa chỉ mạng nội bộ. Máy tính đã kết nối Wi-Fi hoặc mạng dây chưa?';
    $('#qr').hidden = true;
  } else {
    show(0);
    if (urls.length > 1) {
      const pick = $('#url-pick');
      urls.forEach((u, i) => pick.add(new Option(u.split('?')[0], String(i))));
      pick.addEventListener('change', () => show(Number(pick.value)));
      $('#url-pick-wrap').hidden = false;
    }
  }
  const t = info.tools || {};
  const warn = [];
  if (!t.images) {
    warn.push('Chưa có công cụ đổi ảnh HEIC sang JPG/PNG. Cài <b>ffmpeg 7.1 trở lên</b> hoặc <b>ImageMagick</b> (xem README), rồi mở lại PhotoBridge.');
  }
  if (!t.videos) {
    warn.push('Chưa có <b>ffmpeg</b> nên chưa đổi được video HEVC sang MP4 H.264. File gốc vẫn gửi và nhận bình thường.');
  }
  if (warn.length) {
    $('#tool-warn').innerHTML = warn.join('<br>');
    $('#tool-warn').hidden = false;
  }
}

// ---------- Sending files to the computer ----------

function setupUpload() {
  const pick = $('#pick');
  pick.addEventListener('change', () => {
    const files = Array.from(pick.files || []);
    pick.value = '';
    enqueue(files);
  });
  const drop = $('#drop');
  ['dragenter', 'dragover'].forEach((ev) => drop.addEventListener(ev, (e) => {
    e.preventDefault();
    drop.classList.add('over');
  }));
  ['dragleave', 'drop'].forEach((ev) => drop.addEventListener(ev, () => drop.classList.remove('over')));
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    enqueue(Array.from(e.dataTransfer.files || []));
  });
  window.addEventListener('beforeunload', (e) => {
    if (uploadQueue.length || uploading) { e.preventDefault(); e.returnValue = ''; }
  });
}

const uploadQueue = [];
let uploading = false;

function enqueue(files) {
  for (const file of files) {
    const li = document.createElement('li');
    li.innerHTML = '<div class="top-line"><span class="qname"></span><span class="qstate muted">Đang chờ</span></div><progress max="1" value="0"></progress>';
    $('.qname', li).textContent = file.name;
    $('#queue').prepend(li);
    uploadQueue.push({ file, li });
  }
  pump();
}

async function pump() {
  if (uploading) return;
  uploading = true;
  while (uploadQueue.length) {
    const item = uploadQueue.shift();
    const st = $('.qstate', item.li);
    const bar = $('progress', item.li);
    try {
      st.textContent = 'Đang gửi…';
      const saved = await uploadFile(item.file, (f) => {
        bar.value = f;
        st.textContent = Math.floor(f * 100) + '%';
      });
      bar.value = 1;
      st.className = 'qstate';
      st.innerHTML = '';
      st.append(...formatBadges(saved, true));
      refresh();
    } catch (err) {
      st.className = 'qstate status err';
      st.textContent = 'Lỗi: ' + (err && err.message ? err.message : err);
    }
  }
  uploading = false;
}

async function uploadFile(file, onProgress) {
  const { res, body } = await api('/api/uploads', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: file.name, size: file.size, lastModified: file.lastModified || 0 }),
  });
  if (!res.ok) throw new Error(body.error || res.status);
  if (body.done) return body.file;
  const id = body.id;
  let offset = body.offset;
  let failures = 0;
  onProgress(file.size ? offset / file.size : 1);

  while (offset < file.size) {
    const end = Math.min(offset + CHUNK, file.size);
    try {
      const r = await api(`/api/uploads/${id}?offset=${offset}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/octet-stream' },
        body: file.slice(offset, end),
      });
      if (r.res.status === 409 || (r.res.status === 400 && typeof r.body.offset === 'number')) {
        offset = r.body.offset;
        failures++;
        if (failures > 10) throw new Error('mạng chập chờn, thử lại sau');
        continue;
      }
      if (!r.res.ok) throw new Error(r.body.error || r.res.status);
      offset = r.body.offset;
      failures = 0;
      onProgress(offset / file.size);
      if (r.body.done) return r.body.file;
    } catch (err) {
      failures++;
      if (failures > 10) throw err;
      await sleep(Math.min(1000 * 2 ** failures, 15000));
      try {
        const s = await api(`/api/uploads/${id}`);
        if (s.res.ok) offset = s.body.offset;
      } catch (_) { /* still offline, try again */ }
    }
  }
  throw new Error('không hoàn tất');
}

// ---------- Files on the computer ----------

function formatBadges(f, afterUpload) {
  const out = [];
  const add = (text, cls) => {
    const b = document.createElement('span');
    b.className = 'badge ' + (cls || '');
    b.textContent = text;
    out.push(b);
  };
  if (f.kind === 'image') {
    if (f.format === 'heic') add(afterUpload ? 'HEIC gốc' : 'HEIC', afterUpload ? 'ok' : '');
    else add(f.format.toUpperCase(), afterUpload && isIOS && f.format === 'jpeg' ? 'warn' : '');
  } else if (f.kind === 'video') {
    const codec = f.videoCodec === 'hevc' ? 'HEVC' : f.videoCodec === 'h264' ? 'H.264' : 'video';
    add(codec, afterUpload && f.videoCodec === 'hevc' ? 'ok' : '');
    add(f.format.toUpperCase());
  } else {
    add('file');
  }
  return out;
}

// conversionFor decides what the file should become on this device, or null for the original.
function conversionFor(f) {
  const mode = $('#mode').value;
  if (mode === 'original') return null;
  const force = mode === 'compat';
  const img = $('#img-format').value;
  if (f.kind === 'image') {
    if (f.format === 'heic' && (force || !state.caps.heic)) return img;
    if (f.format === 'avif' && force) return img;
  }
  if (f.kind === 'video') {
    if (f.videoCodec === 'hevc' && (force || !state.caps.hevc)) return 'mp4';
    if (force && f.format === 'mov') return 'mp4';
  }
  return null;
}

function setupList() {
  $('#select-all').addEventListener('click', () => {
    const all = state.selected.size < state.files.length;
    state.selected = new Set(all ? state.files.map((f) => f.name) : []);
    render();
  });
  $('#download-selected').addEventListener('click', async () => {
    const names = state.files.filter((f) => state.selected.has(f.name));
    for (const f of names) {
      await download(f);
      await sleep(700); // browsers block bursts of downloads
    }
  });
  $('#delete-selected').addEventListener('click', async () => {
    const names = [...state.selected];
    if (!names.length || !confirm(`Xoá ${names.length} file khỏi máy tính?`)) return;
    for (const n of names) await api('/api/files/' + encodeURIComponent(n), { method: 'DELETE' });
    state.selected.clear();
    refresh();
  });
}

async function refresh() {
  const { res, body } = await api('/api/files').catch(() => ({ res: { ok: false }, body: {} }));
  if (!res.ok) {
    if (res.status !== 401) setConn('bad', 'Mất kết nối với máy tính');
    return;
  }
  setConn('ok', 'Đã kết nối');
  const files = body.files || [];
  const sig = JSON.stringify(files);
  if (sig === state.listSig) return;
  state.listSig = sig;
  state.files = files;
  const names = new Set(files.map((f) => f.name));
  state.selected.forEach((n) => { if (!names.has(n)) state.selected.delete(n); });
  render();
}

function render() {
  const list = $('#files');
  const tpl = $('#file-tpl');
  const existing = new Map([...list.children].map((li) => [li.dataset.key, li]));
  const keep = new Set();
  let prev = null;

  for (const f of state.files) {
    const key = `${f.name}|${f.size}|${f.modified}`;
    keep.add(key);
    let li = existing.get(key);
    if (!li) {
      li = tpl.content.firstElementChild.cloneNode(true);
      li.dataset.key = key;
      fillCard(li, f);
    }
    const sel = $('.sel', li);
    sel.checked = state.selected.has(f.name);
    li.classList.toggle('selected', sel.checked);
    const busy = state.busy.get(f.name);
    const status = $('.status', li);
    status.hidden = !busy;
    if (busy) { status.textContent = busy.text; status.classList.toggle('err', !!busy.error); }
    // Keep DOM order equal to list order without re-creating cards.
    const want = prev ? prev.nextSibling : list.firstChild;
    if (want !== li) list.insertBefore(li, want);
    prev = li;
  }
  existing.forEach((li, key) => { if (!keep.has(key)) li.remove(); });

  $('#empty').hidden = state.files.length > 0;
  $('#count').textContent = state.files.length ? `${state.files.length} file` : '';
  $('#download-selected').disabled = state.selected.size === 0;
  $('#delete-selected').disabled = state.selected.size === 0;
  $('#download-selected').textContent = state.selected.size ? `Tải về ${state.selected.size} mục đã chọn` : 'Tải về các mục đã chọn';
  $('#select-all').textContent = state.selected.size && state.selected.size === state.files.length ? 'Bỏ chọn' : 'Chọn tất cả';
}

function fillCard(li, f) {
  const enc = encodeURIComponent(f.name);
  $('.name', li).textContent = f.name;
  $('.name', li).title = f.name;
  $('.size', li).textContent = fmtSize(f.size);
  $('.badges', li).append(...formatBadges(f, false));

  const img = $('img', li);
  const icon = $('.kind-icon', li);
  const original = `/api/files/${enc}?inline=1`;
  img.src = `/api/thumbs/${enc}?v=${f.modified}`;
  img.onerror = () => {
    const canShow = f.kind === 'image' && (NATIVE_IMAGES.has(f.format) || (f.format === 'heic' && state.caps.heic));
    if (canShow && img.dataset.fallback !== '1') {
      img.dataset.fallback = '1';
      img.src = original;
      return;
    }
    img.hidden = true;
    icon.hidden = false;
    icon.textContent = f.kind === 'video' ? '🎬 Video' : f.kind === 'image' ? '🖼️ ' + f.format.toUpperCase() : '📄 File';
  };

  $('.sel', li).addEventListener('change', (e) => {
    if (e.target.checked) state.selected.add(f.name); else state.selected.delete(f.name);
    render();
  });
  $('.dl', li).addEventListener('click', () => download(f));
  $('.view', li).addEventListener('click', () => view(f));
  const del = $('.del', li);
  if (!(state.info && state.info.isLocal)) del.hidden = true;
  del.addEventListener('click', async () => {
    if (!confirm(`Xoá "${f.name}" khỏi máy tính?`)) return;
    await api('/api/files/' + enc, { method: 'DELETE' });
    refresh();
  });
}

function setBusy(f, text, error) {
  if (text) state.busy.set(f.name, { text, error }); else state.busy.delete(f.name);
  render();
}

// prepare returns a URL for the file in the form this device needs,
// converting on the computer first when necessary.
async function prepare(f) {
  const to = conversionFor(f);
  if (!to) return { url: '/api/files/' + encodeURIComponent(f.name), converted: false };
  setBusy(f, 'Đang đổi sang ' + to.toUpperCase() + '…');
  for (;;) {
    const { res, body } = await api('/api/convert', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: f.name, to }),
    });
    if (!res.ok) throw new Error(body.error || res.status);
    if (body.state === 'ready') {
      setBusy(f, null);
      return { url: body.url, converted: true };
    }
    if (body.state === 'error') {
      const msg = body.error === 'ffmpeg-missing'
        ? 'Máy tính chưa cài ffmpeg nên chưa đổi được video.'
        : body.error === 'image-tool-missing'
          ? 'Máy tính chưa cài công cụ đổi ảnh HEIC.'
          : 'Không đổi được: ' + body.error;
      const err = new Error(msg);
      err.canFallback = true;
      throw err;
    }
    const pct = body.state === 'working' && body.progress > 0 ? ' ' + Math.floor(body.progress * 100) + '%' : '';
    setBusy(f, (body.state === 'queued' ? 'Đang chờ đổi định dạng…' : 'Đang đổi sang ' + to.toUpperCase()) + pct);
    await sleep(1000);
  }
}

function triggerDownload(url) {
  const a = document.createElement('a');
  a.href = url;
  a.download = '';
  a.rel = 'noopener';
  document.body.append(a);
  a.click();
  a.remove();
}

async function download(f) {
  try {
    const { url } = await prepare(f);
    triggerDownload(url);
  } catch (err) {
    setBusy(f, err.message + (err.canFallback ? ' Đang tải file gốc.' : ''), true);
    if (err.canFallback) triggerDownload('/api/files/' + encodeURIComponent(f.name));
  }
}

async function view(f) {
  // Open the tab first so pop-up blockers allow it, then point it at the file.
  const win = window.open('about:blank', '_blank');
  try {
    const { url } = await prepare(f);
    const inline = url + (url.includes('?') ? '&' : '?') + 'inline=1';
    if (win) win.location.href = inline; else location.href = inline;
  } catch (err) {
    if (win) win.close();
    setBusy(f, err.message, true);
  }
}

init().catch((err) => setConn('bad', 'Lỗi: ' + err.message));
