'use strict';

/* ══ Eye tracker ═══════════════════════════════════════════
   Bencho's "Eye tracker" (its Watcher; bencho.dev, MIT licence: bencho.dev/licence), ported from its React source to
   the panel's plain JS. Here it is JARVIS's mark in the nav: it watches the pointer near the nav, and its colour, shape
   and eyes say how JARVIS is (jarvisMood, at the end). Bencho's comments are kept; the lines that say "the panel"
   are what changed for it. Left out: the chat bubble a poke opens (a press on the nav mark opens JARVIS instead).

   A ball with two eyes that watches your cursor, wherever it is
   on the page.

   ── THE EYES ARE ON THE BALL, NOT IN FRONT OF IT ───────────
   The easy version slides two shapes toward the pointer, and it
   reads as stickers on a disc. Here each eye is a point on the
   sphere's surface: looking left turns the ball, so an eye
   travels along the curve, and the one heading for the rim
   moves less and narrows as it turns away from you while the
   other swings wide. That foreshortening is the whole trick —
   it is what makes a flat circle read as something round.

   One turn (yaw and pitch) is the state, sprung toward where
   the pointer is; both eyes are read off it, so they can never
   disagree. It relaxes back to facing you when the pointer
   leaves the window, and it blinks on its own now and then.

   Written straight to the DOM every frame, never through state:
   a pointer move is not a render. */

/* Read once, the way the wheel and the pill nav do. A
   preference, not a live input. */
const eytStillness = () => !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;

const EYT_R = 50; /* the ball's radius, in the 100-unit viewBox */

/* ── the eyes: a style, and where they rest ─────────────────
   Every style is two rounded rectangles, the same size as each
   other — the difference between styles is only their width,
   height and corner — so they all ride the same sphere and
   blink the same way. Any difference in SIZE between the two is
   depth, worked out from the turn, never drawn in.

   The face goes where you are: straight on, centred and level
   when you are on top of it, and it comes back to centre when
   the pointer leaves the window. */
const EYT_STYLES = {
  Slant: { w: 9, h: 15, r: 4.5 },
  Dots: { w: 11, h: 11, r: 5.5 },
  Squares: { w: 12, h: 12, r: 3.5 },
};
/* half the gap between the eyes, as a fraction of the radius */
const EYT_GAP = 0.19;
/* how far a look to a corner tilts the pair, in degrees at the
   rim — a glance up and right leans them back, like the logo */
const EYT_TILT = 64;

const eytClamp = (v, a, b) => Math.min(b, Math.max(a, v));

/* ── the body, in three shapes ──────────────────────────────
   The eyes are worked out on a sphere whatever the body is, so
   the turn reads the same on all three; the body is only the
   outline they are clipped to. The cube is a soft square, the
   pill lies on its side — both keep the eyes' resting spot
   inside them. */
const EYT_SHAPES = {
  Ball: 'M50 0 A50 50 0 1 1 49.99 0 Z',
  /* a smaller circle, the same area as the hexagon, for the mark
     that changes between the two */
  Circle: 'M50 7 A43 43 0 1 1 49.99 7 Z',
  Cube: 'M30 6 H70 A24 24 0 0 1 94 30 V70 A24 24 0 0 1 70 94 H30 A24 24 0 0 1 6 70 V30 A24 24 0 0 1 30 6 Z',
  Pill: 'M34 17 H66 A33 33 0 0 1 66 83 H34 A33 33 0 0 1 34 17 Z',
  /* six sides with softened corners: stood on a point, and lying
     on a flat side */
  Hexagon: 'M59.53 8.50 L81.18 21.00 Q90.70 26.50 90.70 37.50 L90.70 62.50 Q90.70 73.50 81.18 79.00 L59.53 91.50 Q50.00 97.00 40.47 91.50 L18.82 79.00 Q9.30 73.50 9.30 62.50 L9.30 37.50 Q9.30 26.50 18.82 21.00 L40.47 8.50 Q50.00 3.00 59.53 8.50 Z',
  'Hex flat': 'M91.50 59.53 L79.00 81.18 Q73.50 90.70 62.50 90.70 L37.50 90.70 Q26.50 90.70 21.00 81.18 L8.50 59.53 Q3.00 50.00 8.50 40.47 L21.00 18.82 Q26.50 9.30 37.50 9.30 L62.50 9.30 Q73.50 9.30 79.00 18.82 L91.50 40.47 Q97.00 50.00 91.50 59.53 Z',
};

/* ── morphing between shapes ────────────────────────────────
   The shapes are written as different kinds of path — arcs for
   the ball and the pill, lines and curves for the hexagon — so
   they cannot be tweened as they are. Each is read instead as
   its distance from the centre at a set of evenly spaced angles
   (they are all star-shaped about the centre, so a ray from the
   middle crosses each edge once), and a morph is those distances
   eased from one set to the other and drawn back out as a path.
   Measured once per shape, off the browser's own hit-testing,
   and kept. */
const EYT_RAYS = 120;
const eytRadii = new Map();

function eytMeasure(shape) {
  const hit = eytRadii.get(shape);
  if (hit) return hit;
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 100 100');
  svg.style.cssText = 'position:absolute;width:0;height:0;overflow:hidden';
  const path = document.createElementNS(ns, 'path');
  path.setAttribute('d', EYT_SHAPES[shape] ?? EYT_SHAPES.Ball);
  svg.appendChild(path);
  document.body.appendChild(svg);
  const pt = svg.createSVGPoint();
  const out = [];
  for (let i = 0; i < EYT_RAYS; i++) {
    const a = (i / EYT_RAYS) * Math.PI * 2 - Math.PI / 2;
    let lo = 0, hi = 60;
    for (let k = 0; k < 16; k++) {
      const mid = (lo + hi) / 2;
      pt.x = 50 + Math.cos(a) * mid;
      pt.y = 50 + Math.sin(a) * mid;
      if (path.isPointInFill(pt)) lo = mid; else hi = mid;
    }
    out.push(lo);
  }
  svg.remove();
  eytRadii.set(shape, out);
  return out;
}

function eytOutline(r) {
  let d = '';
  r.forEach((v, i) => {
    const a = (i / r.length) * Math.PI * 2 - Math.PI / 2;
    d += `${i ? 'L' : 'M'}${(50 + Math.cos(a) * v).toFixed(2)} ${(50 + Math.sin(a) * v).toFixed(2)}`;
  });
  return d + 'Z';
}

let eytN = 0;

/* EyeTracker(host, opts): the watcher, drawn into `host`. Returns { set(opts), lookAt(el), destroy() }: set changes
   any knob (a new shape morphs, the rest apply on the next frame); lookAt(el) aims the face at an element on the page
   (the panel: the nav row that needs the owner) until lookAt(null), the pointer near it still winning. */
function EyeTracker(host, opts = {}) {
  const o = {
    /* how far the ball turns toward the pointer, 0..100 */
    follow: 60,
    /* how much the turn overshoots and settles, 0..100 */
    bounce: 30,
    size: 100,
    shape: 'Cube',
    eyes: 'Slant',
    /* the eyes' size against the body; a small mark wants them
       bigger than a big one does to read at all */
    eyeScale: 1,
    /* the eyes' width on its own, for a thinner or fuller stroke */
    eyeWidth: 1,
    /* how round the eyes' corners are, 1 fully round; below 1 they
       start to square off */
    eyeRound: 1,
    /* the eyes' height on its own */
    eyeHeight: 1,
    /* a mark rather than a toy: it ignores the pointer, looks
       straight ahead, glances somewhere every five seconds and
       blinks every two */
    idle: false,
    /* the box it watches inside, as a selector; left unset it is the
       card, stage or board it sits in */
    within: '',
    /* the panel: how far under the ball it still watches, in px; past that it faces front (the nav: only its top part) */
    below: Infinity,
    ...opts,
  };
  const still = eytStillness();
  const clip = `eyt-clip-${++eytN}`;
  const box = document.createElement('div');
  box.className = 'eyt';
  box.setAttribute('role', 'img');
  box.setAttribute('aria-label', 'JARVIS, watching');
  box.innerHTML = `<svg class="eyt-ball" viewBox="0 0 100 100"><defs><clipPath id="${clip}"><path/></clipPath>` +
    `<linearGradient id="${clip}-f" x1="0" y1="0" x2="1" y2="1"><stop offset="0" class="eyt-s1"/><stop offset=".48" class="eyt-s2"/><stop offset="1" class="eyt-s3"/></linearGradient>` +
    `<radialGradient id="${clip}-h" cx=".15" cy=".05" r="1.2"><stop offset="0" class="eyt-h"/><stop offset=".34" class="eyt-h" stop-opacity="0"/></radialGradient></defs>` +
    `<path class="eyt-body" style="--eyt-grad:url(#${clip}-f)"/>` +
    `<g clip-path="url(#${clip})"><rect class="eyt-sheen" width="100" height="100" fill="url(#${clip}-h)"/><g><rect class="eyt-eye"/></g><g><rect class="eyt-eye"/></g></g></svg>`;
  host.replaceChildren(box);
  const bodyEl = box.querySelector('.eyt-body'), clipEl = box.querySelector('clipPath path');
  const eyes = [...box.querySelectorAll('g[clip-path] > g')];
  const eyeOf = () => {
    const base = EYT_STYLES[o.eyes] ?? EYT_STYLES.Slant;
    return { w: base.w * o.eyeScale * o.eyeWidth, h: base.h * o.eyeScale * o.eyeHeight, r: base.r * o.eyeScale * o.eyeWidth * o.eyeRound };
  };
  const sizeIt = () => box.style.setProperty('--eyt-size', `${eytClamp(o.size, 16, 180)}px`);
  sizeIt();
  let shown = null, from = o.shape, morphRaf = 0;
  bodyEl.setAttribute('d', EYT_SHAPES[o.shape] ?? EYT_SHAPES.Ball);
  clipEl.setAttribute('d', EYT_SHAPES[o.shape] ?? EYT_SHAPES.Ball);

  /* ── a change of shape is a morph, not a swap ─────────────
     The first shape is drawn from its own path; every change
     after that eases the outline across in about half a second,
     with a little overshoot so it lands. Both the body and the
     clip the eyes sit in are written each frame. */
  const morph = (shape) => {
    if (from === shape) return;
    const start = shown ?? eytMeasure(from);
    const end = eytMeasure(shape);
    from = shape;
    cancelAnimationFrame(morphRaf);
    if (still) {
      shown = end;
      const d = eytOutline(end);
      bodyEl.setAttribute('d', d);
      clipEl.setAttribute('d', d);
      return;
    }
    const t0 = performance.now();
    const ms = 520;
    const step = (now) => {
      const u = Math.min(1, (now - t0) / ms);
      /* ease out with a touch of overshoot */
      const c = 1.3;
      const e = 1 + (c + 1) * Math.pow(u - 1, 3) + c * Math.pow(u - 1, 2);
      const r = start.map((a, i) => a + (end[i] - a) * e);
      shown = r;
      const d = eytOutline(r);
      bodyEl.setAttribute('d', d);
      clipEl.setAttribute('d', d);
      if (u < 1) morphRaf = requestAnimationFrame(step);
    };
    morphRaf = requestAnimationFrame(step);
  };

  /* where the face is on the ball (fractions of the radius),
     its speed, and where it is heading — one spring on a point,
     read as a turn when the eyes are drawn */
  const t = { x: 0, y: 0, vx: 0, vy: 0, tx: 0, ty: 0 };
  let raf = 0;
  let prev = 0;
  let blink = 0;
  let nextBlink = performance.now() + 2200;
  let target = null; // the panel: an element it looks at, when the pointer isn't near

  const draw = (now) => {
    /* a blink is 160ms down and up, as a squash on each eye */
    let lid = 1;
    if (now > nextBlink) {
      blink = now;
      nextBlink = now + (o.idle ? 2000 : 2600 + Math.random() * 3200);
    }
    const since = now - blink;
    if (since < 160) lid = 1 - 0.9 * Math.sin((since / 160) * Math.PI);

    /* the turn that brings the front of the ball to the face's
       spot: yaw about the vertical, pitch about the horizontal */
    const as = (v) => Math.asin(eytClamp(v, -0.92, 0.92));
    const yaw = as(t.x);
    const pitch = -as(t.y);
    const cy = Math.cos(yaw), sy = Math.sin(yaw);
    const cp = Math.cos(pitch), sp = Math.sin(pitch);
    /* the pair leans with a diagonal look and stands level on
       the axes: up-right and down-left lean one way, the other
       two the other, nothing straight on */
    const tilt = EYT_TILT * t.x * t.y;
    /* bigger eyes stand a little further apart */
    const g0 = EYT_GAP * Math.max(1, o.eyeScale * 0.8);
    const e0 = eyeOf();

    [-g0, g0].forEach((x0, i) => {
      const g = eyes[i];
      /* the eye as a point on the unit sphere, either side of
         the front, turned with the ball */
      const z0 = Math.sqrt(1 - x0 * x0);
      const x1 = x0 * cy + z0 * sy;
      const z1 = -x0 * sy + z0 * cy;
      const y2 = -z1 * sp;
      const z2 = z1 * cp;
      /* depth: the eye nearer the rim is further from you, so it
         is smaller all over and narrower still across the turn */
      const near = eytClamp(z2, 0, 1);
      const k = 0.45 + 0.55 * near;
      const sx = k * (0.7 + 0.3 * near);
      /* ── the size is written as a size, never as a scale ──
         A scale stretches the corner with the shape: an eye
         narrowed for depth or squashed by a blink had elliptical
         corners, rounder on one side than the other. Writing the
         width and height directly keeps every corner the same
         radius, clamped so it never exceeds half a side. */
      const w = e0.w * sx;
      const h = Math.max(0.6, e0.h * k * lid);
      const rect = g.firstElementChild;
      rect.setAttribute('x', (-w / 2).toFixed(2));
      rect.setAttribute('y', (-h / 2).toFixed(2));
      rect.setAttribute('width', w.toFixed(2));
      rect.setAttribute('height', h.toFixed(2));
      rect.setAttribute('rx', Math.min(e0.r, w / 2, h / 2).toFixed(2));
      g.setAttribute('transform', `translate(${(50 + x1 * EYT_R).toFixed(2)} ${(50 + y2 * EYT_R).toFixed(2)}) rotate(${tilt.toFixed(2)})`);
      g.style.opacity = z2 < 0.05 ? '0' : '1';
    });
  };

  /* ── it only runs while it can be seen ───────────────────
     Off screen or in a background tab the loop stops; it starts
     again the moment the ball comes back into view */
  let seen = true;
  const tick = (now) => {
    if (!seen || document.hidden) { raf = 0; prev = 0; return; }
    const dt = prev ? Math.min(2.5, (now - prev) / 16.67) : 1;
    prev = now;
    if (still) {
      t.x = t.tx;
      t.y = t.ty;
    } else {
      /* one spring for both axes; Bounce loosens the damping */
      const k = 0.06;
      const d = 0.34 - (eytClamp(o.bounce, 0, 100) / 100) * 0.22;
      t.vx += ((t.tx - t.x) * k - t.vx * d) * dt;
      t.vy += ((t.ty - t.y) * k - t.vy * d) * dt;
      t.x += t.vx * dt;
      t.y += t.vy * dt;
    }
    draw(now);
    raf = requestAnimationFrame(tick);
  };
  raf = requestAnimationFrame(tick);
  const resume = () => { if (!raf && seen && !document.hidden) raf = requestAnimationFrame(tick); };
  const io = new IntersectionObserver(([e]) => { seen = e.isIntersecting; resume(); });
  io.observe(box);
  document.addEventListener('visibilitychange', resume);

  /* ── the face goes where you are ──────────────────────────
     A spot out along the pointer's direction, further the
     further away you are — so on top of it the face is dead
     centre, and off in a corner it is out by that corner's
     rim, turned, one eye smaller and the pair leaning. */
  /* ── only while you are in its card ─────────────────────
     It watches the pointer while the pointer is inside the
     card, stage or board it sits in, and faces front again the
     moment you leave — a wall of these all turning after a
     cursor crossing the page read as noise. The reach is
     measured against that card, so the eyes arrive at the rim
     as you arrive at its edge. */
  const frame = () => (o.within && box.closest(o.within)) || box;
  const toward = (x, y, f) => {
    const r = box.getBoundingClientRect();
    const dx = x - (r.left + r.width / 2);
    const dy = y - (r.top + r.height / 2);
    const d = Math.hypot(dx, dy) || 1;
    const reach = Math.max(60, Math.min(f.width, f.height) * 0.28);
    const far = Math.min(0.85, Math.tanh(d / reach) * (eytClamp(o.follow, 0, 100) / 100) * 1.5);
    t.tx = (dx / d) * far;
    t.ty = (dy / d) * far;
    resume();
  };
  let near = false;
  const aim = (e) => {
    if (o.idle) return;
    const f = frame().getBoundingClientRect();
    near = !(e.clientX < f.left || e.clientX > f.right || e.clientY < f.top || e.clientY > f.bottom ||
      e.clientY > box.getBoundingClientRect().bottom + o.below);
    if (!near) return rest();
    toward(e.clientX, e.clientY, f);
  };
  /* the panel: with nothing to watch it faces front, or looks at what it was asked to */
  const rest = () => {
    if (target?.isConnected) { // a glance its way, not a stare: half as far as the pointer would take the eyes
      const r = target.getBoundingClientRect();
      toward(r.left + r.width / 2, r.top + r.height / 2, frame().getBoundingClientRect());
      t.tx *= 0.5; t.ty *= 0.5;
      return;
    }
    t.tx = 0; t.ty = 0; resume();
  };
  const leave = () => { near = false; rest(); };
  addEventListener('pointermove', aim, { passive: true });
  document.documentElement.addEventListener('pointerleave', leave);

  /* ── idle: straight ahead, and now and then a glance ──────
     Every 5 seconds it looks off in a random direction,
     holds it for a moment, and comes back to centre. */
  let glance = 0;
  const look = () => {
    if (!o.idle) return;
    const a = Math.random() * Math.PI * 2;
    const far = 0.45 + Math.random() * 0.25;
    t.tx = Math.cos(a) * far;
    t.ty = Math.sin(a) * far;
    resume();
    glance = setTimeout(() => { rest(); glance = setTimeout(look, 5000 - 900); }, 900);
  };
  if (o.idle) glance = setTimeout(look, 5000);

  return {
    set(next) {
      const wasIdle = o.idle;
      Object.assign(o, next);
      sizeIt();
      if (next.shape) morph(next.shape);
      if (o.idle && !wasIdle) { clearTimeout(glance); glance = setTimeout(look, 1200); }
      if (!o.idle && wasIdle) { clearTimeout(glance); rest(); }
      resume();
    },
    lookAt(el) { target = el || null; if (!near) rest(); },
    destroy() {
      cancelAnimationFrame(raf); cancelAnimationFrame(morphRaf); clearTimeout(glance);
      io.disconnect();
      document.removeEventListener('visibilitychange', resume);
      removeEventListener('pointermove', aim);
      document.documentElement.removeEventListener('pointerleave', leave);
      box.remove();
    },
  };
}

/* ── the panel: JARVIS's mark in the nav ───────────────────────
   The J.A.R.V.I.S row's icon becomes the watcher (in the nav's [data-eyt] slot; the nav hides its sparkle then). It watches the pointer anywhere over the nav, and says how JARVIS
   is by its colour, shape and eyes (.eyt[data-mood] in app.css):
     calm      a soft square, slanted eyes, the panel's ink: all quiet
     thinking  a ball, round eyes glancing about, violet: a turn running, here or in a thread
     needs     a hexagon, tall eyes, yellow: a thread or JARVIS is waiting on the owner; it looks at the row that needs them
     blocked   a pill, squinting, orange: the last turn failed
     error     flat hexagon, square eyes squinting, red: Hermes can't be reached
   Read off the page's own state (AI, the threads) every second; nothing new is fetched. */
const EYT_MOODS = {
  calm: { shape: 'Cube', eyes: 'Slant', eyeHeight: 1, idle: false },
  thinking: { shape: 'Ball', eyes: 'Dots', eyeHeight: 1, idle: true },
  needs: { shape: 'Hexagon', eyes: 'Slant', eyeHeight: 1.2, idle: false },
  blocked: { shape: 'Pill', eyes: 'Squares', eyeHeight: 0.5, idle: false },
  error: { shape: 'Hex flat', eyes: 'Squares', eyeHeight: 0.4, idle: false },
};
function jarvisMood() {
  if (typeof AI === 'undefined') return 'calm';
  if (AI.state?.err) return 'error';
  const ths = AI.threads || [];
  if (ths.some((t) => t.status === 'needs_you' && !t.archived)) return 'needs';
  if (AI.live || ths.some((t) => t.live)) return 'thinking';
  const last = (AI.items || [])[AI.items.length - 1];
  if (last?.role === 'error') return 'blocked';
  return 'calm';
}
(function jarvisMark() {
  const start = () => {
    const mk = document.querySelector('.nav-btn[data-view="ai"] [data-eyt]'); // the nav leaves a slot for it in the row's mark
    if (!mk || mk.querySelector('.eyt')) return;
    const w = EyeTracker(mk, { size: 28, eyeScale: 1.55, within: 'nav, .nav, .side', below: 140, ...EYT_MOODS.calm });
    let mood = '';
    setInterval(() => {
      const m = jarvisMood();
      if (m !== mood) { mood = m; mk.querySelector('.eyt').dataset.mood = m; w.set(EYT_MOODS[m]); }
      // what needs the owner: the first other nav row with a count on it; JARVIS looks there
      w.lookAt(m === 'needs' || m === 'error' ? [...document.querySelectorAll('.nav-btn:not([data-view="ai"]) .badge')].find((b) => b.textContent.trim())?.closest('.nav-btn') || null : null);
    }, 1000);
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start); else start();
})();
