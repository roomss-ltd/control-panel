'use strict';

// ---------- rich replies ----------
// What JARVIS's answers can carry besides markdown: styled tables (markdown ones get numbers aligned and status
// words as pills; ```table JSON adds typed columns: bars, statuses, terminal links, sorting), ```buttons (open a
// terminal chat or a panel page, send JARVIS a reply, follow a link), ```mermaid diagrams, ```chart charts
// (Chart.js: line, bar, pie, doughnut, radar, polarArea, scatter, and venn) and files on the VM it links to
// (cards that download, images inline). The chart and diagram libraries load the first time a reply needs
// them; a drawn one is kept per source, so the chat's re-renders move the same node instead of drawing again.

const RICH = { cache: new Map(), libs: {}, n: 0, mermaidReady: false };
const RICH_COLORS = ['#7aa2ff', '#2dd4bf', '#bc8cff', '#34d399', '#f472b6', '#fbbf24', '#60a5fa', '#fb7185'];

// ---- files: [label](/abs/path), ![alt](/abs/path.png) and Hermes' "MEDIA:/abs/path" lines ----
const RICH_LOCAL = /(!?)\[([^\]\n]*)\]\(((?:~|\/home\/[^/\s)]+|\/root|\/tmp)\/[^)\s]+)\)|^MEDIA:\s*(\S+)\s*$/gm;
const richIsImg = (p) => /\.(png|jpe?g|gif|webp|avif)$/i.test(p);

// richMd(text, reply): md() with ```buttons and ```table drawn, and the VM's files as cards and images. reply: what
// the owner said next, if anything (buttons that sent it show as chosen; the rest of their group is spent).
function richMd(text, reply) {
  const refs = [];
  const blocks = [];
  const marked = String(text ?? '')
    .replace(/<details>\s*<summary>([\s\S]*?)<\/summary>([\s\S]*?)<\/details>/g, (_, sum, inner) => { // Telegram's collapsed blocks
      blocks.push(`<details class="ai-det"><summary>${esc(sum.trim())}</summary><div class="md">${richMd(inner.trim(), reply)}</div></details>`);
      return `\n⟦B${blocks.length - 1}⟧\n`;
    })
    .replace(/^```(buttons|table|params|ratemodel|rates|asset|proposal|artifact)[^\n]*\n([\s\S]*?)\n```[ \t]*$/gm, (m, kind, body) => {
      try { blocks.push(kind === 'buttons' ? richButtons(JSON.parse(body), reply) : RICH_BLOCKS[kind](JSON.parse(body), reply)); } catch { return m; } // not JSON: shown as code
      return `\n⟦B${blocks.length - 1}⟧\n`;
    })
    .replace(RICH_LOCAL, (m, bang, label, path, media) => {
      const p = (path || media).replace(/^~(?=\/)/, window.cpHome || '~');
      refs.push({ img: !!bang || (!!media && richIsImg(p)), label, path: p });
      return `⟦F${refs.length - 1}⟧`;
    });
  return md(marked)
    .replace(/(?:<p>)?⟦B(\d+)⟧(?:<\/p>)?/g, (_, i) => blocks[+i])
    .replace(/⟦F(\d+)⟧/g, (_, i) => richFile(refs[+i]));
}

// ---- buttons: [{"label", "send" | "open" | "url", "style": "primary" | "danger", "icon"}], or {"title", "buttons": [...]} ----
// send: the text goes to JARVIS as your reply; open: "pane:<session>:<pane>" (that terminal's chat), a panel page
// (res, ci, term, sec) or "control:<tab>" (memory, skills, tools, mcp, jobs, logs); url: a link.
function richButtons(spec, reply) {
  const list = Array.isArray(spec) ? spec : spec.buttons || [];
  const said = (reply || '').trim();
  const spent = said !== '';
  const html = list.map((b) => {
    const cls = `ai-btn${b.style === 'primary' || b.style === 'danger' ? ' ' + b.style : ''}`;
    const icon = b.icon ? ic(b.icon) : b.open?.startsWith('pane:') ? ic('terminal-square') : b.open ? ic('arrow-right') : b.url ? ic('arrow-up-right') : '';
    const label = esc(b.label || b.send || b.open || b.url || '?');
    if (b.url) return `<a class="${cls}" href="${esc(/^(https?:\/\/|\/hermes\/)/.test(b.url) ? b.url : '#')}" target="_blank" rel="noopener">${label}${icon}</a>`;
    if (b.open) return `<button type="button" class="${cls}" data-aiopen="${esc(b.open)}">${icon}${label}</button>`;
    const chosen = spent && said === String(b.send ?? b.label).trim();
    // "✍️ Other" (labelled so, its send "Other", or nothing to send at all): your own answer, typed: the button opens
    // the box and sends nothing (Telegram has no box: there it sends, and JARVIS waits for the typed text)
    const otherRe = /^\W*other\W*$/i;
    if (otherRe.test(String(b.label ?? '').trim()) || otherRe.test(String(b.send ?? '').trim()) || !String(b.send ?? b.label ?? '').trim()) return `<button type="button" class="${cls}" data-aiother${spent ? ' disabled' : ''}>${icon}${label}</button>`;
    return `<button type="button" class="${cls}${chosen ? ' chosen' : ''}" data-aisend="${esc(b.send ?? b.label)}"${spent ? ' disabled' : ''}>${chosen ? ic('check') : icon}${label}</button>`;
  }).join('');
  return `<div class="ai-btns">${spec.title ? `<div class="ai-btns-t">${esc(spec.title)}</div>` : ''}<div class="ai-btns-r">${html}</div></div>`;
}

// ---- tables: {"title", "columns": [{"key", "label", "type", "align"}], "rows": [{...}] or [[...]]} ----
// types: text (markdown), number, percent, bar (0-100), status (a pill), pane ("box-dev:3" or {pane, label}: opens
// that terminal's chat), date (ISO or epoch: shown as "5m ago"), code, link ({url, label} or a URL)
const RICH_PILL = [[/^(ok|pass(ed)?|success(ful)?|succeeded|done|complete(d)?|healthy|up|online|active|yes|green|merged|✅)$/i, 'ok'],
  [/^(fail(ed|ing)?|error|errored|down|broken|critical|offline|no|red|❌|blocked)$/i, 'bad'],
  [/^(warn(ing)?|pending|waiting|stalled|slow|degraded|paused|queued|asking|review|amber|draft|⚠️)$/i, 'warn'],
  [/^(running|working|in progress|building|deploying|busy|live)$/i, 'busy']];
const richPill = (v) => { const t = String(v ?? '').trim(); const k = RICH_PILL.find(([re]) => re.test(t)); return k ? `<span class="pill ${k[1]}">${esc(t)}</span>` : null; };
const RICH_NUM = /^[-+−]?[$€£]?\s?\d[\d,.\s]*(%|ms|s|m|h|d|k|K|M|B|GB|MB|KB|TB|x|×)?$|^(\d+(\.\d+)?\s?[hms]\s?)+$/;

// A ```table block: columns and rows from the JSON spec, with typed cells (bars, status pills, terminal links) and sortable headers.
function richTable(spec) {
  const cols = (spec.columns || []).map((c) => (typeof c === 'string' ? { key: c, label: c } : c));
  const rows = spec.rows || [];
  const val = (r, c, i) => (Array.isArray(r) ? r[i] : r[c.key]);
  const cell = (c, v) => {
    if (v == null || v === '') return ['', '<span class="faint">–</span>', ''];
    switch (c.type) {
      case 'number': return [+v, esc(typeof v === 'number' ? v.toLocaleString() : v), 'num'];
      case 'percent': return [+v, esc(v) + '%', 'num'];
      case 'bar': { const n = Math.max(0, Math.min(100, +v)); return [n, `<div class="ai-bar ${n >= 80 ? 'ok' : n >= 50 ? 'warn' : 'bad'}"><i data-w="${n}"></i></div><span>${esc(v)}%</span>`, 'tbar']; }
      case 'status': return [String(v), richPill(v) || `<span class="pill">${esc(v)}</span>`, ''];
      case 'pane': { const p = typeof v === 'object' ? v : { pane: v }; return [p.label || p.pane, `<button type="button" class="ai-btn sm" data-aiopen="pane:${esc(p.pane)}">${ic('terminal-square')}${esc(p.label || p.pane)}</button>`, '']; }
      case 'date': { const t = typeof v === 'number' ? v : Date.parse(v) / 1000; return [t, `<span title="${esc(new Date(t * 1000).toLocaleString())}">${esc(ago(t))}</span>`, '']; }
      case 'code': return [String(v), `<code>${esc(v)}</code>`, ''];
      case 'link': { const l = typeof v === 'object' ? v : { url: v }; return [l.label || l.url, `<a href="${esc(/^https?:\/\//.test(l.url) ? l.url : '#')}" target="_blank" rel="noopener">${esc(l.label || l.url)}</a>`, '']; }
      default: return [String(v), richPill(v) || mdInline(String(v)), RICH_NUM.test(String(v).trim()) ? 'num' : ''];
    }
  };
  const head = cols.map((c, i) => `<th class="${c.align === 'right' || ['number', 'percent'].includes(c.type) ? 'num' : ''}"><button type="button" data-aisort="${i}">${esc(c.label ?? c.key)}${ic('angle-down')}</button></th>`).join('');
  const body = rows.map((r) => `<tr>${cols.map((c, i) => { const [sv, h, cls] = cell(c, val(r, c, i)); return `<td class="${cls}${c.align === 'right' ? ' num' : ''}" data-v="${esc(sv)}">${h}</td>`; }).join('')}</tr>`).join('');
  return `<figure class="ai-tblw">${spec.title ? `<figcaption>${esc(spec.title)}</figcaption>` : ''}<div class="ai-tbls"><table class="ai-tbl"><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div></figure>`;
}

// markdown tables in answers get the same look: numbers aligned right, status words as pills, sortable headers
function richStyleTables(root) {
  for (const t of root.querySelectorAll('.ai-ans table:not(.ai-tbl)')) {
    t.classList.add('ai-tbl', 'md-tbl');
    const wrap = document.createElement('figure');
    wrap.className = 'ai-tblw';
    wrap.innerHTML = '<div class="ai-tbls"></div>';
    t.replaceWith(wrap);
    wrap.firstChild.append(t);
    const ncols = t.rows[0]?.cells.length || 0;
    [...(t.tHead?.rows[0]?.cells || [])].forEach((th, i) => { th.innerHTML = `<button type="button" data-aisort="${i}">${th.innerHTML}${ic('angle-down')}</button>`; }); // sortable too
    for (const td of t.querySelectorAll('tbody td')) td.dataset.v = td.textContent.trim();
    for (let c = 0; c < ncols; c++) {
      const cells = [...t.querySelectorAll(`tbody tr > :nth-child(${c + 1})`)];
      const nums = cells.filter((td) => RICH_NUM.test(td.textContent.trim())).length;
      if (cells.length && nums >= Math.ceil(cells.length * 0.6)) { // a numbers column: right-aligned, header too
        for (const td of cells) td.classList.add('num');
        t.querySelector(`thead tr > :nth-child(${c + 1})`)?.classList.add('num');
      }
      for (const td of cells) { if (!td.querySelector('*')) { const p = richPill(td.textContent); if (p) td.innerHTML = p; } }
    }
  }
  for (const i of root.querySelectorAll('.ai-bar i[data-w]')) i.style.width = i.dataset.w + '%';
}

// a ```table's header sorts its column: first by value, again to reverse
function richSort(th) {
  const t = th.closest('table');
  const i = +th.querySelector('[data-aisort]').dataset.aisort;
  const dir = th.dataset.dir === 'asc' ? 'desc' : 'asc';
  for (const h of t.tHead.rows[0].cells) delete h.dataset.dir;
  th.dataset.dir = dir;
  const rows = [...t.tBodies[0].rows];
  const key = (r) => { const v = r.cells[i]?.dataset.v ?? ''; const n = parseFloat(String(v).replace(/[^\d.\-]/g, '')); return Number.isFinite(n) && /\d/.test(v) ? n : String(v).toLowerCase(); };
  rows.sort((a, b) => { const x = key(a), y = key(b); const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y)); return dir === 'asc' ? c : -c; });
  t.tBodies[0].append(...rows);
}

function richFile({ img, label, path }) {
  const url = '/api/ai/file?path=' + encodeURIComponent(path);
  const name = path.split('/').pop();
  if (img) return `<a class="ai-img" href="${url}" target="_blank" rel="noopener"><img src="${url}" alt="${esc(label || name)}" loading="lazy"></a>`;
  const ext = (/\.([a-z0-9]{1,6})$/i.exec(name)?.[1] || 'file').toUpperCase();
  const open = /\.(pdf|txt)$/i.test(name) ? 'target="_blank" rel="noopener"' : `download="${esc(name)}"`;
  return `<a class="ai-file" href="${url}" ${open}><span class="ai-file-x">${esc(ext)}</span>` +
    `<span class="ai-file-n"><b>${esc(label && label !== name ? label : name)}</b><small>${esc(tilde(path))}</small></span>${ic('download2')}</a>`;
}

// ---- diagrams, charts and cards: ```mermaid, ```chart and ```card code blocks become figures ----
function richEnhance(root) {
  richStyleTables(root);
  const seen = new Map();
  for (const cb of root.querySelectorAll('.cb')) {
    const kind = cb.querySelector('.cb-l')?.textContent;
    if (kind !== 'mermaid' && kind !== 'chart' && kind !== 'card') continue;
    const src = cb.querySelector('code').textContent;
    const n = (seen.get(src) || 0) + 1; // the same source twice: two figures
    seen.set(src, n);
    const key = `${kind}:${n}:${src}`;
    let fig = RICH.cache.get(key);
    if (!fig) {
      fig = document.createElement('figure');
      fig.className = 'rich rich-' + kind;
      fig.innerHTML = `<div class="rich-body">${shim(kind === 'mermaid' ? 'Drawing the diagram…' : `Drawing the ${kind}…`)}</div>`;
      RICH.cache.set(key, fig);
      if (RICH.cache.size > 300) RICH.cache.delete(RICH.cache.keys().next().value);
      ({ chart: richChart, card: richCard, mermaid: richMermaid })[kind](fig, src).catch((err) => richFail(fig, kind, src, err));
    }
    cb.replaceWith(fig);
  }
}

function richFail(fig, kind, src, err) {
  fig.className = 'rich rich-fail';
  fig.innerHTML = `<div class="rich-err">${ic('alert-triangle')}<span>Couldn't draw this ${kind === 'mermaid' ? 'diagram' : kind}: ${esc(String(err?.message || err).split('\n')[0].slice(0, 200))}</span></div>` +
    `<pre class="rich-src"><code>${esc(src)}</code></pre>`;
}

function richLib(name) {
  return (RICH.libs[name] ||= new Promise((ok, fail) => {
    const s = document.createElement('script');
    s.src = document.querySelector(`meta[name="lib-${name}"]`).content; // versioned by the server: cached for good
    s.onload = ok;
    s.onerror = () => { delete RICH.libs[name]; fail(new Error(name + " didn't load")); };
    document.head.append(s);
  }));
}

// ```card JSON: {"card": "metric-card", "props": {…}}: one of vm-agent's message-ui cards, the live component (aicard.go)
async function richCard(fig, src) {
  const r = await api('/api/ai/card', JSON.parse(src));
  if (r.error || !r.html) throw new Error(r.error || 'no card');
  fig.innerHTML = `<div class="rich-body rich-mui">${r.html}</div>`;
}

// Draws a ```mermaid block into its figure; the library loads on first use and is configured once.
async function richMermaid(fig, src) {
  await richLib('mermaid');
  if (!RICH.mermaidReady) {
    mermaid.initialize({
      startOnLoad: false, securityLevel: 'strict', theme: 'base', fontFamily: 'system-ui, -apple-system, "Segoe UI", sans-serif',
      themeVariables: {
        darkMode: true, background: 'transparent', fontSize: '14px',
        primaryColor: '#18202c', primaryTextColor: '#e7e9ec', primaryBorderColor: '#4d6fb8',
        secondaryColor: '#1d1830', secondaryTextColor: '#e7e9ec', secondaryBorderColor: '#7a5cc0',
        tertiaryColor: '#12241f', tertiaryTextColor: '#e7e9ec', tertiaryBorderColor: '#2f8f74',
        lineColor: '#6b7585', textColor: '#c9ced6', mainBkg: '#18202c', nodeBorder: '#4d6fb8',
        clusterBkg: 'rgba(122,162,255,.05)', clusterBorder: '#2a3240', edgeLabelBackground: '#121519',
        noteBkgColor: '#1f1a12', noteTextColor: '#e7e9ec', noteBorderColor: '#8a6d2c',
        actorBkg: '#18202c', actorBorder: '#4d6fb8', actorTextColor: '#e7e9ec', signalColor: '#9aa3b0', signalTextColor: '#c9ced6',
        pie1: RICH_COLORS[0], pie2: RICH_COLORS[1], pie3: RICH_COLORS[2], pie4: RICH_COLORS[3], pie5: RICH_COLORS[4], pie6: RICH_COLORS[5],
        pieStrokeColor: '#0a0c0f', pieOuterStrokeColor: '#2a3240', pieTitleTextColor: '#e7e9ec', pieSectionTextColor: '#0a0c0f', pieLegendTextColor: '#c9ced6',
        git0: RICH_COLORS[0], git1: RICH_COLORS[1], git2: RICH_COLORS[2], git3: RICH_COLORS[3],
      },
    });
    RICH.mermaidReady = true;
  }
  const { svg } = await mermaid.render('mmd' + ++RICH.n, src);
  fig.innerHTML = `<div class="rich-body">${svg}</div>`;
}

// ```chart JSON: {"type", "title", "labels", "datasets": [{"label", "data"}]}; venn: "sets" with members
// ([{"label":"A","values":[…]}]) or "areas" with counts ([{"sets":["A","B"],"value":3}]).
async function richChart(fig, src) {
  const spec = JSON.parse(src);
  const type = String(spec.type || 'line').toLowerCase();
  await richLib('chart');
  if (type === 'venn') await richLib('venn');
  const Chart = window.Chart;
  Chart.defaults.font.family = 'system-ui, -apple-system, "Segoe UI", sans-serif';
  Chart.defaults.color = '#9aa3b0';
  fig.innerHTML = (spec.title ? `<figcaption>${esc(spec.title)}</figcaption>` : '') + '<div class="rich-body rich-canvas"><canvas></canvas></div>';
  const canvas = fig.querySelector('canvas');
  const round = ['pie', 'doughnut', 'polarArea'].includes(type);
  if (round || type === 'venn') fig.classList.add('rich-round');
  const tooltip = { backgroundColor: 'rgba(18,21,25,.94)', borderColor: 'rgba(255,255,255,.1)', borderWidth: 1, padding: 10, cornerRadius: 10, titleColor: '#e7e9ec', bodyColor: '#c9ced6', boxPadding: 4 };
  const legend = { position: round ? 'right' : 'bottom', labels: { color: '#c9ced6', usePointStyle: true, pointStyle: 'circle', boxWidth: 8, padding: 14 } };
  if (type === 'venn') {
    const data = spec.sets ? ChartVenn.extractSets(spec.sets, { label: spec.title || '' })
      : { labels: spec.areas.map((a) => a.sets.join(' ∩ ')), datasets: [{ label: spec.title || '', data: spec.areas.map((a) => ({ sets: a.sets, value: a.value })) }] };
    Object.assign(data.datasets[0], { backgroundColor: RICH_COLORS.map((c) => c + '40'), borderColor: RICH_COLORS, borderWidth: 2 });
    new ChartVenn.VennDiagramChart(canvas, { data, options: { maintainAspectRatio: false, animation: { duration: 900 }, plugins: { legend: { display: false }, tooltip } } });
    return;
  }
  const datasets = (spec.datasets || []).map((d, i) => {
    const c = RICH_COLORS[i % RICH_COLORS.length];
    if (round) return { borderColor: '#0a0c0f', borderWidth: 2, hoverOffset: 8, backgroundColor: (d.data || []).map((_, j) => RICH_COLORS[j % RICH_COLORS.length] + 'dd'), ...d };
    if (type === 'line' || type === 'radar' || type === 'scatter') {
      return { borderColor: c, backgroundColor: c + '26', pointBackgroundColor: c, pointRadius: type === 'scatter' ? 4 : 2.5, pointHoverRadius: 5, borderWidth: 2, tension: 0.35, fill: type === 'radar', ...d };
    }
    return { backgroundColor: c + 'cc', hoverBackgroundColor: c, borderRadius: 6, borderSkipped: false, maxBarThickness: 46, ...d };
  });
  const axis = { grid: { color: 'rgba(255,255,255,.05)', drawTicks: false }, border: { display: false }, ticks: { color: '#8b94a1', padding: 8 } };
  new Chart(canvas, {
    type,
    data: { labels: spec.labels, datasets },
    options: {
      maintainAspectRatio: false, responsive: true, animation: { duration: 900, easing: 'easeOutQuart' },
      interaction: { mode: round ? 'nearest' : 'index', intersect: round },
      plugins: { legend: { ...legend, display: datasets.length > 1 || round }, tooltip },
      scales: round ? {} : type === 'radar' ? { r: { grid: { color: 'rgba(255,255,255,.07)' }, angleLines: { color: 'rgba(255,255,255,.07)' }, pointLabels: { color: '#c9ced6' }, ticks: { display: false } } } : { x: axis, y: { ...axis, beginAtZero: type === 'bar' } },
      ...spec.options,
    },
  });
}

// ---- uimaxx components, filled with JARVIS's data: ```params, ```ratemodel, ```rates, ```asset ----
// Ports of uimaxx.ing's Protocol Parameters, Interest Rate Model, Interest Rates and Asset Header (their layout,
// sizes, colours and rings; app.css .ux-*): only the data is JARVIS's. A field left out hides its part.
const RICH_BLOCKS = { table: richTable, params: uxParams, ratemodel: uxRateModel, rates: uxRates, asset: uxAsset };
const UX_PATH = { // Phosphor icons (@phosphor-icons/core), 256×256
  info: 'M108,84a16,16,0,1,1,16,16A16,16,0,0,1,108,84Zm128,44A108,108,0,1,1,128,20,108.12,108.12,0,0,1,236,128Zm-24,0a84,84,0,1,0-84,84A84.09,84.09,0,0,0,212,128Zm-72,36.68V132a20,20,0,0,0-20-20,12,12,0,0,0-4,23.32V168a20,20,0,0,0,20,20,12,12,0,0,0,4-23.32Z', // bold
  'info-regular': 'M128,24A104,104,0,1,0,232,128,104.11,104.11,0,0,0,128,24Zm0,192a88,88,0,1,1,88-88A88.1,88.1,0,0,1,128,216Zm16-40a8,8,0,0,1-8,8,16,16,0,0,1-16-16V128a8,8,0,0,1,0-16,16,16,0,0,1,16,16v40A8,8,0,0,1,144,176ZM112,84a12,12,0,1,1,12,12A12,12,0,0,1,112,84Z',
  back: 'M224,128a8,8,0,0,1-8,8H59.31l58.35,58.34a8,8,0,0,1-11.32,11.32l-72-72a8,8,0,0,1,0-11.32l72-72a8,8,0,0,1,11.32,11.32L59.31,120H216A8,8,0,0,1,224,128Z',
  down: 'M50.34,117.66a8,8,0,0,1,11.32-11.32L120,164.69V32a8,8,0,0,1,16,0V164.69l58.34-58.35a8,8,0,0,1,11.32,11.32l-72,72a8,8,0,0,1-11.32,0ZM216,208H40a8,8,0,0,0,0,16H216a8,8,0,0,0,0-16Z',
  up: 'M205.66,138.34a8,8,0,0,1-11.32,11.32L136,91.31V224a8,8,0,0,1-16,0V91.31L61.66,149.66a8,8,0,0,1-11.32-11.32l72-72a8,8,0,0,1,11.32,0ZM216,32H40a8,8,0,0,0,0,16H216a8,8,0,0,0,0-16Z',
  dots: 'M140,128a12,12,0,1,1-12-12A12,12,0,0,1,140,128Zm56-12a12,12,0,1,0,12,12A12,12,0,0,0,196,116ZM60,116a12,12,0,1,0,12,12A12,12,0,0,0,60,116Z',
};
const uxIc = (name, cls) => `<svg class="${cls}" viewBox="0 0 256 256" fill="currentColor" aria-hidden="true"><path d="${UX_PATH[name]}"/></svg>`;
const UX_DIAMOND = '<path d="M12 2.8 17.2 11.5 12 14.62 6.8 11.5 12 2.8Z"/><path d="M6.8 13.36 12 16.48 17.2 13.36 12 20.9 6.8 13.36Z"/>'; // the asset logo
const uxArr = (a) => (Array.isArray(a) ? a.filter((x) => x && typeof x === 'object') : []);
const uxPct = (v) => { const n = parseFloat(v); return Number.isFinite(n) ? Math.max(0, Math.min(100, n)) : null; };
const uxColor = (c, d) => (/^#[0-9a-f]{3,8}$/i.test(c) ? c : d);
const uxHead = (s) => (s.title ? `<div class="ux-h">${esc(s.title)}</div>` : '') + (s.subtitle ? `<div class="ux-sub">${esc(s.subtitle)}</div>` : '');
const uxRing = (pct, r, w, track, arc, size) => { const C = 2 * Math.PI * r; // the donuts and rings
  return `<svg viewBox="0 0 ${size} ${size}" aria-hidden="true" fill="none"><g transform="rotate(-90 ${size / 2} ${size / 2})"><circle cx="${size / 2}" cy="${size / 2}" r="${r}" stroke="${track}" stroke-width="${w}"/>` +
    `<circle cx="${size / 2}" cy="${size / 2}" r="${r}" stroke="${arc}" stroke-width="${w}" stroke-linecap="round" stroke-dasharray="${(pct / 100) * C} ${C}"/></g></svg>`; };

// ```params: {"title", "subtitle", "gauges": [{"label", "pct", "value", "of", "sub", "color"}],
//   "left": [{"label", "value", "secondary", "dotted"}], "right": [...]}  ("rows": [...] fills both columns)
function uxParams(s) {
  const rows = uxArr(s.rows), half = Math.ceil(rows.length / 2);
  const left = s.left ? uxArr(s.left) : rows.slice(0, half), right = s.right ? uxArr(s.right) : rows.slice(half);
  const row = (r) => `<div class="ux-pp-r"><span class="ux-pp-l">${esc(r.label)}${uxIc('info', 'ux-i')}</span><span class="ux-pp-v">` +
    `<span class="${r.dotted ? 'ux-dot4' : ''}">${esc(r.value)}</span>${r.secondary != null ? ` <span class="ux-mut">${esc(r.secondary)}</span>` : ''}</span></div>`;
  const gauges = uxArr(s.gauges).map((g, i) => { const p = uxPct(g.pct ?? g.value) ?? 0;
    return `<div class="ux-pp-gi"><div class="ux-donut">${uxRing(p, 24, 5, '#1c1c1c', uxColor(g.color, ['#34d399', '#ffb37a'][i % 2]), 56)}<span>${esc(g.pct ?? p)}%</span></div>` +
      `<div class="ux-min"><span class="ux-lab">${esc(g.label)}${uxIc('info', 'ux-i')}</span><div class="ux-pp-n"><b>${esc(g.value)}</b>${g.of != null ? `<span> of </span><b>${esc(g.of)}</b>` : ''}</div>` +
      (g.sub ? `<div class="ux-pp-s">${esc(g.sub)}</div>` : '') + '</div></div>'; }).join('');
  const cols = [left, right].filter((c) => c.length).map((c) => `<div>${c.map(row).join('')}</div>`);
  return `<div class="ux-card ux-raise">${uxHead(s)}${gauges ? `<div class="ux-pp-g">${gauges}</div>` : ''}` +
    (cols.length ? `<div class="ux-well ux-pp-w${cols.length > 1 ? ' two' : ''}">${cols.join('')}</div>` : '') + '</div>';
}

// a monotone cubic through svg points (d3's curveMonotoneX): smooth like the source's curve, never overshooting
function uxCurve(P) {
  const n = P.length;
  if (n < 3) return 'M' + P.map((p) => p.join(' ')).join(' L');
  const d = P.slice(1).map((p, i) => (p[1] - P[i][1]) / (p[0] - P[i][0] || 1));
  const m = P.map((_, i) => (i === 0 ? d[0] : i === n - 1 ? d[n - 2] : d[i - 1] * d[i] <= 0 ? 0 : (d[i - 1] + d[i]) / 2));
  d.forEach((dk, k) => { if (!dk) { m[k] = m[k + 1] = 0; return; } const a = m[k] / dk, b = m[k + 1] / dk, h = a * a + b * b; if (h > 9) { const t = 3 / Math.sqrt(h); m[k] = t * a * dk; m[k + 1] = t * b * dk; } });
  const f = (v) => +v.toFixed(1);
  return `M${f(P[0][0])} ${f(P[0][1])}` + P.slice(1).map((p, i) => { const q = P[i], h = (p[0] - q[0]) / 3;
    return ` C${f(q[0] + h)} ${f(q[1] + m[i] * h)}, ${f(p[0] - h)} ${f(p[1] - m[i + 1] * h)}, ${f(p[0])} ${f(p[1])}`; }).join('');
}

// ```ratemodel: {"title", "subtitle", "label", "value", "pct", "params": [{"label", "value"}],
//   "points": [[x, y], …] (the curve, x 0-100), "marker": {"x", "label"} (the dashed line), "current": {"x", "label"}}
function uxRateModel(s) {
  const pct = uxPct(s.pct ?? s.value), params = uxArr(s.params);
  const pts = (Array.isArray(s.points) ? s.points : []).filter((p) => Array.isArray(p) && Number.isFinite(+p[0]) && Number.isFinite(+p[1])).map((p) => [+p[0], +p[1]]).sort((a, b) => a[0] - b[0]);
  let chart = '';
  if (pts.length > 1) {
    const xs = pts.map((p) => p[0]), ys = pts.map((p) => p[1]);
    const x0 = Math.min(...xs), x1 = Math.max(...xs), y0 = Math.min(0, ...ys), y1 = Math.max(...ys);
    const X = (x) => 10 + ((x - x0) / (x1 - x0 || 1)) * 580, Y = (y) => 310 - ((y - y0) / (y1 - y0 || 1)) * 280;
    const pin = (v, dflt) => (v == null ? null : typeof v === 'object' ? { x: +v.x, label: v.label ?? dflt(+v.x) } : { x: +v, label: dflt(+v) });
    const mk = pin(s.marker, () => 'Optimal'), cur = pin(s.current, (x) => `Current ${x}%`);
    let svg = `<path d="${uxCurve(pts.map((p) => [X(p[0]), Y(p[1])]))}" fill="none" stroke="#ffb37a" stroke-width="1.5" stroke-linecap="round"/>`;
    if (mk && Number.isFinite(mk.x)) { const x = X(mk.x), r = x + 10 + String(mk.label).length * 5.5 > 560; // right of the line as in the source; left when it would meet the curve's climb
      svg += `<line x1="${x}" y1="12" x2="${x}" y2="310" stroke="rgba(255,255,255,0.25)" stroke-width="1" stroke-dasharray="3 4"/>` +
        `<text x="${r ? x - 10 : x + 10}" y="70" fill="#737373" font-size="11"${r ? ' text-anchor="end"' : ''}>${esc(mk.label)}</text>`; }
    if (cur && Number.isFinite(cur.x)) { const x = X(cur.x), l = x - 12 > 110; // left of its point, as in the source
      svg += `<text x="${l ? x - 12 : x + 12}" y="150" fill="#f5f5f5" font-size="11" font-weight="500"${l ? ' text-anchor="end"' : ''}>${esc(cur.label)}</text>`; }
    chart = `<div class="ux-well ux-rm-c"><svg viewBox="0 0 600 320" aria-hidden="true">${svg}</svg></div>`;
  }
  const left = (s.label || s.value != null ? `<div class="ux-lab">${esc(s.label || '')}${uxIc('info', 'ux-i')}</div>` +
      `<div class="ux-rm-u">${pct != null ? uxRing(pct, 8, 2, 'rgba(255,255,255,0.1)', '#6d7cff', 20) : ''}<span class="ux-big">${esc(s.value ?? (pct != null ? pct + '%' : ''))}</span></div>` : '') +
    (params.length ? `${s.label || s.value != null ? '<div class="ux-rule"></div>' : ''}<div class="ux-rm-ps">${params.map((p) => `<div class="ux-rm-p"><span class="ux-pp-l">${esc(p.label)}${uxIc('info', 'ux-i')}</span><span class="ux-fg">${esc(p.value)}</span></div>`).join('')}</div>` : '');
  return `<div class="ux-card ux-tight">${uxHead(s)}<div class="ux-split${left && chart ? '' : ' one'}">${left ? `<div>${left}</div>` : ''}${chart}</div></div>`;
}

// ```rates: {"title", "subtitle", "figures": [{"label", "value"}], "chart": "Base Rates", "ranges": ["1D", "1W", …],
//   "range": "1W", "series": [{"label", "data": [numbers], "color"}]}  (series share one scale; the first is drawn on top)
function uxRates(s) {
  const figs = uxArr(s.figures).map((f, i) => `<div>${i ? '<div class="ux-rule ux-rule5"></div>' : ''}<div class="ux-lab ux-hov">${esc(f.label)}${uxIc('info', 'ux-i')}</div><div class="ux-big ux-mt2">${esc(f.value)}</div></div>`).join('');
  const series = uxArr(s.series).map((x, i) => ({ ...x, data: (Array.isArray(x.data) ? x.data : []).map(Number).filter(Number.isFinite), color: uxColor(x.color, ['#34d399', '#ffb37a', ...RICH_COLORS][i % 10]) })).filter((x) => x.data.length > 1);
  const ranges = Array.isArray(s.ranges) ? s.ranges : [];
  let chart = '';
  if (series.length || s.chart || ranges.length) {
    const all = series.flatMap((x) => x.data), lo = Math.min(...all), hi = Math.max(...all);
    const norm = (v) => (hi > lo ? 0.15 + (0.7 * (v - lo)) / (hi - lo) : 0.5); // one scale for all, spread like the source's series
    const line = (x) => `<polyline points="${x.data.map((v, i) => `${((i * 620) / (x.data.length - 1)).toFixed(1)},${(12 + 196 * (1 - norm(v))).toFixed(1)}`).join(' ')}" fill="none" stroke="${x.color}" stroke-width="1.2" stroke-linejoin="round"/>`;
    chart = `<div class="ux-well ux-rt-c"><div class="ux-rt-h"><span>${esc(s.chart || '')}</span><div class="ux-rt-rg">${ranges.map((r) => `<span class="${r === s.range ? 'on' : ''}">${esc(r)}</span>`).join('')}</div></div>` +
      (series.length ? `<svg viewBox="0 0 620 220" aria-hidden="true">${[...series].reverse().map(line).join('')}</svg>` +
        `<div class="ux-rt-lg">${series.map((x) => `<div><i style="background:${x.color}"></i><span>${esc(x.label)}</span></div>`).join('')}</div>` : '') + '</div>';
  }
  return `<div class="ux-card ux-tight">${uxHead(s)}<div class="ux-split${figs && chart ? '' : ' one'}">${figs ? `<div>${figs}</div>` : ''}${chart}</div></div>`;
}

// ```asset: {"back", "name", "symbol", "logo" (a letter or emoji; the source's diamond without it),
//   "meta": ["on", {"text", "dotted", "logo"}], "actions": [{"label", "icon": "down" | "up", "send" | "open" | "url"}],
//   "stats": [{"label", "value", "sub", "pct" (a ring before the value)}]}
function uxAsset(s) {
  const act = (a) => { const icon = UX_PATH[a.icon] && /^(down|up)$/.test(a.icon) ? uxIc(a.icon, 'ux-i4') : '', label = icon + esc(a.label ?? '');
    if (a.url) return `<a class="ux-pill" href="${esc(/^https?:\/\//.test(a.url) ? a.url : '#')}" target="_blank" rel="noopener">${label}</a>`;
    if (a.open) return `<button type="button" class="ux-pill" data-aiopen="${esc(a.open)}">${label}</button>`;
    return `<button type="button" class="ux-pill"${a.send ? ` data-aisend="${esc(a.send)}"` : ''}>${label}</button>`; };
  const actions = uxArr(s.actions);
  const meta = (Array.isArray(s.meta) ? s.meta : []).map((m) => (typeof m === 'object' && m
    ? (m.logo ? `<svg viewBox="0 0 24 24" class="ux-ah-ml" aria-hidden="true">${UX_DIAMOND}</svg>` : '') + `<span class="${m.dotted ? 'ux-dot2' : ''}">${esc(m.text)}</span>`
    : `<span>${esc(m)}</span>`)).join('');
  const stats = uxArr(s.stats).map((t) => { const p = uxPct(t.pct);
    return `<div class="ux-ah-st"><div class="ux-ah-sl">${esc(t.label)}${uxIc('info-regular', 'ux-i')}</div>` +
      (p != null ? `<div class="ux-ah-sv ux-ah-ring">${uxRing(p, 7, 2.5, 'rgba(255,255,255,0.1)', '#6d7cff', 18)}<span>${esc(t.value ?? p + '%')}</span></div>` : `<div class="ux-ah-sv">${esc(t.value)}</div>`) +
      (t.sub ? `<div class="ux-ah-ss">${esc(t.sub)}</div>` : '') + '</div>'; }).join('');
  const id = s.name || s.symbol ? `<div class="ux-ah-id"><span class="ux-ah-logo">${s.logo ? esc(s.logo) : `<svg viewBox="0 0 24 24" fill="#ffffff" aria-hidden="true">${UX_DIAMOND}</svg>`}</span><div>` +
    `<div class="ux-ah-nm"><span class="ux-ah-n">${esc(s.name || '')}</span>${s.symbol ? `<span class="ux-ah-sy">${esc(s.symbol)}</span>` : ''}</div>${meta ? `<div class="ux-ah-meta">${meta}</div>` : ''}</div></div>` : '';
  return `<div class="ux-card">${s.back ? `<div class="ux-ah-back">${uxIc('back', 'ux-i35')}${esc(s.back)}</div>` : ''}` +
    (id || actions.length ? `<div class="ux-ah-top${s.back ? '' : ' first'}">${id}${actions.length ? `<div class="ux-ah-acts">${actions.map(act).join('')}<span class="ux-ah-more">${uxIc('dots', 'ux-i4')}</span></div>` : ''}</div>` : '') +
    (stats ? `<div class="ux-ah-stats" style="--n:${Math.min(5, uxArr(s.stats).length)}">${stats}</div>` : '') + '</div>';
}
