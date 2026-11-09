'use strict';

/* ══ Seek ═════════════════════════════════════════════════
   Bencho's Search (bencho.dev, MIT licence: bencho.dev/licence), ported from its React source to the panel's plain
   JS, as the Slingshot was (web/slingshot.js). Its comments are Bencho's, kept; the few that say "the panel" are
   what changed here. In the panel it is the search in every page's title bar (app.js wires it): across pages,
   threads and terminals, and on Terminals it filters the list in place.

   A search icon that becomes a search field.

   ONE OBJECT, NOT TWO. Everything here follows from that. The
   obvious build is an icon that fades out and an input that
   fades in, and it is obvious because it is easy — but two
   things swapping is never mistaken for one thing changing,
   however well the crossfade is tuned. So there is a single
   box whose WIDTH is the state, and the icon and the field
   are both inside it the whole time.

   Which turns the interesting problems into geometry rather
   than choreography:

   · The lens never moves relative to the box. It sits at a
     fixed inset from the left edge, and at the closed width
     that inset happens to centre it — (44 - 18) / 2 is 13,
     and 13 is also the padding the open field wants. So one
     number is both "centred in a circle" and "aligned in a
     field", and the lens travelling leftward across the page
     is not an animation anybody wrote. It is the box growing
     around a mark that stayed put.

   · The box grows from its middle, so the composition stays
     centred at every frame. Growing from the left would pin
     the lens and throw the field off-centre; growing from the
     right would slide the lens across the page. Neither is
     the object transforming, and both are what you get by
     accident.
     (The panel: in a title bar the box sits at the bar's right end, so it grows leftward from its right edge: the
     bar's other controls stay where your hand left them, and the lens still never moves inside the box.)

   · Height never changes and the corner is fully round at
     every width, so the SHAPE is one rule rather than a tween
     with two ends. At 44 it is a circle and at 320 a pill,
     and those are the same statement. The radius used to
     interpolate 22 → 13, which was a second thing that had
     to agree with the first; a stadium corner needs no
     agreement at all.

   The press is a real beat. A click compresses the object for
   a moment before it expands, because a thing that yields
   before it moves reads as having been pushed, and a thing
   that only moves reads as having been triggered. */

/* ── one spring, for everything that settles ───────────────
   Frames, not milliseconds. `dt` is expressed in sixtieths of
   a second and the damping is RAISED to it rather than
   multiplied by it, so a dropped frame decays the same amount
   of energy as the two frames it replaced. Multiplying is the
   version that makes a spring behave differently on a busy
   page, which is the hardest kind of bug to see.

   The loop parks itself the moment the value has settled.

   0..100 into the two numbers a spring actually has. The pair
   is chosen by DAMPING RATIO and then written back as
   stiffness and decay, because the ratio is the thing a
   person is actually setting:

     zeta = -ln(d) / (2 * sqrt(k))

     0   → zeta ~0.85, heavy, arrives without a ring
     50  → zeta ~0.41
     100 → zeta ~0.20, lively, two visible rebounds

   Units matter. The snap threshold is absolute, so a caller
   works in pixels or in 0..100 — a spring driven over 0..1
   would be "settled" before it had visibly moved. */
const seekSpringOf = (tune) => ({
  /* stiffness: how hard it is pulled toward the target */
  k: 0.08 + (tune / 100) * 0.16,
  /* decay, per frame: how much of the velocity survives */
  d: 0.62 + (tune / 100) * 0.2,
});
const seekClamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));

/* Seek(host, opts): builds the object into `host` and returns { el, input, open(), close(), clear() }.
   opts: shut (the closed circle, px), span (the open field, px), give (0..100, the magnet), spring (0..100),
   placeholder, onInput(value), onKey(event) (return true when handled), onOpen(), onClose().
   The panel's title bar is a 44px-ish header, so its Seek is 36 across with a 16px lens — Bencho's 64/28 is the
   whole component alone on a card; the ratio (0.44) is the one every round icon button on his bench keeps. */
function Seek(host, opts = {}) {
  const SHUT = opts.shut || 36;
  /* the lens, and its inset. This number is doing two jobs and
     they agree by construction: at 0.44 of the button, in with
     every other round icon button. The STROKE is independent of
     this — the svg is an 18-unit viewBox, so a stroke-width of u
     always draws at u/18 of the glyph's own size. */
  const LENS = Math.round(SHUT * 0.44);
  const INSET = (SHUT - LENS) / 2;
  const span = () => opts.span?.() ?? (matchMedia('(max-width: 760px)').matches ? 200 : 280);
  const give = opts.give ?? 50, tune = seekClamp(opts.spring ?? 50, 0, 100);
  /* Read once. A preference, not a live input. */
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;

  const el = document.createElement('div');
  el.className = 'sek';
  el.dataset.open = 'false';
  el.innerHTML = '<div class="sek-skin">' +
    /* Drawn rather than imported. The lens has to take a state —
       it thickens a hair while you type — and an icon you cannot
       address is an icon that can only be swapped for another one. */
    '<svg class="sek-lens" viewBox="0 0 18 18" aria-hidden="true"><circle cx="7.6" cy="7.6" r="5.4"/><path d="M11.6 11.6 L15.4 15.4"/></svg>' +
    `<input class="sek-field" type="text" placeholder="${opts.placeholder || 'Search'}" aria-label="${opts.label || 'Search'}" tabindex="-1" autocomplete="off" spellcheck="false">` +
    /* The way in, and only while there is a way in. When the field
       is open this is the field's own job, and a second target
       sitting over it would swallow the click that places the caret. */
    `<button type="button" class="sek-hit" aria-label="${opts.label || 'Search'}"></button></div>`;
  host.append(el);
  const field = el.querySelector('.sek-field'), hit = el.querySelector('.sek-hit');
  const set = (k, v) => el.style.setProperty(k, v);
  set('--inset', INSET + 'px'); set('--lens', LENS + 'px'); set('--shut', SHUT + 'px');

  /* ── the width is the state ──────────────────────────────
     Tuned toward the top of the range by default: this is a
     control, and a control that wobbles reads as decoration.
     What the spring is for here is the tiny overshoot at the
     end — enough that the object arrives rather than stops,
     and not enough to notice as a bounce. */
  let open = false, cur = SHUT, vel = 0, raf = 0, beat = 0, rest = 0;
  const paint = () => {
    const full = Math.max(SHUT, span());
    /* how far through the transformation, 0 shut and 1 open */
    const p = seekClamp((cur - SHUT) / Math.max(1, full - SHUT), 0, 1);
    set('--w', cur.toFixed(2) + 'px');
    set('--p', p.toFixed(3));
    /* the placeholder and the caret arrive in the last third,
       once there is somewhere for them to be */
    set('--say', seekClamp((p - 0.55) / 0.45, 0, 1).toFixed(3));
  };
  const to = (target) => {
    cancelAnimationFrame(raf);
    if (still) { cur = target; vel = 0; return paint(); }
    const { k, d } = seekSpringOf(tune);
    let prev = 0;
    const tick = (t) => {
      const dt = prev ? seekClamp((t - prev) / 16.67, 0, 2.5) : 1;
      prev = t;
      vel += (target - cur) * k * dt;
      vel *= Math.pow(d, dt);
      cur += vel * dt;
      if (Math.abs(target - cur) < 0.02 && Math.abs(vel) < 0.02) { cur = target; vel = 0; raf = 0; return paint(); }
      paint();
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
  };
  paint();

  /* ── the magnet ──────────────────────────────────────────
     Measured from the FRAME, which never moves, and not from
     the object, which does. A vector read off a thing the
     vector is currently displacing is a feedback loop: it
     converges, but it converges by ringing, and the ring is
     visible as a shiver on an object this small.

     Off entirely once open. A field that drifts toward the
     pointer while you are trying to click into it is a field
     fighting you. */
  let lraf = 0, at = { x: 0, y: 0 };
  const publish = () => { lraf = 0; set('--lx', at.x.toFixed(2) + 'px'); set('--ly', at.y.toFixed(2) + 'px'); };
  if (!still) {
    document.addEventListener('pointermove', (e) => {
      if (open || !el.isConnected) return;
      const b = el.getBoundingClientRect();
      const dx = e.clientX - (b.right - SHUT / 2), dy = e.clientY - (b.top + b.height / 2);
      const d = Math.hypot(dx, dy), R = 80; // the panel: 110 was for a 64px object alone on a card
      if (d > R) {
        if (at.x || at.y) { at = { x: 0, y: 0 }; if (!lraf) lraf = requestAnimationFrame(publish); }
        return;
      }
      /* A FEW PIXELS. The brief for this is "responsive, not
         following" and the difference is entirely in the
         ceiling: past about seven the object stops being a
         thing that acknowledges you and starts being a thing
         you are dragging around. (The panel: a smaller object, a smaller ceiling.) */
      const pull = (1 - d / R) ** 1.4 * (1.5 + (give / 100) * 3);
      at = { x: (dx / (d || 1)) * pull, y: (dy / (d || 1)) * pull };
      if (!lraf) lraf = requestAnimationFrame(publish);
    }, { passive: true });
  }

  /* ── opening ─────────────────────────────────────────────
     Compress, then expand, then focus. The order matters and
     the gap is short: 90ms is long enough to be felt as the
     object yielding and short enough that nobody waits for
     it. Focus lands with the expansion rather than at the end
     of it, so the caret is already there when the field
     arrives — waiting for the width to settle puts a visible
     pause between the box being ready and the box being
     usable. */
  const start = () => {
    if (open) return field.focus();
    el.dataset.press = 'true';
    clearTimeout(beat);
    beat = setTimeout(() => {
      el.dataset.press = 'false';
      open = true;
      el.dataset.open = 'true';
      at = { x: 0, y: 0 }; publish();
      hit.hidden = true;
      field.tabIndex = 0;
      to(Math.max(SHUT, span()));
      field.focus();
      opts.onOpen?.();
    }, still ? 0 : 90);
  };
  const close = () => {
    if (!open) return;
    open = false;
    el.dataset.open = 'false';
    hit.hidden = false;
    field.tabIndex = -1;
    to(SHUT);
    opts.onClose?.();
  };
  hit.addEventListener('click', start);

  /* Away with nothing typed and it goes back. Away with
     something typed and it stays: the value IS the reason the
     field exists, and closing over it would either hide it or
     throw it away. */
  field.addEventListener('blur', () => setTimeout(() => {
    if (!field.value.trim() && !el.contains(document.activeElement)) close();
    opts.onBlur?.();
  }, 120));

  /* the lens acknowledges typing without performing it — one
     short beat per burst, not one per character. A mark that
     flickers at the rate of your typing is noise wearing the
     costume of feedback. */
  field.addEventListener('input', () => {
    el.dataset.busy = 'true';
    clearTimeout(rest);
    rest = setTimeout(() => { el.dataset.busy = 'false'; }, 340);
    opts.onInput?.(field.value);
  });
  field.addEventListener('keydown', (e) => {
    if (opts.onKey?.(e)) return;
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    field.value = '';
    opts.onInput?.('');
    close();
    field.blur();
  });

  return {
    el, input: field, open: start, close,
    clear() { field.value = ''; opts.onInput?.(''); },
    get isOpen() { return open; },
  };
}
