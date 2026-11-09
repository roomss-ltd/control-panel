// A VNC screen (vnc.go) in the page, drawn by noVNC 1.7.0 (web/novnc, MPL-2.0). app.js and browser.js call
// window.cpVNC; it's a module, so it may load after them: they check for it when they need it.
import RFB from './novnc/core/rfb.js';

// cpVNC(el, path, o) → noVNC's RFB, showing the websocket at path (on this host) in el.
//   o.viewOnly: draw only (the Browser keeps its own input); o.onflip(canvas): after each update is drawn (the
//   Browser copies the screen into its own canvas); o.onpaste(text): a paste (Cmd/Ctrl+Shift+V) as text, for the
//   caller to send its own way (RFB has no paste).
window.cpVNC = (el, path, o = {}) => {
  const rfb = new RFB(el, `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${path}`, { shared: true });
  rfb.viewOnly = !!o.viewOnly;
  rfb.focusOnClick = !o.viewOnly;
  rfb.scaleViewport = !o.viewOnly; // draw-only: the caller sizes what it shows
  rfb.background = o.bg || '#171717';
  rfb.qualityLevel = o.quality ?? 6;
  rfb.compressionLevel = 2;
  // draw-only: the display's pointer, which no one here moves, goes to the corner, where Xvnc stops drawing it into
  // the picture (it draws a pointer the viewer didn't move itself)
  if (o.viewOnly) rfb.addEventListener('connect', () => RFB.messages.pointerEvent(rfb._sock, 0, 0, 0));
  rfb.cpCanvas = rfb._display._target; // the screen as drawn (noVNC's own: pinned to 1.7.0)
  if (o.onflip) { // Display.flip draws the updated rectangles on that canvas
    const d = rfb._display, flip = d.flip.bind(d);
    d.flip = (q) => { flip(q); o.onflip(d._target); };
  }
  if (o.onpaste) {
    // the paste keys don't reach noVNC (it would send them as keys and stop the paste): the browser pastes
    el.addEventListener('keydown', (e) => {
      if ((e.key === 'v' || e.key === 'V') && (e.metaKey || (e.ctrlKey && e.shiftKey))) e.stopPropagation();
    }, true);
    el.addEventListener('paste', (e) => {
      const text = e.clipboardData?.getData('text');
      if (text) { e.preventDefault(); o.onpaste(text); }
    });
  }
  // what's copied there (a selection in the terminal) comes here, when the page may write the clipboard
  rfb.addEventListener('clipboard', (e) => { if (e.detail?.text) navigator.clipboard?.writeText(e.detail.text).catch(() => {}); });
  return rfb;
};
dispatchEvent(new Event('cpvnc')); // for a view that opened before this module had loaded
