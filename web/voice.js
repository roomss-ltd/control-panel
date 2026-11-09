'use strict';

/* Dictation, in-house (Wispr Flow's way): every chat box gets a mic before its send button. Tap it and talk, tap
   again and the words land where the cursor is; or hold it (or Alt+Space in the box) and let go when done. Esc
   throws the take away. The clip goes to /api/stt, where Whisper runs on this machine (stt.go): nothing leaves it.
   While it listens the mic is a small pill with your voice's level and the time; while it writes, a spinner. */
(() => {
  const MIC = '<svg class="i" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 19v3"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><rect x="9" y="2" width="6" height="13" rx="3"/></svg>'; // lucide Mic (ISC)
  const BARS = 5, HOLD = 300;
  let cur = null; // the take in progress: { btn, box, rec, stream, ctx, raf, t0, cancel }

  const say = (m) => (window.toast ? window.toast(m, true) : console.warn(m));
  const fmt = (s) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`;

  // a mic on every chat box with a send button, now and as the boxes are drawn
  const arm = (root) => {
    for (const tools of root.querySelectorAll?.('.ctools') || []) {
      const send = tools.querySelector('.cbtn.send, [type="submit"]');
      const box = tools.closest('form')?.querySelector('textarea');
      if (!send || !box || tools.querySelector('.cbtn.mic')) continue;
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'cbtn mic';
      b.title = 'Dictate (tap, or hold; Alt+Space)';
      b.setAttribute('aria-label', 'Dictate');
      b.innerHTML = MIC + `<span class="mic-lv" aria-hidden="true">${'<i></i>'.repeat(BARS)}</span><span class="mic-t"></span>`;
      send.before(b);
    }
  };
  arm(document);
  new MutationObserver((ms) => { for (const m of ms) for (const n of m.addedNodes) if (n.nodeType === 1) arm(n.matches?.('.ctools') ? n.parentNode : n); })
    .observe(document.body, { childList: true, subtree: true });

  async function start(btn) {
    if (cur || btn.classList.contains('busy')) return;
    const box = btn.closest('form')?.querySelector('textarea');
    if (!navigator.mediaDevices?.getUserMedia) return say('This browser won\'t give the page a microphone (it needs https).');
    // the glow around the box (the Neon Prompt Bar's, where it has one) breathes with your voice
    const take = cur = { btn, box, chunks: [], cancel: false, glow: btn.closest('.ai-foot, .thp-foot, .dr-foot') };
    btn.classList.add('arm'); // the browser takes a moment to open the mic: it says so (a ripple) instead of showing a dead pill
    btn.setAttribute('aria-label', 'Stop dictating');
    try {
      take.stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } });
    } catch (e) {
      reset(take);
      return say(e.name === 'NotAllowedError' ? 'The microphone is blocked for this site: allow it in the address bar.' : 'No microphone: ' + e.message);
    }
    if (take.cancel) return reset(take); // let go before the browser said yes
    take.rec = new MediaRecorder(take.stream);
    take.rec.ondataavailable = (e) => e.data.size && take.chunks.push(e.data);
    take.rec.onstop = () => finish(take);
    take.rec.start();
    take.t0 = performance.now();
    btn.classList.replace('arm', 'rec'); // listening: the pill opens out
    take.glow?.classList.add('voicing');
    // the level: five bars from the mic's loudness, and the time
    take.ctx = new AudioContext();
    const an = take.ctx.createAnalyser();
    an.fftSize = 256;
    take.ctx.createMediaStreamSource(take.stream).connect(an);
    const buf = new Uint8Array(an.fftSize), bars = btn.querySelectorAll('.mic-lv i'), t = btn.querySelector('.mic-t');
    let sm = 0;
    const draw = () => {
      an.getByteTimeDomainData(buf);
      let sum = 0;
      for (const v of buf) sum += (v - 128) ** 2;
      const lv = Math.min(1, Math.sqrt(sum / buf.length) / 40);
      sm += (lv - sm) * (lv > sm ? 0.45 : 0.1); // the glow: quick to rise with your voice, slow to fall back
      take.glow?.style.setProperty('--vlv', sm.toFixed(3));
      bars.forEach((b, i) => { b.style.transform = `scaleY(${(0.2 + lv * (0.55 + 0.45 * Math.sin(i * 1.7 + performance.now() / 120))).toFixed(2)})`; });
      t.textContent = fmt((performance.now() - take.t0) / 1000);
      take.raf = requestAnimationFrame(draw);
    };
    draw();
  }

  function stop(cancel = false) {
    const take = cur;
    if (!take) return;
    take.cancel = cancel;
    if (take.rec?.state === 'recording') take.rec.stop();
    else if (!take.rec) { take.cancel = true; reset(take); } // still waiting on the permission prompt: the take is off
  }

  function reset(take) {
    cancelAnimationFrame(take.raf);
    take.stream?.getTracks().forEach((t) => t.stop());
    take.ctx?.close();
    take.btn.classList.remove('arm', 'rec', 'busy');
    take.glow?.classList.remove('voicing');
    take.glow?.style.removeProperty('--vlv');
    take.btn.setAttribute('aria-label', 'Dictate');
    if (cur === take) cur = null;
  }

  async function finish(take) {
    const short = performance.now() - take.t0 < 400;
    reset(take);
    if (take.cancel || short || !take.chunks.length) return;
    const btn = take.btn;
    btn.classList.add('busy');
    btn.setAttribute('aria-label', 'Writing what you said');
    try {
      const r = await fetch('/api/stt', { method: 'POST', credentials: 'same-origin', body: new Blob(take.chunks, { type: take.chunks[0].type }) });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || `Dictation failed (${r.status})`);
      if (j.text) insert(take.box, j.text);
    } catch (e) { say(e.message); }
    btn.classList.remove('busy');
    btn.setAttribute('aria-label', 'Dictate');
  }

  // the words go where the cursor is, spaced from what's around them, and the box hears it (send lights up)
  function insert(box, text) {
    if (!box) return;
    const a = box.selectionStart ?? box.value.length, b = box.selectionEnd ?? a;
    const pre = box.value.slice(0, a), post = box.value.slice(b);
    const s = (pre && !/\s$/.test(pre) ? ' ' : '') + text + (post && !/^\s/.test(post) ? ' ' : '');
    box.focus();
    box.setRangeText(s, a, b, 'end');
    box.dispatchEvent(new Event('input', { bubbles: true }));
  }

  // tap: on until tapped again; hold: on while held
  document.addEventListener('pointerdown', (e) => {
    const btn = e.target.closest?.('.cbtn.mic');
    if (!btn || e.button !== 0) return;
    e.preventDefault(); // keep the box's focus and caret
    if (cur?.btn === btn) return stop();
    const at = performance.now();
    start(btn);
    addEventListener('pointerup', () => { if (performance.now() - at > HOLD) stop(); }, { once: true });
  });
  document.addEventListener('click', (e) => { if (e.target.closest?.('.cbtn.mic')) e.preventDefault(); });
  // Alt+Space in a chat box: same as the mic (tap or hold); Esc while listening: throw it away
  let keyAt = 0;
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && cur) { e.preventDefault(); e.stopPropagation(); return stop(true); }
    if (e.code !== 'Space' || !e.altKey || e.repeat) return;
    const mic = e.target.closest?.('form')?.querySelector('.cbtn.mic');
    if (!mic) return;
    e.preventDefault();
    if (cur) return stop();
    keyAt = performance.now();
    start(mic);
  }, true);
  document.addEventListener('keyup', (e) => {
    if ((e.code === 'Space' || e.key === 'Alt') && cur && keyAt && performance.now() - keyAt > HOLD) { keyAt = 0; stop(); }
  }, true);
})();
