package main

import (
	"log"
	"net/http"
	"sync"
)

// The Browser as VNC (vnc.go): the owner's Chromium draws on an X display instead of headless, and the page shows
// that display. The display takes the size of the tab being watched (its page at 2x, the density Chromium renders
// at) and the tab's window is raised over it with its toolbar above the top edge, so only the page shows. The page
// draws the display into the canvas the frames went to; input, annotations and the rest go on as they did (over
// CDP). The agents' Chromiums are headless and stream frames as before; so does everything on a box without Xvnc.

// bvnc is the owner's Chromium's X display and its screen size; show makes raising a tab's window over the
// display one at a time, so the last tab watched ends up on top.
type bvnc struct {
	mu   sync.Mutex
	x    *xdisp
	size [2]int // the screen's, in pixels
	show sync.Mutex
}

// display: the owner's Chromium's display, started when first needed (nil: no VNC here, Chromium runs headless).
// One that died (and the Chromium with it) is started again.
func (b *browser) display() *xdisp {
	v := &b.vnc
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.x != nil {
		select {
		case <-v.x.done:
			v.x = nil
		default:
			return v.x
		}
	}
	if vncBin("Xtigervnc") == "" {
		return nil
	}
	x, err := startX(2560, 1600)
	if err != nil {
		log.Println("browser: vnc:", err)
		return nil
	}
	v.x, v.size = x, [2]int{2560, 1600}
	return x
}

// vncShow puts the tab on the display: the screen its page's size, its window raised over it, the toolbar (and
// any infobar) above the top edge. Run after emulate, which sized the window (bounds in CSS px: 2x on screen).
func (b *browser) vncShow(t *btab) {
	v := &b.vnc
	v.mu.Lock()
	x := v.x
	v.mu.Unlock()
	if x == nil || t == nil || !t.src.mine() {
		return
	}
	v.show.Lock() // one at a time: the last tab watched ends up on top
	defer v.show.Unlock()
	b.mu.Lock()
	w, h, win, uiW, uiH := int(t.cssW), int(t.cssH), t.win, t.uiW, t.uiH
	b.mu.Unlock()
	if win == 0 || w <= 0 || h <= 0 {
		return
	}
	v.mu.Lock()
	resize := v.size != [2]int{2 * w, 2 * h}
	v.size = [2]int{2 * w, 2 * h}
	v.mu.Unlock()
	if resize {
		if err := x.resize(2*w, 2*h); err != nil {
			log.Println("browser: vnc resize:", err)
		}
	}
	c := t.src.cdp
	c.call("", "Browser.setWindowBounds", map[string]any{"windowId": win, "bounds": map[string]any{"windowState": "normal",
		"left": -uiW, "top": -uiH, "width": w + uiW, "height": h + uiH}}, nil)
	c.call(t.sess, "Page.bringToFront", nil, nil)
}

// serveVNC: GET /api/browser/vnc (a websocket): the owner's Chromium's display.
func (b *browser) serveVNC(w http.ResponseWriter, r *http.Request) {
	x := b.display()
	if x == nil {
		writeJSON(w, 501, map[string]any{"error": errNoVNC.Error()})
		return
	}
	go b.ensureMe()
	b.s.vncBridge(w, r, x.sock)
}
