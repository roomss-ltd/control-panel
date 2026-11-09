package main

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// VNC: the screens that lagged as pictures or HTML go as VNC instead. A terminal's zellij runs in an xterm on an X
// display, and so does the Browser's Chromium. TigerVNC's Xvnc is that display and serves it as RFB, which sends
// only the rectangles that changed, each encoded for what it holds, and only when the page asks for the next
// update, so a slow phone never gets a backlog. The page's noVNC (web/novnc) draws it; the panel carries RFB
// between a websocket and the display's unix socket (mode 0600: nobody else on the box can watch or type).
//
// No root on this box: Xtigervnc and xterm are Debian's packages unpacked under ~/.local/opt/vnc (vncRoot).
// Without them the page falls back to the streams it had before.

// vncRoot is where the Xtigervnc and xterm packages are unpacked.
var vncRoot = filepath.Join(os.Getenv("HOME"), ".local/opt/vnc")

// errNoVNC is returned when Xtigervnc is not installed.
var errNoVNC = errors.New("no Xtigervnc on this box (unpack tigervnc-standalone-server under ~/.local/opt/vnc)")

// vncBin finds an X program: under vncRoot, else on PATH ("" if neither).
func vncBin(name string) string {
	if p := filepath.Join(vncRoot, "usr/bin", name); fileExists(p) {
		return p
	}
	p, _ := exec.LookPath(name)
	return p
}

// vncEnv: what an X program here runs with: the display, the unpacked libraries, UTF-8.
func vncEnv(num int) []string {
	env := []string{"LD_LIBRARY_PATH=" + filepath.Join(vncRoot, "usr/lib/x86_64-linux-gnu"), "LC_ALL=C.UTF-8"}
	if num >= 0 {
		env = append(env, "DISPLAY=:"+strconv.Itoa(num))
	}
	for _, kv := range cleanEnv(os.Environ()) {
		if k, _, _ := strings.Cut(kv, "="); k != "DISPLAY" && k != "LD_LIBRARY_PATH" && k != "LC_ALL" {
			env = append(env, kv)
		}
	}
	return env
}

// the displays' sockets: a short directory, a unix socket's path must fit in 108 bytes
var vncDir = sync.OnceValues(func() (string, error) { return os.MkdirTemp("/tmp", "cpvnc") })

// vncSeq numbers the displays this panel starts.
var vncSeq atomic.Int64

// xdisp is a running Xvnc display: its number, the unix socket it serves RFB on, and its process.
type xdisp struct {
	num  int
	sock string
	cmd  *exec.Cmd
	done chan struct{}
}

// startX starts an Xvnc of w×h pixels. It dies with the panel (and on stop).
func startX(w, h int) (*xdisp, error) {
	bin := vncBin("Xtigervnc")
	if bin == "" {
		return nil, errNoVNC
	}
	dir, err := vncDir()
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, strconv.FormatInt(vncSeq.Add(1), 10)+".sock")
	pr, pw, err := os.Pipe() // Xvnc picks a free display and says which on it (fd 3) once it takes connections
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	cmd := exec.Command(bin, "-displayfd", "3", "-geometry", fmt.Sprintf("%dx%d", w, h), "-depth", "24",
		"-SecurityTypes", "None", "-rfbport", "-1", "-rfbunixpath", sock, "-rfbunixmode", "0600", "-nolisten", "tcp",
		"-fp", filepath.Join(vncRoot, "usr/share/fonts/X11/misc"))
	cmd.ExtraFiles = []*os.File{pw}
	cmd.Env = vncEnv(-1)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	pw.Close()
	x := &xdisp{sock: sock, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(x.done) }()
	pr.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, _ := bufio.NewReader(pr).ReadString('\n')
	if x.num, err = strconv.Atoi(strings.TrimSpace(line)); err != nil {
		x.stop()
		return nil, errors.New("Xvnc didn't start")
	}
	return x, nil
}

// stop ends the display (and anything started on it) and removes its socket.
func (x *xdisp) stop() {
	syscall.Kill(-x.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-x.done:
	case <-time.After(3 * time.Second):
		syscall.Kill(-x.cmd.Process.Pid, syscall.SIGKILL)
	}
	os.Remove(x.sock)
}

// resize makes the screen w×h pixels (the viewers' noVNC follows).
func (x *xdisp) resize(w, h int) error {
	cmd := exec.Command(vncBin("xrandr"), "--fb", fmt.Sprintf("%dx%d", w, h))
	cmd.Env = vncEnv(x.num)
	return cmd.Run()
}

// vncBridge carries RFB between the page's websocket and a display, while the panel login lasts.
func (s *server) vncBridge(w http.ResponseWriter, r *http.Request, sock string) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "the display is gone"})
		return
	}
	defer c.Close()
	ws, err := wsUpgrade(w, r, 1<<20)
	if err != nil {
		return
	}
	defer ws.close()
	done := make(chan struct{})
	defer close(done)
	go func() { // the page's keys, pointer and update requests
		defer c.Close()
		for {
			op, p, err := ws.read()
			if err != nil {
				return
			}
			if op == wsBinary {
				if _, err := c.Write(p); err != nil {
					return
				}
			}
		}
	}()
	tok, email := sessionToken(r), identity(r)
	go func() { // a login that ended ends the screen; a ping keeps an idle screen's socket open through proxies
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if !s.auth.peekSession(tok, email) || ws.write(wsPing, nil) != nil {
					c.Close()
					return
				}
			}
		}
	}()
	buf := make([]byte, 256<<10)
	for {
		n, err := c.Read(buf)
		if n > 0 && ws.write(wsBinary, buf[:n]) != nil {
			return
		}
		if err != nil {
			return
		}
	}
}

// ---- a pane's terminal ----

// vncCell: an xterm cell of DejaVu Sans Mono at px pixels (measured: its advance is 0.6021 em, rounded; its
// height the ascent and descent, each rounded up).
func vncCell(px int) (w, h int) {
	f := float64(px)
	return int(math.Round(f * 0.6021)), int(math.Ceil(f*0.928) + math.Ceil(f*0.236))
}

// paneVNC: GET /api/pane/vnc?session&pane&cols&rows&px (a websocket). The pane's terminal as VNC: an xterm of
// cols×rows cells, glyphs px pixels tall, running a zellij client on the session, taken to the pane's tab. A
// real terminal: keys go straight to zellij, and the tab takes its size, as with the terminal view's fit
// (fit.go). It goes when the page does.
func (s *server) paneVNC(w http.ResponseWriter, r *http.Request) {
	session, pane, err := paneParams(r)
	q := r.URL.Query()
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))
	px, _ := strconv.Atoi(q.Get("px"))
	if err != nil || cols < 20 || cols > 400 || rows < 5 || rows > 150 || px < 6 || px > 64 {
		writeJSON(w, 400, map[string]any{"error": "bad session, pane or size"})
		return
	}
	xterm := vncBin("xterm")
	if vncBin("Xtigervnc") == "" || xterm == "" {
		writeJSON(w, 501, map[string]any{"error": errNoVNC.Error()})
		return
	}
	ps, err := s.z.panes(session)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	tab := -1
	for _, p := range ps {
		if p.ID == pane {
			tab = p.TabPos
		}
	}
	if tab < 0 {
		writeJSON(w, 404, map[string]any{"error": "no such pane"})
		return
	}
	before := s.z.clients(session)
	cw, ch := vncCell(px)
	x, err := startX(cols*cw, rows*ch)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer x.stop()
	cmd := exec.Command(xterm, "-u8", "-fa", fmt.Sprintf("DejaVu Sans Mono:pixelsize=%d", px), "-geometry", fmt.Sprintf("%dx%d+0+0", cols, rows),
		"-b", "0", "-bw", "0", "-bg", "#171717", "-fg", "#d7dae0", "-xrm", "XTerm*disallowedWindowOps: 1,2,3,4,5,6,7,8,9,11,13,14,18,19,20,21,GetSelection,SetWinLines,SetXprop",
		"-e", zellijBin, "attach", session)
	cmd.Env = vncEnv(x.num)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); cmd.Wait() }()
	go s.z.vncTo(session, tab, pane, before)
	s.vncBridge(w, r, x.sock)
}

// clients: how many clients are attached to the session (-1: couldn't tell).
func (z *zellij) clients(session string) int {
	out, err := z.action(session, "list-clients")
	if err != nil {
		return -1
	}
	return strings.Count(strings.TrimSpace(string(out)), "\n") // the lines past the header
}

// vncTo takes the client that just attached to the pane: its tab, its pane. Alone on the session it's locked, so
// Ctrl-keys reach the program, as they do in the terminal view (with other terminals attached, switching the
// mode would switch theirs: left as it is).
func (z *zellij) vncTo(session string, tab, pane, before int) {
	for i := 0; i < 50 && z.clients(session) <= before; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if before == 0 {
		z.action(session, "switch-mode", "locked")
	}
	z.action(session, "go-to-tab", strconv.Itoa(tab+1))
	z.action(session, "focus-pane-id", pid(pane))
}
