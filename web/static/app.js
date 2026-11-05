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
  dir: '',
  folders: [],
  files: [],
  listSig: '',
  selected: new Set(), // file paths
  busy: new Map(), // file path -> {text, error}
  viewIndex: -1,
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

const postJSON = (path, data) => api(path, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(data),
});

const encPath = (p) => p.split('/').map(encodeURIComponent).join('/');
const fileURL = (f, inline) => '/api/files/' + encPath(f.path) + (inline ? '?inline=1' : '');
const withInline = (url) => url + (url.includes('?') ? '&' : '?') + 'inline=1';

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

const canShowImage = (f) => NATIVE_IMAGES.has(f.format) || (f.format === 'heic' && state.caps.heic);
const canPlayVideo = (f) => f.videoCodec === 'h264' || (f.videoCodec === 'hevc' && state.caps.hevc);

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
  const yes = (b) => (b ? 'có' : 'không');
  $('#caps').textContent = `Trình duyệt này mở được ảnh HEIC: ${yes(heic)} · video HEVC: ${yes(hevc)}`;

  const { res, body } = await api('/api/info');
  if (!res.ok) return;
  state.info = body;
  setConn('ok', 'Đã kết nối');
  $('#version').textContent = body.version || '';
  if (body.isLocal) setupPC(body);

  setupUpload();
  setupList();
  setupViewer();
  state.dir = dirFromHash();
  window.addEventListener('hashchange', () => {
    const d = dirFromHash();
    if (d !== state.dir) openDir(d, false);
  });
  await openDir(state.dir, false);
  setInterval(() => { if (!document.hidden) refresh(); }, 4000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
}

function dirFromHash() {
  const h = location.hash.replace(/^#\/?/, '');
  try { return decodeURIComponent(h); } catch (_) { return ''; }
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
    warn.push('Chưa có công cụ đổi ảnh HEIC. Trình duyệt Chrome, Edge, Android sẽ không xem trước được ảnh HEIC. Cài <b>ffmpeg 7.1 trở lên</b> hoặc <b>ImageMagick</b> (xem README) rồi mở lại PhotoBridge.');
  }
  if (!t.videos) {
    warn.push('Chưa có <b>ffmpeg</b> nên chưa đổi được video HEVC sang MP4 H.264. File gốc vẫn gửi và nhận bình thường.');
  }
  if (warn.length) {
    $('#tool-warn').innerHTML = warn.join('<br>');
    $('#tool-warn').hidden = false;
  }
}

// ---------- Folders ----------

async function openDir(dir, push = true) {
  state.dir = dir;
  state.selected.clear();
  state.listSig = '';
  const h = dir ? '#/' + encodeURIComponent(dir) : '#/';
  if (push && location.hash !== h) location.hash = h;
  renderCrumbs();
  $('#upload-target').textContent = ['shared', ...(dir ? dir.split('/') : [])].join(' / ');
  $('#files').replaceChildren();
  await refresh();
}

function renderCrumbs() {
  const nav = $('#crumbs');
  nav.replaceChildren();
  const parts = state.dir ? state.dir.split('/') : [];
  const crumbs = [{ name: 'shared', path: '' }];
  parts.forEach((p, i) => crumbs.push({ name: p, path: parts.slice(0, i + 1).join('/') }));
  crumbs.forEach((c, i) => {
    if (i > 0) {
      const sep = document.createElement('span');
      sep.className = 'sep';
      sep.textContent = '›';
      nav.append(sep);
    }
    if (i === crumbs.length - 1) {
      const here = document.createElement('span');
      here.className = 'here';
      here.textContent = c.name;
      nav.append(here);
    } else {
      const b = document.createElement('button');
      b.textContent = c.name;
      b.addEventListener('click', () => openDir(c.path));
      nav.append(b);
    }
  });
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
    // Remember the folder at the moment the file was chosen.
    uploadQueue.push({ file, li, dir: state.dir });
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
      const saved = await uploadFile(item.file, item.dir, (f) => {
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

async function uploadFile(file, dir, onProgress) {
  const { res, body } = await postJSON('/api/uploads', {
    dir, name: file.name, size: file.size, lastModified: file.lastModified || 0,
  });
  if (!res.ok) throw new Error(body.error === 'folder not found' ? 'thư mục đã bị xoá' : (body.error || res.status));
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

// conversionFor decides what the file should become when downloaded on this
// device, or null for the original.
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
  $('#new-folder').addEventListener('click', async () => {
    const name = prompt('Tên thư mục mới');
    if (!name || !name.trim()) return;
    const { res, body } = await postJSON('/api/folders', { dir: state.dir, name: name.trim() });
    if (res.status === 409) alert('Đã có thư mục tên này.');
    else if (!res.ok) alert('Không tạo được thư mục: ' + (body.error || res.status));
    else openDir(body.path);
  });
  $('#select-all').addEventListener('click', () => {
    const all = state.selected.size < state.files.length;
    state.selected = new Set(all ? state.files.map((f) => f.path) : []);
    render();
  });
  $('#download-selected').addEventListener('click', async () => {
    const chosen = state.files.filter((f) => state.selected.has(f.path));
    for (const f of chosen) {
      await download(f);
      await sleep(700); // browsers block bursts of downloads
    }
  });
  $('#delete-selected').addEventListener('click', async () => {
    const paths = [...state.selected];
    if (!paths.length || !confirm(`Xoá ${paths.length} file khỏi thư mục chia sẻ? Thao tác này không hoàn tác được.`)) return;
    for (const p of paths) await api('/api/files/' + encPath(p), { method: 'DELETE' });
    state.selected.clear();
    refresh();
  });
}

async function refresh() {
  const dir = state.dir;
  const { res, body } = await api('/api/files?dir=' + encodeURIComponent(dir))
    .catch(() => ({ res: { ok: false, status: 0 }, body: {} }));
  if (dir !== state.dir) return; // the user moved on meanwhile
  if (res.status === 404 && dir) {
    openDir('', true); // the folder was deleted elsewhere
    return;
  }
  if (!res.ok) {
    if (res.status !== 401) setConn('bad', 'Mất kết nối với máy tính');
    return;
  }
  setConn('ok', 'Đã kết nối');
  const sig = JSON.stringify([body.folders, body.files]);
  if (sig === state.listSig) return;
  state.listSig = sig;
  state.folders = body.folders || [];
  state.files = body.files || [];
  const paths = new Set(state.files.map((f) => f.path));
  state.selected.forEach((p) => { if (!paths.has(p)) state.selected.delete(p); });
  render();
}

function render() {
  const list = $('#files');
  const existing = new Map([...list.children].map((li) => [li.dataset.key, li]));
  const keep = new Set();
  let prev = null;
  const place = (li) => {
    const want = prev ? prev.nextSibling : list.firstChild;
    if (want !== li) list.insertBefore(li, want);
    prev = li;
  };

  for (const d of state.folders) {
    const key = `d|${d.path}|${d.items}`;
    keep.add(key);
    let li = existing.get(key);
    if (!li) {
      li = $('#folder-tpl').content.firstElementChild.cloneNode(true);
      li.dataset.key = key;
      fillFolder(li, d);
    }
    place(li);
  }
  state.files.forEach((f, idx) => {
    const key = `f|${f.path}|${f.size}|${f.modified}`;
    keep.add(key);
    let li = existing.get(key);
    if (!li) {
      li = $('#file-tpl').content.firstElementChild.cloneNode(true);
      li.dataset.key = key;
      fillCard(li, f);
    }
    li.dataset.index = String(idx);
    const sel = $('.sel', li);
    sel.checked = state.selected.has(f.path);
    li.classList.toggle('selected', sel.checked);
    const busy = state.busy.get(f.path);
    const status = $('.status', li);
    status.hidden = !busy;
    if (busy) { status.textContent = busy.text; status.classList.toggle('err', !!busy.error); }
    place(li);
  });
  existing.forEach((li, key) => { if (!keep.has(key)) li.remove(); });

  const n = state.selected.size;
  $('#empty').hidden = state.files.length + state.folders.length > 0;
  const parts = [];
  if (state.folders.length) parts.push(`${state.folders.length} thư mục`);
  if (state.files.length) parts.push(`${state.files.length} file`);
  $('#count').textContent = parts.join(', ');
  $('#download-selected').disabled = n === 0;
  $('#delete-selected').disabled = n === 0;
  $('#download-selected').textContent = n ? `Tải về ${n} mục đã chọn` : 'Tải về các mục đã chọn';
  $('#delete-selected').textContent = n ? `Xoá ${n} mục` : 'Xoá';
  $('#select-all').disabled = state.files.length === 0;
  $('#select-all').textContent = n && n === state.files.length ? 'Bỏ chọn' : 'Chọn tất cả';
}

function fillFolder(li, d) {
  $('.name', li).textContent = d.name;
  $('.name', li).title = d.name;
  $('.count', li).textContent = d.items ? `${d.items} mục` : 'Trống';
  li.querySelectorAll('.folder-open').forEach((b) => b.addEventListener('click', () => openDir(d.path)));
  $('.del', li).addEventListener('click', async () => {
    if (!confirm(`Xoá thư mục "${d.name}"?`)) return;
    const { res } = await api('/api/files/' + encPath(d.path), { method: 'DELETE' });
    if (res.status === 409) alert('Thư mục còn file bên trong. Hãy xoá hoặc chuyển các file đó trước.');
    refresh();
  });
}

function fillCard(li, f) {
  $('.name', li).textContent = f.name;
  $('.name', li).title = f.name;
  $('.size', li).textContent = fmtSize(f.size);
  $('.badges', li).append(...formatBadges(f, false));

  const img = $('img', li);
  const icon = $('.kind-icon', li);
  $('.play', li).hidden = f.kind !== 'video';
  img.src = `/api/thumbs/${encPath(f.path)}?v=${f.modified}`;
  img.onerror = () => {
    if (f.kind === 'image' && canShowImage(f) && img.dataset.fallback !== '1') {
      img.dataset.fallback = '1';
      img.src = fileURL(f, true);
      return;
    }
    img.hidden = true;
    icon.hidden = false;
    icon.textContent = f.kind === 'video' ? 'Video ' + f.format.toUpperCase()
      : f.kind === 'image' ? 'Ảnh ' + f.format.toUpperCase() : 'File';
  };

  $('.sel', li).addEventListener('change', (e) => {
    if (e.target.checked) state.selected.add(f.path); else state.selected.delete(f.path);
    render();
  });
  $('.open', li).addEventListener('click', () => openViewer(Number(li.dataset.index)));
  $('.dl', li).addEventListener('click', () => download(f));
  $('.del', li).addEventListener('click', async () => {
    if (!confirm(`Xoá "${f.name}" khỏi thư mục chia sẻ? Thao tác này không hoàn tác được.`)) return;
    await api('/api/files/' + encPath(f.path), { method: 'DELETE' });
    refresh();
  });
}

function setBusy(f, text, error) {
  if (text) state.busy.set(f.path, { text, error }); else state.busy.delete(f.path);
  render();
}

// convert asks the computer for a copy of f in format `to` and waits for it.
// The original file is never changed.
async function convert(f, to, onStatus) {
  for (;;) {
    const { res, body } = await postJSON('/api/convert', { path: f.path, to });
    if (!res.ok) throw new Error(body.error || res.status);
    if (body.state === 'ready') return body.url;
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
    onStatus((body.state === 'queued' ? 'Đang chờ đổi định dạng…' : 'Đang đổi sang ' + to.toUpperCase()) + pct);
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
  const to = conversionFor(f);
  if (!to) { triggerDownload(fileURL(f)); return; }
  try {
    setBusy(f, 'Đang đổi sang ' + to.toUpperCase() + '…');
    const url = await convert(f, to, (t) => setBusy(f, t));
    setBusy(f, null);
    triggerDownload(url);
  } catch (err) {
    setBusy(f, err.message + (err.canFallback ? ' Đang tải file gốc.' : ''), true);
    if (err.canFallback) triggerDownload(fileURL(f));
  }
}

// ---------- Viewer ----------

let viewToken = 0;

function setupViewer() {
  const dlg = $('#viewer');
  $('#v-close').addEventListener('click', () => dlg.close());
  $('#v-prev').addEventListener('click', () => step(-1));
  $('#v-next').addEventListener('click', () => step(1));
  $('#v-download').addEventListener('click', () => {
    const f = state.files[state.viewIndex];
    if (f) download(f);
  });
  dlg.addEventListener('close', () => {
    viewToken++;
    const v = $('#v-video');
    v.pause();
    v.removeAttribute('src');
    v.load();
    $('#v-img').removeAttribute('src');
  });
  dlg.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowLeft') step(-1);
    if (e.key === 'ArrowRight') step(1);
  });
  let x0 = null;
  const stage = $('#v-stage');
  stage.addEventListener('touchstart', (e) => { x0 = e.touches[0].clientX; }, { passive: true });
  stage.addEventListener('touchend', (e) => {
    if (x0 === null) return;
    const dx = e.changedTouches[0].clientX - x0;
    x0 = null;
    if (Math.abs(dx) > 60) step(dx < 0 ? 1 : -1);
  });
}

function step(d) {
  const i = state.viewIndex + d;
  if (i >= 0 && i < state.files.length) showInViewer(i);
}

function openViewer(i) {
  const dlg = $('#viewer');
  if (!dlg.open) dlg.showModal();
  showInViewer(i);
}

async function showInViewer(i) {
  const f = state.files[i];
  if (!f) return;
  const token = ++viewToken;
  state.viewIndex = i;
  const img = $('#v-img');
  const video = $('#v-video');
  const msg = $('#v-msg');
  const note = $('#v-note');
  $('#v-name').textContent = f.name;
  $('#v-pos').textContent = `${i + 1} / ${state.files.length}`;
  $('#v-prev').disabled = i === 0;
  $('#v-next').disabled = i === state.files.length - 1;
  img.hidden = true;
  video.hidden = true;
  video.pause();
  video.removeAttribute('src');
  msg.hidden = true;
  note.textContent = '';
  const say = (t) => { if (token === viewToken) { msg.textContent = t; msg.hidden = false; } };

  // Show the original when this browser can; otherwise a temporary copy
  // made on the computer. Either way the original file stays untouched.
  const viaCopy = async (to) => {
    say('Đang chuẩn bị bản xem…');
    const url = await convert(f, to, say);
    if (token !== viewToken) return null;
    msg.hidden = true;
    note.textContent = to === 'mp4'
      ? 'Đang xem bản MP4 tạm, file gốc vẫn giữ nguyên.'
      : 'Đang xem bản JPG tạm, file gốc vẫn giữ nguyên.';
    return withInline(url);
  };

  try {
    if (f.kind === 'image') {
      let triedCopy = false;
      img.onerror = async () => {
        if (triedCopy || token !== viewToken) { say('Không hiển thị được ảnh này. Bạn vẫn có thể tải về.'); img.hidden = true; return; }
        triedCopy = true;
        try { const u = await viaCopy('jpg'); if (u) img.src = u; } catch (e) { img.hidden = true; say(e.message); }
      };
      img.onload = () => { if (token === viewToken) { img.hidden = false; msg.hidden = true; } };
      if (canShowImage(f)) {
        img.src = fileURL(f, true);
      } else {
        triedCopy = true;
        const u = await viaCopy('jpg');
        if (u) img.src = u;
      }
    } else if (f.kind === 'video') {
      let triedCopy = false;
      video.onerror = async () => {
        if (triedCopy || token !== viewToken) { video.hidden = true; say('Không phát được video này. Bạn vẫn có thể tải về.'); return; }
        triedCopy = true;
        try { const u = await viaCopy('mp4'); if (u) { video.src = u; video.hidden = false; } } catch (e) { video.hidden = true; say(e.message); }
      };
      if (canPlayVideo(f)) {
        video.src = fileURL(f, true);
        video.hidden = false;
      } else {
        triedCopy = true;
        const u = await viaCopy('mp4');
        if (u) { video.src = u; video.hidden = false; }
      }
    } else {
      say('Không xem trước được loại file này. Bấm Tải về để lấy file.');
    }
  } catch (err) {
    say(err.message + ' Bấm Tải về để lấy file gốc.');
  }
}

init().catch((err) => setConn('bad', 'Lỗi: ' + err.message));
