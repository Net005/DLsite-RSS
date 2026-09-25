'use strict';
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const setText = (el, t) => { if (el.textContent !== String(t)) el.textContent = t; };
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const S = { status: null, items: [], settings: null, logs: [], lastLogId: 0, paused: false, pending: [], view: 'grid' };
try { S.view = localStorage.getItem('view') || 'grid'; } catch (e) {}

async function api(path, opts = {}) {
  const r = await fetch(path, { ...opts, headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) } });
  let data = null; try { data = await r.json(); } catch (e) {}
  if (r.status === 401) { location.href = '/login'; throw new Error('Session expired'); }
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}
function toast(msg, err) {
  const t = document.createElement('div'); t.className = 'toast' + (err ? ' err' : ''); t.textContent = msg;
  $('#toasts').append(t); setTimeout(() => t.remove(), 4200);
}

/* ---------- time helpers ---------- */
// Compact, language-neutral durations ("5h 12m") so nothing here can be mistranslated.
function span(s) {
  s = Math.max(0, Math.floor(s));
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  if (d) return `${d}d ${h}h`; if (h) return `${h}h ${m}m`; if (m) return `${m}m`; return '<1m';
}
function ago(iso) { return iso ? span((Date.now() - new Date(iso)) / 1000) : '–'; }
function until(iso) {
  if (!iso) return '–';
  let s = Math.round((new Date(iso) - Date.now()) / 1000); if (s <= 0) return 'any moment';
  const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), sec = s % 60;
  return h ? `${h}h ${String(m).padStart(2, '0')}m` : m ? `${m}m ${String(sec).padStart(2, '0')}s` : `${sec}s`;
}
const fmtDate = iso => iso ? new Date(iso).toLocaleString() : '–';
const dur = ms => ms < 1000 ? ms + ' ms' : (ms / 1000).toFixed(1) + ' s';
function daysAgo(ts) { const d = (Date.now() / 1000 - ts) / 86400; return d < 1 ? 'today' : d < 2 ? 'yesterday' : Math.floor(d) + ' days ago'; }

/* ---------- tabs ---------- */
function showTab() {
  const t = (location.hash || '#dashboard').slice(1);
  const tab = $('#tab-' + t) ? t : 'dashboard';
  $$('.tab').forEach(e => e.hidden = e.id !== 'tab-' + tab);
  $$('#tabs a').forEach(a => a.classList.toggle('on', a.dataset.tab === tab));
  if (tab === 'log') scrollLog(true);
  if (tab === 'history') loadRuns();
}
addEventListener('hashchange', showTab);

/* ---------- status ---------- */
function renderStatus() {
  const st = S.status; if (!st) return;
  setText($('#sTotal'), st.total); setText($('#tabCount'), st.total);
  setText($('#sTotalSub'), st.chrome_available ? 'browser scraping available' : 'HTTP mode only');
  setText($('#sNew'), st.new_in_feed);
  setText($('#sNewSub'), `first seen within ${S.settings ? S.settings.new_items_days : 7} days`);
  const lr = st.last_run;
  setText($('#sLast'), lr ? ago(lr.started) : '–');
  setText($('#sLastSub'), lr ? `${lr.status} · ${lr.found} found · ${lr.new} new · ${new Date(lr.started).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})}` : 'no scrape yet');
  setText($('#sNext'), st.running ? 'running…' : st.auto_scrape ? until(st.next_run) : 'off');
  setText($('#sNextSub'), st.auto_scrape ? `every ${st.interval_hours} h` : 'auto-scrape disabled');
  const dot = $('#runDot'), bar = $('#runBar');
  dot.classList.toggle('run', st.running);
  setText($('#runPhase'), st.running ? 'Scraping — ' + st.phase : 'Idle');
  setText($('#runMeta'), st.running ? (st.progress ? `${st.progress} games collected · started by ${st.trigger}` : `started by ${st.trigger}`)
    : lr ? `last run: ${lr.status}, ${ago(lr.started)} ago${lr.error ? ' — ' + lr.error : ''}` : 'waiting for the first scrape');
  bar.classList.toggle('indet', st.running && !st.progress);
  const target = S.settings ? S.settings.target_games : 100;
  bar.firstElementChild.style.width = st.running && st.progress ? Math.min(100, st.progress / target * 100) + '%' : (st.running ? '' : '0');
  $('#btnRun').disabled = st.running; $('#btnCancel').hidden = !st.running;
  setText($('#aboutLine'), `Version ${st.version} · uptime ${Math.floor(st.uptime_sec / 60)} min`);
}
setInterval(() => { if (S.status && !document.hidden) { const st = S.status; setText($('#sNext'), st.running ? 'running…' : st.auto_scrape ? until(st.next_run) : 'off'); if (st.last_run) setText($('#sLast'), ago(st.last_run.started)); } }, 1000);

/* ---------- items ---------- */
function rankDelta(it) {
  if (!it.prev_rank) return it.is_new ? '<span class="delta new">NEW</span>' : '<span class="delta same">—</span>';
  const d = it.prev_rank - it.rank;
  return d > 0 ? `<span class="delta up">▲ ${d}</span>` : d < 0 ? `<span class="delta down">▼ ${-d}</span>` : '<span class="delta same">—</span>';
}
function card(it) {
  const rc = it.rank <= 3 ? ' r' + it.rank : '';
  return `<article class="card${it.active ? '' : ' dim'}" data-pid="${esc(it.product_id)}">
  <div class="thumb"><img loading="lazy" referrerpolicy="no-referrer" src="${esc(it.cover)}" alt="" onerror="this.style.visibility='hidden'">
    <span class="rank${rc}">${it.rank}</span>${it.is_new ? '<span class="tag">NEW</span>' : ''}${it.category ? `<span class="cat">${esc(it.category)}</span>` : ''}</div>
  <div class="acts">
    <button data-act="open" title="Open on DLsite">↗</button>
    <button data-act="hide" title="${it.hidden ? 'Show in feed' : 'Hide from feed'}">${it.hidden ? '👁' : '🚫'}</button>
    <button data-act="del" title="Delete from state">🗑</button></div>
  <div class="body">
    <a class="ttl" href="${esc(it.url)}" target="_blank" rel="noopener" title="${esc(it.title)}">${esc(it.title)}</a>
    <div class="circ">${esc(it.circle) || '&nbsp;'}</div>
    <div class="meta"><span class="price">${esc((it.price || '').split(' / ')[0])}</span>${rankDelta(it)}<span class="muted" title="first seen">${daysAgo(it.first_seen)}</span></div>
  </div></article>`;
}
function filtered() {
  const q = $('#q').value.trim().toLowerCase(), f = $('#fFilter').value, cat = $('#fCat').value, sort = $('#fSort').value;
  let a = S.items.filter(it => {
    if (q && !(it.title + ' ' + it.circle + ' ' + it.product_id).toLowerCase().includes(q)) return false;
    if (cat && it.category !== cat) return false;
    if (f === 'feed') return it.is_new && !it.hidden;
    if (f === 'active') return it.active;
    if (f === 'dropped') return !it.active;
    if (f === 'hidden') return it.hidden;
    return true;
  });
  const cmp = { rank: (x, y) => x.rank - y.rank, newest: (x, y) => y.first_seen - x.first_seen || x.rank - y.rank,
    climb: (x, y) => ((y.prev_rank ? y.prev_rank - y.rank : -999) - (x.prev_rank ? x.prev_rank - x.rank : -999)), title: (x, y) => x.title.localeCompare(y.title) };
  return a.sort(cmp[sort]);
}
function renderItems() {
  const box = $('#items'); const a = filtered();
  box.className = 'cards' + (S.view === 'list' ? ' list' : '');
  box.innerHTML = a.map(card).join('');
  $('#empty').hidden = a.length > 0;
  $('#resultCount').textContent = `${a.length} of ${S.items.length}`;
  const cats = [...new Set(S.items.map(i => i.category).filter(Boolean))].sort(), sel = $('#fCat'), cur = sel.value;
  sel.innerHTML = '<option value="">All types</option>' + cats.map(c => `<option${c === cur ? ' selected' : ''}>${esc(c)}</option>`).join('');
  $('#vGrid').classList.toggle('on', S.view === 'grid'); $('#vList').classList.toggle('on', S.view === 'list');
  // dashboard: newest arrivals
  const fresh = [...S.items].filter(i => i.is_new && !i.hidden).sort((x, y) => y.first_seen - x.first_seen || x.rank - y.rank).slice(0, 12);
  $('#newList').innerHTML = fresh.length ? fresh.map(i => `<a class="mrow" href="${esc(i.url)}" target="_blank" rel="noopener">
    <img loading="lazy" referrerpolicy="no-referrer" src="${esc(i.cover)}" alt=""><div class="t"><b>${esc(i.title)}</b><span>${esc(i.circle)} · ${daysAgo(i.first_seen)}</span></div><span class="rk">#${i.rank}</span></a>`).join('')
    : '<div class="empty">Nothing new yet.</div>';
}
async function loadItems() { try { S.items = await api('/api/items'); renderItems(); } catch (e) { toast(e.message, true); } }

$('#items').addEventListener('click', async e => {
  const cardEl = e.target.closest('.card'); if (!cardEl) return;
  const b = e.target.closest('button[data-act]');
  if (!b) { // click anywhere on the card (except links/buttons) opens the game on DLsite
    if (e.target.closest('a')) return;
    const g = S.items.find(i => i.product_id === cardEl.dataset.pid); if (g) window.open(g.url, '_blank', 'noopener');
    return;
  }
  const it = S.items.find(i => i.product_id === cardEl.dataset.pid); if (!it) return;
  try {
    if (b.dataset.act === 'open') window.open(it.url, '_blank', 'noopener');
    if (b.dataset.act === 'hide') { await api(`/api/items/${it.product_id}/hide?hidden=${!it.hidden}`, { method: 'POST' }); toast(it.hidden ? 'Restored to feed' : 'Hidden from feed'); }
    if (b.dataset.act === 'del' && confirm(`Delete "${it.title}" from the tracked state?\nIt will be re-added as NEW if it is still in the ranking at the next scrape.`)) { await api('/api/items/' + it.product_id, { method: 'DELETE' }); toast('Deleted'); }
  } catch (err) { toast(err.message, true); }
});
['q', 'fFilter', 'fSort', 'fCat'].forEach(id => $('#' + id).addEventListener('input', renderItems));
$('#vGrid').onclick = () => { S.view = 'grid'; try { localStorage.setItem('view', 'grid'); } catch (e) {} renderItems(); };
$('#vList').onclick = () => { S.view = 'list'; try { localStorage.setItem('view', 'list'); } catch (e) {} renderItems(); };

/* ---------- log ---------- */
function logLine(e) {
  return `<div class="ll ${e.level}" data-id="${e.id}"><span class="t">${esc(e.time.slice(11))}</span><span class="lv">${esc(e.level.toUpperCase())}</span>${esc(e.msg)}</div>`;
}
function logVisible(e) {
  const q = $('#logQ').value.trim().toLowerCase(), lv = $('#logLevel').value;
  if (lv === 'warn' && e.level === 'info') return false; if (lv === 'error' && e.level !== 'error') return false;
  return !q || e.msg.toLowerCase().includes(q);
}
function renderLog() {
  $('#fullLog').innerHTML = S.logs.filter(logVisible).map(logLine).join('');
  $('#miniLog').innerHTML = S.logs.slice(-40).map(logLine).join('');
  scrollLog(); const m = $('#miniLog'); m.scrollTop = m.scrollHeight;
}
function scrollLog(force) { const f = $('#fullLog'); if (force || $('#logFollow').checked) f.scrollTop = f.scrollHeight; }
function addLog(e) {
  if (e.id <= S.lastLogId) return; S.lastLogId = e.id;
  if (S.paused) { S.pending.push(e); return; }
  S.logs.push(e); if (S.logs.length > 1500) S.logs.shift();
  const f = $('#fullLog'); if (logVisible(e)) f.insertAdjacentHTML('beforeend', logLine(e));
  const m = $('#miniLog'); m.insertAdjacentHTML('beforeend', logLine(e)); while (m.children.length > 40) m.firstChild.remove();
  m.scrollTop = m.scrollHeight; while (f.children.length > 1500) f.firstChild.remove(); scrollLog();
}
$('#logQ').oninput = $('#logLevel').onchange = renderLog;
$('#logPause').onclick = () => {
  S.paused = !S.paused; $('#logPause').textContent = S.paused ? `Resume` : 'Pause';
  if (!S.paused) { const p = S.pending; S.pending = []; p.forEach(e => { S.lastLogId = Math.min(S.lastLogId, e.id - 1); addLog(e); }); }
};
$('#logClear').onclick = async () => { await api('/api/logs', { method: 'DELETE' }); S.logs = []; renderLog(); };
$('#logDl').onclick = () => {
  const blob = new Blob([S.logs.map(e => `${e.time} [${e.level.toUpperCase()}] ${e.msg}`).join('\n')], { type: 'text/plain' });
  const a = Object.assign(document.createElement('a'), { href: URL.createObjectURL(blob), download: 'dlsite-rss.log' }); a.click();
};

/* ---------- history ---------- */
async function loadRuns() {
  try {
    const runs = await api('/api/runs');
    $('#runsTable tbody').innerHTML = runs.length ? runs.map(r => `<tr><td>${r.id}</td><td>${fmtDate(r.started)}</td><td>${esc(r.trigger)}</td><td>${esc(r.source || '–')}</td>
      <td><span class="pill ${r.status}" title="${esc(r.error || '')}">${r.status}</span></td><td class="r">${r.found}</td><td class="r">${r.new}</td><td class="r">${dur(r.duration_ms)}</td></tr>`).join('')
      : '<tr><td colspan="8" class="muted">No runs yet.</td></tr>';
  } catch (e) { toast(e.message, true); }
}
$('#btnClearRuns').onclick = async () => { if (confirm('Clear scrape history?')) { await api('/api/runs', { method: 'DELETE' }); loadRuns(); } };

/* ---------- actions ---------- */
$('#btnRun').onclick = async () => { try { await api('/api/scrape', { method: 'POST' }); toast('Scrape started'); } catch (e) { toast(e.message, true); } };
$('#btnCancel').onclick = () => api('/api/scrape/cancel', { method: 'POST' }).then(() => toast('Cancelling…'));
$('#btnCopy').onclick = async () => { try { await navigator.clipboard.writeText($('#feedUrl').value); toast('Feed URL copied'); } catch (e) { $('#feedUrl').select(); toast('Press Ctrl+C to copy'); } };

/* ---------- settings ---------- */
function fillForm() {
  const f = $('#settingsForm'), s = S.settings; if (!s) return;
  for (const [k, v] of Object.entries(s)) { const el = f.elements[k]; if (!el) continue; if (el.type === 'checkbox') el.checked = v; else el.value = v; }
  const url = (s.public_url || location.origin) + '/feed.xml';
  $('#feedUrl').value = url; $('#btnOpenFeed').href = '/feed.xml';
  $('#feedHint').textContent = `Serves games first seen in the last ${s.new_items_days} days. Optional query parameters: ?days=30 &limit=25`;
}
$('#settingsForm').addEventListener('submit', async e => {
  e.preventDefault(); const f = e.target, body = {};
  for (const el of f.elements) {
    if (!el.name) continue;
    body[el.name] = el.type === 'checkbox' ? el.checked : el.type === 'number' ? Number(el.value) : el.value.trim();
  }
  try { S.settings = await api('/api/settings', { method: 'PUT', body: JSON.stringify(body) }); fillForm(); renderStatus(); toast('Settings saved'); $('#saveMsg').textContent = 'Saved ' + new Date().toLocaleTimeString(); }
  catch (err) { toast(err.message, true); }
});
$('#btnWebhook').onclick = async () => { try { await api('/api/webhook/test', { method: 'POST' }); toast('Test message sent'); } catch (e) { toast(e.message + ' (save settings first)', true); } };
$('#btnClearState').onclick = async () => { if (confirm('Remove ALL tracked games? Everything currently in the ranking will show as new on the next scrape.')) { await api('/api/items/clear', { method: 'POST' }); toast('State cleared'); } };
$('#importFile').onchange = async e => {
  const file = e.target.files[0]; if (!file) return;
  if (!confirm(`Replace all tracked games with the contents of ${file.name}?`)) { e.target.value = ''; return; }
  try { const r = await api('/api/import', { method: 'POST', body: await file.text() }); toast(`Imported ${r.imported} games`); } catch (err) { toast(err.message, true); }
  e.target.value = '';
};

/* ---------- live connection ---------- */
function connect() {
  const es = new EventSource('/api/events'), c = $('#conn');
  es.onopen = () => { c.className = 'conn ok'; c.lastChild.textContent = 'live'; };
  es.onerror = () => { c.className = 'conn bad'; c.lastChild.textContent = 'reconnecting…'; };
  es.addEventListener('log', e => addLog(JSON.parse(e.data)));
  es.addEventListener('status', e => { S.status = JSON.parse(e.data); renderStatus(); });
  es.addEventListener('items', () => loadItems());
  es.addEventListener('open', () => {});
}

(async function init() {
  showTab();
  try {
    S.settings = await api('/api/settings'); fillForm();
    S.logs = await api('/api/logs?n=500'); S.lastLogId = S.logs.length ? S.logs[S.logs.length - 1].id : 0; renderLog();
    S.status = await api('/api/status'); renderStatus();
  } catch (e) { toast(e.message, true); }
  await loadItems(); connect();
  fetch('/api/auth').then(r => r.json()).then(a => { $('#btnLogout').hidden = !a.enabled; }).catch(() => {});
  setInterval(() => api('/api/status').then(s => { S.status = s; renderStatus(); }).catch(() => {}), 30000);
})();

$('#btnLogout').onclick = async () => { await fetch('/api/logout', { method: 'POST' }); location.href = '/login'; };
