'use strict';

/* ══ Slingshot ════════════════════════════════════════════
   Bencho's Slingshot (bencho.dev, MIT licence: bencho.dev/licence), ported from its React source to the panel's
   plain JS. Here it is a way to send: hold a chat's send button and it becomes the ball; pull back and let go, and the
   message goes as the ball flies (Enter and a click still send as ever). Its comments are Bencho's, kept, plus the
   few lines that say what changed for the panel.

   An ink ball resting in an elastic socket in the middle of the
   card. Pull it back and let go: it flies on an arc, bounces off the
   card's own sides and top, settles on the floor and comes back to
   the band.

   ── THE BAND IS THE SOCKET, STRETCHED ───────────────────
   The ball sits in a socket, and pulling it out does not draw a
   string: the socket itself stretches into a capsule that hugs both
   circles — the hull of the two, their outer tangents joined by the
   arcs. The arrow in the ball turns to point where it will go. Let
   go and the band snaps back on a spring, past its rest and home.

   ── THE BAND IS A SPRING, THE FLIGHT IS GRAVITY ─────────
   How far the ball is pulled from its rest, capped on a rubber
   curve, sets the speed it leaves with, straight back the other way,
   and it grows faster than the pull: a full stretch throws hardest.
   From there it is gravity, the walls and the floor. */

/* the ball, its socket and the pull are sized so the slingshot
   reads on the wall; the frame keeps its room to fly */
const SLS_W = 360;
const SLS_H = 260;
const SLS_R = 29;
/* the socket the ball sits in */
const SLS_SOCKET = 38;
const SLS_REST = { x: SLS_W / 2, y: SLS_H / 2 };
const SLS_PULL = 118;
/* the band gives freely up to this share of PULL, then stiffens */
const SLS_FREE = 0.6;
/* how many soft clicks the stretch makes over its whole reach */
const SLS_NOTCHES = 8;
/* the most the ball trembles at full strain, px */
const SLS_SHAKE = 0.7;

const slsClamp = (v, a, b) => Math.min(b, Math.max(a, v));
/* how far the ball goes for how far the hand goes, both over PULL:
   one for one at first, then a steep rubber curve that only creeps
   toward the end however far you drag */
const slsGive = (u) => (u <= SLS_FREE ? u : SLS_FREE + (1 - SLS_FREE) * Math.tanh(((u - SLS_FREE) / (1 - SLS_FREE)) * 0.7));
/* the strain at the end of the band, 0 until nearly taut, 1 at it */
const slsStrainOf = (m) => slsClamp((m - 0.9) / 0.08, 0, 1);
const SLS_K = 13;
/* the panel's flight: a whisper of gravity, air that slows it over a couple of seconds, at most FLY s out, and a
   throw scaled to the window (a full pull crosses a big screen in about half a second) */
const SLS_FLY_G = 140;
const SLS_AIR = 0.75;
const SLS_FLY = 3.2;

/* ── dust ──────────────────────────────────────────────────
   A burst of SPARKS off the sling as the ball leaves it, and only
   then — none on the bounces. Each is a short
   streak along its own flight, longer the faster it goes, so the
   burst reads as thrown rather than dropped. They fan about 100
   degrees and stop short: the first try, 160 degrees and fast, flew
   too wide and too far. Each slows, sinks a little and fades in half a
   second. In the block's own ink, so it stays monochrome and
   inverts with the fill. A fixed pool of circles, drawn into from
   the same loop as the ball — no element is made or thrown away
   while it flies. Off under reduced motion. */
const SLS_POOL = 36;
/* lucide's ArrowUp (ISC licence), drawn as the React component draws it at size 28, stroke 2 */
const SLS_ARROW = '<svg class="sls-arrow" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12 7-7 7 7"/><path d="M12 19V5"/></svg>';

/* Slingshot({ power, bounce, at, onFire, onDone }): one sling, laid over the page with its rest on `at` (a point on
   screen: the send button's middle) and the ball already in the hand of the pointer that is down. onFire() as it is
   let go with a real pull (the panel sends then); onDone() once the ball is back on the band, or let go unpulled
   (the panel takes the sling away). The walls and the floor are the window's edges, not the block's box: the panel's
   send button sits near the bottom of the screen, where the block's own floor would be out of sight. */
function Slingshot({ power = 100, bounce = 30, at, pointer, onFire, onDone }) {
  const box = document.createElement('div');
  box.className = 'sls';
  box.style.cssText = `width:${SLS_W}px;height:${SLS_H}px;left:${Math.round(at.x - SLS_REST.x)}px;top:${Math.round(at.y - SLS_REST.y)}px`;
  box.innerHTML = `<svg class="sls-svg" width="${SLS_W}" height="${SLS_H}" viewBox="0 0 ${SLS_W} ${SLS_H}" aria-hidden="true"><path class="sls-band"/>` +
    `<g class="sls-dust">${'<line opacity="0"/>'.repeat(SLS_POOL)}</g></svg>` +
    `<span class="sls-ball" style="width:${SLS_R * 2}px;height:${SLS_R * 2}px" aria-hidden="true">${SLS_ARROW}</span>`;
  document.body.append(box);
  const ball = box.querySelector('.sls-ball'), band = box.querySelector('.sls-band'), arrow = box.querySelector('.sls-arrow'), dust = box.querySelector('.sls-dust');
  const knobs = { power, bounce };

  const s = {
    x: SLS_REST.x, y: SLS_REST.y, vx: 0, vy: 0,
    phase: 'rest', // rest | held | fly | home (the panel: flying back to the socket)
    /* the band's free end: on the ball while held, then sprung home */
    bx: SLS_REST.x, by: SLS_REST.y, bvx: 0, bvy: 0,
    raf: 0, last: 0, appear: 1, jx: 0, jy: 0,
    /* the side walls and the ceiling: the card's own edges, in the
       block's units — the floor stays the block's own (the panel: the window's bottom, wb) */
    wl: 0, wr: SLS_W, wt: 0, wb: SLS_H,
    grip: null,
    /* the last click the stretch made, and whether the band is taut */
    notch: 0, taut: false, strain: 0,
    specks: [],
    /* how hard the last throw was pulled, 0..1, and whether the
       ball has left the sling yet — the burst waits for that */
    pull: 0, fired: true,
    /* the panel: seconds since it left the sling */
    flown: 0,
  };
  let unhook = () => {};
  /* reduced motion: the band still stiffens and clicks, it does not shake */
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* the speed a pull would give, from where the ball is held */
  /* the further the band is stretched, the more each pixel of it is
     worth: a short tug is a lob, a full stretch nearly twice the old
     top speed (0.7 + 1.1·u², so 0.8x at a third, 1.1x at 60%, 1.8x
     taut) */
  const launch = (x, y) => {
    const u = slsClamp(Math.hypot(SLS_REST.x - x, SLS_REST.y - y) / SLS_PULL, 0, 1);
    const k = SLS_K * (knobs.power / 100) * (0.7 + 1.1 * u * u) * Math.max(1, Math.hypot(innerWidth, innerHeight) / 1100);
    return { vx: (SLS_REST.x - x) * k, vy: (SLS_REST.y - y) * k };
  };

  /* n specks from (x, y), aimed along (dx, dy) and fanned by `spread`
     radians, at about `speed` px/s */
  const puff = (x, y, dx, dy, n, speed, spread) => {
    if (still) return;
    const aim = Math.atan2(dy, dx);
    for (let i = 0; i < n; i++) {
      const a = aim + (Math.random() - 0.5) * spread;
      const v = speed * (0.45 + Math.random() * 0.9);
      s.specks.push({
        x: x + (Math.random() - 0.5) * 10, y: y + (Math.random() - 0.5) * 10,
        vx: Math.cos(a) * v, vy: Math.sin(a) * v,
        t: 0, life: 0.35 + Math.random() * 0.35, r: 1.1 + Math.random() * 0.9,
      });
    }
    if (s.specks.length > SLS_POOL) s.specks.splice(0, s.specks.length - SLS_POOL);
  };

  /* two circles and the outer tangents between them: a capsule that
     hugs both, or the bigger circle alone when one holds the other */
  const hull = (ax, ay, ra, bx, by, rb) => {
    const d = Math.hypot(bx - ax, by - ay);
    const circle = `M${ax - ra},${ay}a${ra},${ra} 0 1,0 ${ra * 2},0a${ra},${ra} 0 1,0 ${-ra * 2},0Z`;
    if (d <= Math.abs(ra - rb) + 0.5) return circle;
    const th = Math.atan2(by - ay, bx - ax);
    const ph = Math.acos(slsClamp((ra - rb) / d, -1, 1));
    const P = (cx, cy, r, t) => `${(cx + r * Math.cos(t)).toFixed(2)},${(cy + r * Math.sin(t)).toFixed(2)}`;
    return `M${P(ax, ay, ra, th + ph)}L${P(bx, by, rb, th + ph)}`
      + `A${rb},${rb} 0 0,0 ${P(bx, by, rb, th - ph)}`
      + `L${P(ax, ay, ra, th - ph)}`
      + `A${ra},${ra} 0 1,0 ${P(ax, ay, ra, th + ph)}Z`;
  };

  const paint = () => {
    const sp = Math.hypot(s.vx, s.vy);
    const st = s.phase === 'fly' || s.phase === 'home' ? slsClamp(sp / 2400, 0, 0.22) : 0;
    const ang = Math.atan2(s.vy, s.vx);
    /* held at the limit, the ball trembles in the hand: two sines
       that never line up, scaled by how hard the band is strained */
    const j = s.phase === 'held' && !still ? s.strain * SLS_SHAKE : 0;
    const t = performance.now() / 1000;
    s.jx = j * Math.sin(t * 60); s.jy = j * Math.cos(t * 49);
    ball.style.transform =
      `translate(${(s.x + s.jx - SLS_R).toFixed(2)}px, ${(s.y + s.jy - SLS_R).toFixed(2)}px) rotate(${ang}rad) scale(${(1 + st) * s.appear}, ${(1 - st * 0.7) * s.appear}) rotate(${-ang}rad)`;
    /* the arrow: where a pull would send it, or the way it is flying */
    const dir = s.phase === 'fly' || s.phase === 'home'
      ? Math.atan2(s.vy, s.vx)
      : s.phase === 'held' && Math.hypot(SLS_REST.x - s.x, SLS_REST.y - s.y) > 4
        ? Math.atan2(SLS_REST.y - s.y, SLS_REST.x - s.x)
        : -Math.PI / 2; // the panel: at rest it points up, as the send button's arrow does (Bencho's rests at -π/4)
    arrow.style.transform = `rotate(${dir + Math.PI / 2}rad)`;
    /* the dust: the pool's circles, the live specks first */
    const cs = dust.children;
    for (let i = 0; i < cs.length; i++) {
      const c = cs[i];
      const p = s.specks[i];
      if (!p) { c.setAttribute('opacity', '0'); continue; }
      const k = p.t / p.life;
      /* the streak trails behind the spark, its length its speed */
      const v = Math.hypot(p.vx, p.vy) || 1;
      const len = slsClamp(v * 0.028, 1.5, 12);
      c.setAttribute('x1', p.x.toFixed(1));
      c.setAttribute('y1', p.y.toFixed(1));
      c.setAttribute('x2', (p.x - (p.vx / v) * len).toFixed(1));
      c.setAttribute('y2', (p.y - (p.vy / v) * len).toFixed(1));
      c.setAttribute('stroke-width', p.r.toFixed(2));
      c.setAttribute('opacity', (0.6 * (1 - k) ** 1.4).toFixed(3));
    }
    /* the band: the socket's hull, stretched to its free end */
    band.setAttribute('d', hull(SLS_REST.x, SLS_REST.y, SLS_SOCKET, s.bx + (s.phase === 'held' ? s.jx : 0), s.by + (s.phase === 'held' ? s.jy : 0), SLS_R + 6));
  };

  let finishing = false;
  const finish = () => { if (finishing) return; finishing = true; cancelAnimationFrame(s.raf); s.raf = 0; unhook(); box.remove(); onDone?.(); };

  const step = (dt) => {
    if (s.appear < 1) s.appear = Math.min(1, s.appear + dt * 5);
    /* the band's end: on the ball in the hand, sprung home otherwise */
    let banding = false;
    if (s.phase === 'held') { s.bx = s.x; s.by = s.y; s.bvx = s.bvy = 0; }
    else {
      const k = 900, c = 2 * Math.sqrt(k) * 0.32;
      s.bvx += (-k * (s.bx - SLS_REST.x) - c * s.bvx) * dt;
      s.bvy += (-k * (s.by - SLS_REST.y) - c * s.bvy) * dt;
      s.bx += s.bvx * dt; s.by += s.bvy * dt;
      banding = Math.abs(s.bx - SLS_REST.x) + Math.abs(s.by - SLS_REST.y) + Math.abs(s.bvx) * 0.01 + Math.abs(s.bvy) * 0.01 > 0.05;
      if (!banding) { s.bx = SLS_REST.x; s.by = SLS_REST.y; s.bvx = s.bvy = 0; }
    }
    /* specks drift on, slowing, sinking a touch, until they fade */
    if (s.specks.length) {
      const drag = Math.exp(-4 * dt);
      for (const p of s.specks) {
        p.t += dt;
        p.vx *= drag;
        p.vy = p.vy * drag + 60 * dt;
        p.x += p.vx * dt;
        p.y += p.vy * dt;
      }
      s.specks = s.specks.filter((p) => p.t < p.life);
    }
    const dusty = s.specks.length > 0;
    if (s.phase !== 'fly' && s.phase !== 'home') return s.appear < 1 || banding || dusty;
    /* the moment the ball crosses the sling's middle on its way out:
       the burst comes off the SLING, at the rim the ball leaves
       through, fanned along the throw — not off the ball where it
       was held. More, and faster, for a harder pull */
    if (!s.fired && (s.x - SLS_REST.x) * s.vx + (s.y - SLS_REST.y) * s.vy >= 0) {
      s.fired = true;
      const sp = Math.hypot(s.vx, s.vy) || 1;
      const ux = s.vx / sp, uy = s.vy / sp;
      puff(SLS_REST.x + ux * SLS_SOCKET, SLS_REST.y + uy * SLS_SOCKET, ux, uy, Math.round(12 + s.pull * 10), 150 + s.pull * 170, 1.7);
    }
    /* the panel: the ball is the send button and the card is the whole window, so it doesn't drop to a floor a few
       pixels under the button: it glides across the screen, nearly weightless, off all four edges, with a little
       air to slow it; once it has spent its throw (or after FLY s) it flies home into the socket on a spring */
    if (s.phase === 'home') {
      const k = 55, c = 2 * Math.sqrt(k) * 0.85;
      s.vx += (-k * (s.x - SLS_REST.x) - c * s.vx) * dt;
      s.vy += (-k * (s.y - SLS_REST.y) - c * s.vy) * dt;
      s.x += s.vx * dt; s.y += s.vy * dt;
      if (Math.hypot(s.x - SLS_REST.x, s.y - SLS_REST.y) < 1.5 && Math.hypot(s.vx, s.vy) < 40) {
        /* caught: the socket gives a little with it */
        s.bvx = s.vx * 0.6; s.bvy = s.vy * 0.6;
        s.phase = 'rest'; s.x = SLS_REST.x; s.y = SLS_REST.y; s.vx = s.vy = 0;
      }
      return true;
    }
    const e = 0.82;
    s.flown += dt;
    s.vy += SLS_FLY_G * dt;
    const drag = Math.exp(-SLS_AIR * dt);
    s.vx *= drag; s.vy *= drag;
    s.x += s.vx * dt;
    s.y += s.vy * dt;
    /* a fast ball is drawn stretched along its flight: meet the card's
       edge with the tip of the stretch, not its middle, or the edge
       clips it */
    const m = SLS_R * (1 + slsClamp(Math.hypot(s.vx, s.vy) / 2400, 0, 0.22));
    if (s.x < s.wl + m) { s.x = s.wl + m; s.vx = Math.abs(s.vx) * e; }
    if (s.x > s.wr - m) { s.x = s.wr - m; s.vx = -Math.abs(s.vx) * e; }
    if (s.y < s.wt + m) { s.y = s.wt + m; s.vy = Math.abs(s.vy) * e; }
    if (s.y > s.wb - m) { s.y = s.wb - m; s.vy = -Math.abs(s.vy) * e; }
    if (s.flown > SLS_FLY || Math.hypot(s.vx, s.vy) < 260) s.phase = 'home';
    return true;
  };

  const wake = () => {
    if (s.raf || finishing) return;
    s.last = performance.now();
    const tick = (now) => {
      const dt = Math.min(0.033, (now - s.last) / 1000);
      s.last = now;
      let busy = false;
      for (let i = 0; i < 3; i++) busy = step(dt / 3) || busy;
      paint();
      if (!busy && s.phase !== 'held') { s.raf = 0; if (s.phase === 'rest') finish(); return; } // the panel: home again, put away
      s.raf = requestAnimationFrame(tick);
    };
    s.raf = requestAnimationFrame(tick);
  };

  /* the walls are the card the block sits on (the wall card's stage,
     or the overlay's), measured at each throw: the ball bounces off
     the edges you can see rather than the block's own box. Never
     inside the block, so a small stage cannot pinch the rest.
     The panel: the card is the window, floor included */
  const walls = () => {
    const o = box.getBoundingClientRect();
    const k = o.width / SLS_W || 1;
    s.wl = Math.min(0, -o.left / k);
    s.wr = Math.max(SLS_W, (innerWidth - o.left) / k);
    s.wt = Math.min(0, -o.top / k);
    s.wb = (innerHeight - o.top) / k - 8;
  };

  const local = (e) => {
    const o = box.getBoundingClientRect();
    const k = o.width / SLS_W || 1;
    return { x: (e.clientX - o.left) / k, y: (e.clientY - o.top) / k };
  };

  /* the panel: no press on the ball here; the hand is already down (on the send button), so it starts held */
  const p0 = local(pointer);
  s.grip = { id: pointer.pointerId, real: pointer.isTrusted, dx: s.x - p0.x, dy: s.y - p0.y };
  s.phase = 'held';
  const mv = (ev) => {
    const g = s.grip;
    if (!g || ev.pointerId !== g.id || ev.isTrusted !== g.real) return;
    const q = local(ev);
    let dx = q.x + g.dx - SLS_REST.x, dy = q.y + g.dy - SLS_REST.y;
    const d = Math.hypot(dx, dy);
    /* the band gives freely, then stiffens hard near its end */
    const m = slsGive(d / SLS_PULL);
    if (d > 0) { dx *= (m * SLS_PULL) / d; dy *= (m * SLS_PULL) / d; }
    s.x = SLS_REST.x + dx; s.y = SLS_REST.y + dy;
    s.strain = slsStrainOf(m);
    /* the stretch is heard: a soft click every notch, higher the
       further it goes, and a firmer knock as it goes taut */
    const n = Math.floor(m * SLS_NOTCHES);
    if (n !== s.notch) { s.notch = n; }
    const taut = s.strain > 0.5;
    s.taut = taut;
    wake();
  };
  const up = (ev) => {
    const g = s.grip;
    if (!g || ev.pointerId !== g.id || ev.isTrusted !== g.real) return;
    s.grip = null;
    unhook();
    s.notch = 0; s.taut = false; s.strain = 0;
    const pulled = Math.hypot(SLS_REST.x - s.x, SLS_REST.y - s.y);
    if (pulled < 8 || ev.type === 'pointercancel') { s.phase = 'rest'; s.x = SLS_REST.x; s.y = SLS_REST.y; wake(); return; }
    const v = launch(s.x, s.y);
    s.vx = v.vx; s.vy = v.vy;
    walls();
    s.phase = 'fly';
    /* the burst waits until the ball clears the sling — see step() */
    s.pull = slsClamp(pulled / SLS_PULL, 0, 1);
    s.fired = false;
    s.flown = 0;
    if (still) s.phase = 'home'; // reduced motion: the message goes, the ball doesn't tour the screen
    onFire?.();
    wake();
  };
  addEventListener('pointermove', mv);
  addEventListener('pointerup', up);
  addEventListener('pointercancel', up);
  unhook = () => {
    removeEventListener('pointermove', mv);
    removeEventListener('pointerup', up);
    removeEventListener('pointercancel', up);
    unhook = () => {};
  };
  paint();
  wake();
}

/* the panel's send buttons (JARVIS's, an open thread's and the agent chat's): a click sends as ever; held (a beat, or dragged off), the
   button becomes the sling's ball, and letting go with a pull sends the form as the ball flies */
document.addEventListener('pointerdown', (e) => {
  const btn = e.target.closest?.('#ai-send, #thp-send, #dr-send');
  if (!btn || btn.disabled || btn.classList.contains('stop') || e.button !== 0 || document.querySelector('.sls')) return;
  const form = btn.form;
  let timer = 0, done = false;
  const start = (ev) => {
    if (done) return;
    done = true; clearTimeout(timer); off();
    const r = btn.getBoundingClientRect();
    btn.classList.add('slung');
    // the click that follows letting go is the sling's, not the button's
    const eat = (c) => { c.preventDefault(); c.stopPropagation(); };
    addEventListener('click', eat, { capture: true, once: true });
    setTimeout(() => removeEventListener('click', eat, { capture: true }), 600);
    Slingshot({ at: { x: r.left + r.width / 2, y: r.top + r.height / 2 }, pointer: ev,
      onFire: () => form?.requestSubmit(), onDone: () => btn.classList.remove('slung') });
  };
  const mv = (ev) => { if (ev.pointerId === e.pointerId && Math.hypot(ev.clientX - e.clientX, ev.clientY - e.clientY) > 6) start(ev); };
  const up = () => { done = true; clearTimeout(timer); off(); }; // a click: the button's own
  const off = () => { removeEventListener('pointermove', mv); removeEventListener('pointerup', up); removeEventListener('pointercancel', up); };
  addEventListener('pointermove', mv);
  addEventListener('pointerup', up);
  addEventListener('pointercancel', up);
  timer = setTimeout(() => start(e), 220);
  e.preventDefault(); // no text selection or focus loss while it's held
}, true);
