'use strict';

/* ══ Action node ══════════════════════════════════════════
   Bencho's Action node (bencho.dev, MIT licence: bencho.dev/licence), ported from its React source to the panel's
   plain JS. What travels is the part he says is the component: the controls that come out of a corner, one spring
   each, staggered by a delay on the target, folding back last-out-first-in. The demo card around them (Node 07,
   its sentence, its faces) stays behind: AVATARS were his own pictures, not licensed to travel, and the panel has
   no node cards. Here the "card" is a mark in the collapsed sidebar (J.A.R.V.I.S, Terminals) and the controls are
   that page's quick-access items (your open threads, the terminals you're working in): hover the mark and they
   come out of its corner, as they'd stand under it in the expanded sidebar. His comments are kept; "the panel"
   marks what changed.

   THE CONTROLS ARE THE COMPONENT. What this block is about is
   where a card's actions live when you are not using them, and
   the answer it argues for is: inside the corner they belong to.

   ── THEY COME OUT OF THE CORNER, THEY DO NOT ARRIVE AT IT ─
   The reflex is to fade a toolbar in above the card, and it
   is wrong in a way that is easy to miss: a control that
   fades in has no relationship to the thing it controls. It
   could belong to anything. These start AT the corner with no
   radius and no size, and they travel out — so the card is
   visibly where they came from and the corner is visibly what
   they are attached to.

   Which is also why they scale as they travel rather than
   arriving full size: a thing coming out of somewhere is
   small when it is still mostly inside it.

   ── ONE SPRING EACH, AND THE STAGGER IS A DELAY ON THE
      TARGET, NOT AN EASE ─────────────────────────────────
   Each one is told to open a beat after the one before it,
   and the spring does the rest: the cascade is objects with
   their own physics rather than one animation with offsets,
   which is the difference between a fan opening and a list
   appearing in order.

   Closing runs the other way, last out first back, so the
   fan folds into the corner rather than collapsing from the
   inside. */

/* ── one spring, for everything that settles ───────────────
   Frames, not milliseconds. `dt` is expressed in sixtieths of
   a second and the damping is RAISED to it rather than
   multiplied by it, so a dropped frame decays the same amount
   of energy as the two frames it replaced. Multiplying is the
   version that makes a spring behave differently on a busy
   page, which is the hardest kind of bug to see.

   The loop parks itself the moment the value has settled.

   The pair is chosen by DAMPING RATIO and then written back
   as stiffness and decay (zeta = -ln(d) / (2 * sqrt(k))):
     0   → zeta ~0.85, heavy, arrives without a ring
     50  → zeta ~0.41
     100 → zeta ~0.20, lively, two visible rebounds */
const nodSpringOf = (tune) => ({ k: 0.08 + (tune / 100) * 0.16, d: 0.62 + (tune / 100) * 0.2 });
const nodClamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));

/* the beat between one button and the next, at the top of the
   range. 90ms is about as long as a cascade can be before the
   last one reads as late rather than as following.
   (The panel: up to ten items, not four, so the whole cascade is held to ~320ms and the beat shrinks to fit.) */
const NOD_BEAT = 90;
/* centre to centre along the path. A real distance rather than
   an angle: twelve pixels of air between two buttons is twelve
   pixels whatever the corner is doing. (The panel: rows, 32px
   tall with 6px of air.) */
const NOD_ROW = 32, NOD_AIR = 6;

/* ActionFan(anchor, opts): a fan for one sidebar mark. opts.items() → HTML strings, one per control (the panel's
   .nav-qi buttons); opts.when() → whether it may open (the sidebar is collapsed); opts.onClick(event).
   Returns { close() }. */
function ActionFan(anchor, opts) {
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const bounce = opts.bounce ?? 20, stagger = opts.stagger ?? 55;
  const fan = document.createElement('div');
  fan.className = 'nod-fan';
  fan.setAttribute('role', 'menu');
  fan.hidden = true;
  document.body.append(fan);
  let on = false, acts = [], timers = [], raf = 0;

  /* ── where they ride: down the card's own right edge, offset ──
     The path is the card's outline pushed out by a constant
     distance, so every point on it is exactly `gap` from the
     nearest card surface. (The panel: the card is the sidebar
     mark; the path runs from its top-right corner down past the
     sidebar's edge, which is the right edge's straight.) */
  const place = () => {
    const r = anchor.getBoundingClientRect(), nav = anchor.closest('.nav').getBoundingClientRect();
    const x = nav.right + 10, y0 = r.top + (r.height - NOD_ROW) / 2;
    const h = acts.length * (NOD_ROW + NOD_AIR) - NOD_AIR;
    /* kept on screen: a long list near the foot opens upward from the mark instead */
    const top = Math.max(8, Math.min(y0, innerHeight - 8 - h));
    fan.style.left = x + 'px';
    fan.style.top = top + 'px';
    /* the corner they all come out of, in the fan's own coordinates: the mark's right edge, at its middle */
    fan.style.setProperty('--ox', (r.right - x) + 'px');
    fan.style.setProperty('--oy', (r.top + r.height / 2 - top) + 'px');
    /* ── the air between the card and the fan is a HOLE ─────
       Move the pointer from the card toward a button and it
       leaves the card on the way across — the fan folds itself
       away from under the cursor that was reaching for it. So
       the fan carries a hit area reaching back to the card. */
    fan.style.setProperty('--reach', (x - r.right + 4) + 'px');
    /* the panel: the fan's own box is the whole column (rows and the air between them), and the pad runs its full
       height and the mark's: drifting between two rows, or a little past the last, is not leaving */
    const oy = r.top + r.height / 2 - top, pt = Math.min(0, oy - 24), pb = Math.max(h, oy + 24);
    fan.style.height = h + 'px';
    fan.style.setProperty('--pad-t', pt + 'px');
    fan.style.setProperty('--pad-h', (pb - pt) + 'px');
  };

  const build = () => {
    const html = opts.items();
    if (!html.length) return false;
    fan.innerHTML = '<i class="nod-reach" aria-hidden="true"></i>' + html.join('');
    acts = [...fan.querySelectorAll('.nav-qi')].map((el, i) => {
      el.setAttribute('role', 'menuitem');
      el.style.setProperty('--y', i * (NOD_ROW + NOD_AIR) + 'px');
      return { el, at: 0, v: 0, lit: false };
    });
    return true;
  };

  /* ── out of the corner, in a straight line ───────
     From the hidden point to its place on the path, so the fan
     opens OUT of the corner. `u` is not clamped, so the spring's
     overshoot carries each one a little past its place and back,
     rather than being flattened at the end of the journey. */
  const paint = (a) => {
    const u = nodClamp(a.at / 100, 0, 1.4);
    a.el.style.setProperty('--u', u.toFixed(4));
    /* ── barely any scale, and no fade at all ──
       A button hidden UNDER the card does not need to be
       transparent to be hidden. The 0.18 that is left is not
       concealment, it is weight: a thing being pushed out
       arrives at its full size a moment after it arrives at its
       place. (The panel: nothing is under the sidebar's edge to
       hide behind, so the last few pixels of travel fade in.) */
    a.el.style.setProperty('--pop', (0.82 + 0.18 * Math.min(u, 1)).toFixed(4));
    a.el.style.opacity = nodClamp(u * 3, 0, 1).toFixed(3);
  };
  const tick = (() => {
    let prev = 0;
    const loop = (t) => {
      const dt = prev ? nodClamp((t - prev) / 16.67, 0, 2.5) : 1;
      prev = t;
      /* the panel: quicker than Bencho's demo (a sidebar you use all day, not a card you admire): k 0.24 and d 0.62,
         zeta ~0.49, a hair of overshoot and home in about a fifth of a second */
      const { k, d } = opts.quick ? { k: 0.24, d: 0.62 } : nodSpringOf(bounce);
      let busy = false;
      for (const a of acts) {
        const target = a.lit ? 100 : 0;
        a.v += (target - a.at) * k * dt;
        a.v *= Math.pow(d, dt);
        a.at += a.v * dt;
        if (Math.abs(target - a.at) < 0.02 && Math.abs(a.v) < 0.02) { a.at = target; a.v = 0; } else busy = true;
        paint(a);
      }
      if (busy) raf = requestAnimationFrame(loop);
      else { raf = 0; prev = 0; if (!on) fan.hidden = true; }
    };
    return () => { if (!raf) { prev = 0; raf = requestAnimationFrame(loop); } };
  })();

  /* ── open, and then open PER BUTTON ─────────────────────
     `on` is the pointer. `lit` is what each spring is aiming at,
     which is the same thing a moment later — and the moment is
     the stagger. The reverse fold is the same mechanism read
     backwards rather than a second one. */
  const set = (want) => {
    if (want === on) return;
    if (want && (!opts.when() || !build())) return;
    on = want;
    timers.forEach(clearTimeout);
    timers = [];
    if (on) { place(); fan.hidden = false; }
    fan.dataset.on = on ? '1' : '';
    anchor.classList.toggle('fanned', on);
    const n = acts.length;
    const gap = still ? 0 : Math.min((nodClamp(stagger, 0, 100) / 100) * NOD_BEAT, (opts.quick ? 140 : 320) / Math.max(1, n));
    acts.forEach((a, i) => {
      /* opening runs first to last; closing runs last to first,
         so the fan folds back into the corner in the order it
         came out of it */
      const at = gap * (on ? i : n - 1 - i);
      const go = () => {
        a.lit = on;
        /* hidden from the keyboard while it is folded away: a
           control you cannot see should not be the next thing
           tab lands on */
        a.el.tabIndex = on ? 0 : -1;
        if (still) { a.at = on ? 100 : 0; a.v = 0; paint(a); if (!on) fan.hidden = true; } else tick();
      };
      if (at <= 0) go(); else timers.push(setTimeout(go, at));
    });
  };

  /* ── `out` with a containment test, not `leave` ──────
     The fan and the mark are one object to the pointer: leaving
     one for the other is not leaving. */
  let shut = 0;
  const over = () => { clearTimeout(shut); set(true); };
  const out = (e) => {
    const to = e.relatedTarget;
    if (to && (anchor.contains(to) || fan.contains(to))) return;
    clearTimeout(shut);
    shut = setTimeout(() => set(false), 280); // a slip of the hand isn't leaving
  };
  anchor.addEventListener('pointerenter', (e) => { if (e.pointerType !== 'touch') over(); });
  anchor.addEventListener('pointerout', out);
  fan.addEventListener('pointerenter', over);
  fan.addEventListener('pointerout', out);
  anchor.addEventListener('focus', () => { if (opts.when()) over(); });
  fan.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { set(false); anchor.focus(); }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const xs = acts.map((a) => a.el), i = xs.indexOf(document.activeElement);
      xs[(i + (e.key === 'ArrowDown' ? 1 : xs.length - 1)) % xs.length]?.focus();
      e.preventDefault();
    }
  });
  fan.addEventListener('click', (e) => { if (e.target.closest('.nav-qi')) { opts.onClick(e); set(false); } });
  addEventListener('scroll', () => set(false), true);
  return { close: () => set(false) };
}
