package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Reading the machine's state from /proc: CPU, memory, disks, network, and every process grouped into the app it belongs
// to (a CI runner, a zellij pane, a daemon), for the Resources page, the balancer and the notifications.

// clkTck is the kernel's clock ticks per second.
// ponytail: Linux CLK_TCK is 100 on every mainstream kernel; read sysconf if that ever changes.
const clkTck = 100

// pageSize is the size of a memory page, to turn /proc's page counts into bytes.
var pageSize = uint64(os.Getpagesize())

// ---- parsers ----

// procStat is the part of /proc/<pid>/stat the panel uses.
type procStat struct {
	PID, PPID    int
	PGRP, TPGID  int // process group; foreground group of its terminal (-1: none)
	Comm, State  string
	UTime, STime uint64
	Start        uint64 // ticks after boot; (pid, Start) identifies a process across pid reuse
	RSS          uint64 // pages
}

// parseStat parses the contents of /proc/<pid>/stat; ok is false if it doesn't look like one.
func parseStat(b []byte) (procStat, bool) {
	var st procStat
	open, close := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 1 || close < open {
		return st, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b[:open])))
	if err != nil {
		return st, false
	}
	f := strings.Fields(string(b[close+1:]))
	if len(f) < 22 {
		return st, false
	}
	u := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
	st.PID, st.Comm, st.State = pid, string(b[open+1:close]), f[0]
	st.PPID = int(u(1))
	st.PGRP = int(u(2))
	st.TPGID, _ = strconv.Atoi(f[5])
	st.UTime, st.STime, st.Start, st.RSS = u(11), u(12), u(19), u(21)
	return st, true
}

// cpuTimes is the CPU time a line of /proc/stat adds up to, in ticks: all of it, and the busy part.
type cpuTimes struct{ busy, total uint64 }

// parseCPU returns the aggregate line first, then one entry per core.
func parseCPU(b []byte) []cpuTimes {
	var out []cpuTimes
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		f := strings.Fields(line)[1:]
		var c cpuTimes
		for i, s := range f {
			v, _ := strconv.ParseUint(s, 10, 64)
			if i >= 8 { // guest time is already counted in user
				break
			}
			c.total += v
			if i != 3 && i != 4 { // idle, iowait
				c.busy += v
			}
		}
		out = append(out, c)
	}
	return out
}

// cpuPct returns the percentage of time busy between two readings.
func cpuPct(a, b cpuTimes) float64 {
	if b.total <= a.total || b.busy < a.busy {
		return 0
	}
	return float64(b.busy-a.busy) / float64(b.total-a.total) * 100
}

// parseMeminfo parses /proc/meminfo (and any file of "key: value" lines) into numbers, kB converted to bytes.
func parseMeminfo(b []byte) map[string]uint64 {
	m := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		m[k] = n
	}
	return m
}

// memParts splits RAM the way the Memory card does: apps (anonymous pages), page cache (less
// tmpfs/shm, which shows apart) and the kernel's slab.
func memParts(m map[string]uint64) (apps, cache, kern uint64) {
	cache = m["Cached"] + m["Buffers"]
	return m["AnonPages"], cache - min(cache, m["Shmem"]), m["Slab"]
}

// PSI is /proc/pressure/memory: the % of time some (or all, "full") tasks stalled waiting for memory.
type PSI struct {
	Some10  float64 `json:"some10"`
	Some60  float64 `json:"some60"`
	Some300 float64 `json:"some300"`
	Full10  float64 `json:"full10"`
}

// parsePSI parses /proc/pressure/memory.
func parsePSI(b []byte) (p PSI) {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		v := func(i int) float64 { _, s, _ := strings.Cut(f[i], "="); x, _ := strconv.ParseFloat(s, 64); return x }
		switch f[0] {
		case "some":
			p.Some10, p.Some60, p.Some300 = v(1), v(2), v(3)
		case "full":
			p.Full10 = v(1)
		}
	}
	return
}

// parseNetDev sums the bytes received and sent over all interfaces but loopback, from /proc/net/dev.
func parseNetDev(b []byte) (rx, tx uint64) {
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx, tx = rx+r, tx+t
	}
	return
}

// sockEntry is one line of /proc/net/{tcp,udp}[6].
type sockEntry struct {
	Proto         string
	Local, Remote netip.AddrPort
	State         string // hex: 0A = LISTEN, 01 = ESTABLISHED (tcp); 07 = unconnected (udp)
	UID           int
	Inode         string
}

// parseProcNet parses /proc/net/tcp (or tcp6, udp, udp6, named in proto) into socket entries.
func parseProcNet(b []byte, proto string) []sockEntry {
	var out []sockEntry
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		l, ok1 := parseHexAddr(f[1])
		r, ok2 := parseHexAddr(f[2])
		if !ok1 || !ok2 {
			continue
		}
		uid, _ := strconv.Atoi(f[7])
		out = append(out, sockEntry{Proto: proto, Local: l, Remote: r, State: f[3], UID: uid, Inode: f[9]})
	}
	return out
}

// parseHexAddr decodes /proc/net addresses: each 32-bit word is in host (little-endian) order.
func parseHexAddr(s string) (netip.AddrPort, bool) {
	a, p, ok := strings.Cut(s, ":")
	raw, err := hex.DecodeString(a)
	port, err2 := strconv.ParseUint(p, 16, 16)
	if !ok || err != nil || err2 != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(raw); i += 4 {
		binary.BigEndian.PutUint32(raw[i:], binary.LittleEndian.Uint32(raw[i:]))
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}

// runnerDirRe finds a CI runner's folder in a path and captures the runner's name.
var runnerDirRe = regexp.MustCompile(`/(?:ci-cd-jobs|ci-aws/runners)/((?:r|fe)\d+|aws-vm-[a-z0-9-]+)/`)

// groupKey decides which app a process belongs to, first match wins:
//  1. a CI runner (by path, or RUNNER_NAME env in job steps);
//  2. a zellij pane's root shell (parent is the zellij server);
//  3. a root process (ppid ≤ 2: init, kthreadd, or daemonized) → CI's when it runs in a runner's folder (a job's
//     redis-server, which wipes its env), else its own name, even if it inherited a pane's env (e.g. the Docker VM
//     started from a shell);
//  4. otherwise its parent's group.
//
// zsession, when set, is the pane's current session name (see zellijSessions); env
// ZELLIJ_SESSION_NAME keeps the name the session had when the pane was opened.
func groupKey(cmdline, environ []byte, comm, parentComm string, ppid int, parentKey, parentLabel, zsession, cwd string) (key, label string) {
	if m := runnerDirRe.FindSubmatch(cmdline); m != nil {
		return "runner:" + string(m[1]), "CI runner " + string(m[1])
	}
	var zs, zp string
	for _, kv := range bytes.Split(environ, []byte{0}) {
		k, v, _ := strings.Cut(string(kv), "=")
		switch k {
		case "RUNNER_NAME":
			n := v[strings.LastIndexByte(v, '-')+1:]
			if awsRunnerName.MatchString(v) {
				n = v
			}
			return "runner:" + n, "CI runner " + n
		case "ZELLIJ_SESSION_NAME":
			zs = v
		case "ZELLIJ_PANE_ID":
			zp = v
		}
	}
	if zsession != "" {
		zs = zsession
	}
	if parentComm == "zellij" && zs != "" && zp != "" {
		return "pane:" + zs + ":" + zp, zs + " · pane " + zp
	}
	if ppid <= 2 || parentKey == "" {
		if m := runnerDirRe.FindStringSubmatch(cwd + "/"); m != nil {
			return "runner:" + m[1], "CI runner " + m[1]
		}
		l := rootLabel(comm, cmdline)
		return l, l
	}
	return parentKey, parentLabel
}

// interpreters are the programs that run scripts, which rootLabel names by the script they run.
var interpreters = map[string]bool{"bash": true, "sh": true, "zsh": true, "node": true, "python": true, "python3": true, "bun": true, "deno": true}

// rootLabel names interpreter processes by what they run ("node ~/x.js", "bash -c …", "python3 -m mod").
func rootLabel(comm string, cmdline []byte) string {
	if !interpreters[comm] {
		return comm
	}
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	for i := 1; i < len(args); i++ {
		a := args[i]
		if (a == "-c" || a == "-m") && i+1 < len(args) {
			return trunc(comm+" "+a+" "+args[i+1], 60)
		}
		if !strings.HasPrefix(a, "-") {
			return trunc(comm+" "+tilde(a), 60)
		}
	}
	return comm
}

// trunc caps s at n bytes, cutting on a rune boundary.
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ---- sockets ----

// sockScan is a snapshot of the machine's sockets and which process owns each.
type sockScan struct {
	entries  []sockEntry
	inodePID map[string]int
	tcpByPID map[int]int // established TCP sockets per process
}

// scanSockets maps every socket inode to its owning process. Only processes
// we own are readable, which covers everything this panel can act on.
func scanSockets(procRoot string) sockScan {
	s := sockScan{inodePID: map[string]int{}, tcpByPID: map[int]int{}}
	for _, p := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, _ := os.ReadFile(procRoot + "/net/" + p)
		s.entries = append(s.entries, parseProcNet(b, p)...)
	}
	tcpEst := map[string]bool{}
	for _, e := range s.entries {
		if e.Proto[:3] == "tcp" && e.State == "01" {
			tcpEst[e.Inode] = true
		}
	}
	dirs, _ := os.ReadDir(procRoot)
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, _ := os.ReadDir(procRoot + "/" + d.Name() + "/fd")
		for _, fd := range fds {
			l, err := os.Readlink(procRoot + "/" + d.Name() + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(l, "socket:[") {
				continue
			}
			ino := l[8 : len(l)-1]
			s.inodePID[ino] = pid
			if tcpEst[ino] {
				s.tcpByPID[pid]++
			}
		}
	}
	return s
}

// ---- resource sampler ----

// Disk is one mounted filesystem's capacity and use.
type Disk struct {
	Mount string `json:"mount"`
	Size  uint64 `json:"size"`
	Used  uint64 `json:"used"`
}

// Proc is one process in a resource group, with its CPU and IO rates since the last sample.
type Proc struct {
	PID   int     `json:"pid"`
	Comm  string  `json:"comm"`
	Cmd   string  `json:"cmd"`
	State string  `json:"state"`
	CPU   float64 `json:"cpu"`
	RSS   uint64  `json:"rss"`
	Rd    float64 `json:"rd"`
	Wr    float64 `json:"wr"`
	Conns int     `json:"conns"`
}

// ProcGroup is the processes of one app (a CI runner, a zellij pane, a daemon), added up.
type ProcGroup struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	CPU   float64 `json:"cpu"`
	RSS   uint64  `json:"rss"`
	Rd    float64 `json:"rd"`
	Wr    float64 `json:"wr"`
	Conns int     `json:"conns"`
	N     int     `json:"n"`
	Procs []Proc  `json:"procs"`
	RSS1h *int64  `json:"rss1h,omitempty"` // RSS change over the last hour; nil until the panel has watched that long
}

// Resources is what /api/resources sends: the machine's totals and its process groups.
type Resources struct {
	CPU       float64             `json:"cpu"`
	Cores     []float64           `json:"cores"`
	Load      [3]float64          `json:"load"`
	Uptime    float64             `json:"uptime"`
	MemTotal  uint64              `json:"memTotal"`
	MemAvail  uint64              `json:"memAvail"`
	SwapTotal uint64              `json:"swapTotal"`
	SwapFree  uint64              `json:"swapFree"`
	MemFree   uint64              `json:"memFree"`
	Anon      uint64              `json:"anon"`
	Cache     uint64              `json:"cache"` // Cached + Buffers − Shmem
	Slab      uint64              `json:"slab"`
	SReclaim  uint64              `json:"sreclaim"`
	Shmem     uint64              `json:"shmem"`
	Dirty     uint64              `json:"dirty"`
	PSI       PSI                 `json:"psi"`
	Disks     []Disk              `json:"disks"`
	NetRx     float64             `json:"netRx"`
	NetTx     float64             `json:"netTx"`
	NProcs    int                 `json:"nprocs"`
	Groups    []ProcGroup         `json:"groups"`
	Limits    []Limit             `json:"limits"`
	all       []ProcGroup         // every group, for joining pane usage onto agents
	panes     map[string]paneInfo // by pane group key "pane:<session>:<id>"
}

// paneInfo is what runs in a zellij pane: its root process (child of the zellij server), the
// terminal's foreground process group leader, and when the terminal last got output.
type paneInfo struct {
	Root     int
	Comm     string // root's comm (zsh, htop, …)
	Agent    string // "claude" or "codex" if one runs anywhere in the pane
	AgentPID int    // the newest process of that name in it
	FgPID    int
	FgComm   string
	FgState  string
	FgCmd    string
	FgSince  int64  // unix start of the foreground leader
	Output   int64  // unix mtime of the pane's tty: the kernel bumps it on writes (8 s granularity)
	Cwd      string // working directory of the foreground process (the shell's, at a prompt)
	Ports    []int  // TCP ports something in the pane listens on
	CPU      float64
	RSS      uint64
	N        int
}

// procInfo is what the sampler remembers about a process between samples.
type procInfo struct {
	start      uint64
	ppid       int
	comm       string
	key, label string
	cmd        string
	ticks      uint64
	rd, wr     uint64
	fresh      bool // not yet sampled
	headless   bool // a one-off claude -p / codex exec (read off the whole command line: cmd is cut at 200)
}

// sysSampler reads /proc (or a copy of it in tests) and keeps the previous readings to turn counters into rates.
type sysSampler struct {
	root    string
	sockDir string // zellij session sockets
	liveMu  sync.Mutex
	live    []string
	prevT   time.Time
	prevCPU []cpuTimes
	prevRx  uint64
	prevTx  uint64
	procs   map[int]*procInfo
	snaps   []rssSnap
}

// rssSnap is every group's RSS at one moment: one is kept every 10 min, the last 7.
type rssSnap struct {
	t   time.Time
	rss map[string]uint64
}

// rssBase keeps cur every 10 min and returns what to measure an hour's change against: the
// oldest snapshot once it's ≥ 50 min old, nil before that.
func (s *sysSampler) rssBase(now time.Time, cur map[string]uint64) map[string]uint64 {
	if n := len(s.snaps); n == 0 || now.Sub(s.snaps[n-1].t) >= 10*time.Minute {
		s.snaps = append(s.snaps, rssSnap{now, cur})
		s.snaps = s.snaps[max(0, len(s.snaps)-7):]
	}
	if now.Sub(s.snaps[0].t) >= 50*time.Minute {
		return s.snaps[0].rss
	}
	return nil
}

// newSysSampler returns a sampler that reads procRoot (normally /proc).
func newSysSampler(procRoot string) *sysSampler {
	return &sysSampler{root: procRoot, procs: map[int]*procInfo{}}
}

// defaultSockDir finds zellij's session socket dir; looked up on use, since zellij may start after the panel.
func defaultSockDir() string {
	// where zellij puts them: its temp dir ($TMPDIR, else /tmp), like os.TempDir
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), fmt.Sprintf("zellij-%d", os.Getuid()), "contract_version_*"))
	if len(dirs) == 0 {
		return ""
	}
	return dirs[len(dirs)-1]
}

// keyOf is the group key of a pid seen by the last sample ("pane:dev-1:4", "runner:r1", …).
// Call it from the goroutine that samples.
func (s *sysSampler) keyOf(pid int) string {
	if pi := s.procs[pid]; pi != nil {
		return pi.key
	}
	return ""
}

// pidsByKey groups the pids of the last sample by group key, ancestors before descendants (so a
// pane's shell is stopped before, and continued after, the job it runs). Call it from the sampling goroutine.
func (s *sysSampler) pidsByKey() map[string][]int {
	depth := map[int]int{}
	var d func(pid, n int) int
	d = func(pid, n int) int {
		pi := s.procs[pid]
		if pi == nil || n > 64 {
			return 0
		}
		if v, ok := depth[pid]; ok {
			return v
		}
		depth[pid] = 1 + d(pi.ppid, n+1)
		return depth[pid]
	}
	out := map[string][]int{}
	for pid, pi := range s.procs {
		d(pid, 0)
		out[pi.key] = append(out[pi.key], pid)
	}
	for _, pids := range out {
		sort.Slice(pids, func(i, j int) bool {
			a, b := pids[i], pids[j]
			return depth[a] < depth[b] || (depth[a] == depth[b] && a < b)
		})
	}
	return out
}

// liveSessions lists zellij sessions whose socket belongs to a running server.
func (s *sysSampler) liveSessions() []string {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return append([]string(nil), s.live...)
}

// zellijSessions maps zellij server pids to their current session names. A server's cmdline names
// the socket it made (--server <dir>/<name>), but a rename moves the socket file and leaves the old
// name there, so the socket's mtime (the moment the server created it at startup) matches the rest.
// Pairs go best first, each socket and server once: timing alone can't tell apart servers started in
// the same second (sessions resurrected back to back), and a random pick there hid one of them.
func zellijSessions(stats map[int]procStat, btime int64, dir string, cmdline func(pid int) []byte) map[int]string {
	out := map[int]string{}
	if dir == "" {
		return out
	}
	cmds := map[int]string{} // server pid → its cmdline
	for pid, st := range stats {
		if st.Comm == "zellij" && st.PPID <= 1 {
			cmds[pid] = strings.TrimRight(string(cmdline(pid)), "\x00")
		}
	}
	type pair struct {
		pid   int
		name  string
		other bool  // the server's cmdline names another socket (renamed since)
		d     int64 // |socket mtime − server start|, seconds
	}
	var pairs []pair
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || fi.IsDir() {
			continue
		}
		for pid, cmd := range cmds {
			d := fi.ModTime().Unix() - (btime + int64(stats[pid].Start/clkTck))
			if d < 0 {
				d = -d
			}
			if d < 4 { // ponytail: ±3 s match window; widen if a slow box ever misses
				pairs = append(pairs, pair{pid, e.Name(), !strings.HasSuffix(cmd, "/"+e.Name()), d})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		a, b := pairs[i], pairs[j]
		if a.other != b.other {
			return !a.other
		}
		if a.d != b.d {
			return a.d < b.d
		}
		if a.pid != b.pid {
			return a.pid < b.pid
		}
		return a.name < b.name
	})
	taken := map[string]bool{}
	for _, p := range pairs {
		if _, ok := out[p.pid]; !ok && !taken[p.name] {
			out[p.pid], taken[p.name] = p.name, true
		}
	}
	return out
}

// headlessAgent reports whether pi is a one-off `claude -p` / `codex exec` that a program runs for an answer (the
// panel's own chapter summaries, in the pane the panel runs in). That is a tool call, not an agent working in the pane.
func headlessAgent(pi *procInfo) bool {
	return pi != nil && (pi.headless || headlessCmd(pi.cmd))
}

// headlessCmd reports whether a command line is a headless run. The -p may come after a long --append-system-prompt
// (a claude wrapper puts one first).
func headlessCmd(cmd string) bool {
	f := strings.Fields(cmd)
	for i, a := range f[min(1, len(f)):] {
		if a == "-p" || a == "--print" || (i == 0 && a == "exec") {
			return true
		}
	}
	return false
}

// runsScript reports whether a shell was started on a script or a -c command, not as an interactive one (zsh, -zsh, zsh -l).
func runsScript(cmd string) bool {
	f := strings.Fields(cmd)
	for i := 1; i < len(f); i++ {
		if f[i] == "-c" || !strings.HasPrefix(f[i], "-") {
			return true
		}
	}
	return false
}

// parseBtime returns the boot time (unix seconds) from the contents of /proc/stat.
func parseBtime(stat []byte) int64 {
	for _, line := range strings.Split(string(stat), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// read returns the contents of a file under the sampler's /proc, empty if it can't be read.
func (s *sysSampler) read(name string) []byte {
	b, _ := os.ReadFile(s.root + "/" + name)
	return b
}

// maxGroups and maxProcsPerGroup cap how many groups, and how many processes in each, a sample reports.
const maxGroups, maxProcsPerGroup = 40, 15

// sample reads the machine's state and returns it, with each process put in its group and the rates measured
// since the previous sample (zero on the first).
func (s *sysSampler) sample(now time.Time, sock sockScan) Resources {
	var r Resources
	dt := now.Sub(s.prevT).Seconds()
	first := s.prevT.IsZero() || dt <= 0

	statB := s.read("stat")
	cpu := parseCPU(statB)
	if len(cpu) > 0 && len(s.prevCPU) == len(cpu) {
		r.CPU = cpuPct(s.prevCPU[0], cpu[0])
		for i := 1; i < len(cpu); i++ {
			r.Cores = append(r.Cores, cpuPct(s.prevCPU[i], cpu[i]))
		}
	}
	s.prevCPU = cpu

	m := parseMeminfo(s.read("meminfo"))
	r.MemTotal, r.MemAvail, r.SwapTotal, r.SwapFree = m["MemTotal"], m["MemAvailable"], m["SwapTotal"], m["SwapFree"]
	r.Anon, r.Cache, r.Slab = memParts(m)
	r.MemFree, r.SReclaim, r.Shmem, r.Dirty = m["MemFree"], m["SReclaimable"], m["Shmem"], m["Dirty"]
	r.PSI = parsePSI(s.read("pressure/memory")) // missing on old kernels: all zeros
	if f := strings.Fields(string(s.read("loadavg"))); len(f) >= 3 {
		for i := range 3 {
			r.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if f := strings.Fields(string(s.read("uptime"))); len(f) > 0 {
		r.Uptime, _ = strconv.ParseFloat(f[0], 64)
	}
	rx, tx := parseNetDev(s.read("net/dev"))
	if !first && rx >= s.prevRx && tx >= s.prevTx {
		r.NetRx, r.NetTx = float64(rx-s.prevRx)/dt, float64(tx-s.prevTx)/dt
	}
	s.prevRx, s.prevTx = rx, tx
	r.Disks = disks(s.read("mounts"))

	stats := map[int]procStat{}
	dirs, _ := os.ReadDir(s.root)
	for _, d := range dirs {
		if pid, err := strconv.Atoi(d.Name()); err == nil {
			if st, ok := parseStat(s.read(d.Name() + "/stat")); ok { // !ok: exited mid-scan
				stats[pid] = st
			}
		}
	}
	sockDir := s.sockDir
	if sockDir == "" && s.root == "/proc" {
		sockDir = defaultSockDir()
	}
	btime := parseBtime(statB)
	zs := zellijSessions(stats, btime, sockDir, func(pid int) []byte { return s.read(strconv.Itoa(pid) + "/cmdline") })
	live := make([]string, 0, len(zs))
	for _, name := range zs {
		live = append(live, name)
	}
	sort.Strings(live)
	s.liveMu.Lock()
	s.live = live
	s.liveMu.Unlock()

	// resolve assigns group keys parents-first (pids wrap, so pid order isn't tree order).
	var resolve func(pid, depth int) *procInfo
	resolve = func(pid, depth int) *procInfo {
		st, ok := stats[pid]
		if !ok {
			return nil
		}
		if pi := s.procs[pid]; pi != nil && pi.start == st.Start && pi.comm == st.Comm {
			return pi // comm changes on exec, which keeps pid and start time
		}
		base := strconv.Itoa(pid) + "/"
		var pk, pl, pcomm string
		if parent := stats[st.PPID]; depth < 64 {
			if pp := resolve(st.PPID, depth+1); pp != nil {
				pk, pl, pcomm = pp.key, pp.label, parent.Comm
			}
		}
		cmdline := s.read(base + "cmdline")
		cwd, _ := os.Readlink(s.root + "/" + base + "cwd")
		key, label := groupKey(cmdline, s.read(base+"environ"), st.Comm, pcomm, st.PPID, pk, pl, zs[st.PPID], cwd)
		cmd := strings.TrimSpace(string(bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '})))
		if cmd == "" {
			cmd = "[" + st.Comm + "]"
		}
		pi := &procInfo{start: st.Start, comm: st.Comm, key: key, label: label, cmd: cmd[:min(len(cmd), 200)], fresh: true, headless: headlessCmd(cmd)}
		if old := s.procs[pid]; old != nil && old.start == st.Start { // exec'd: keep its counters
			pi.fresh, pi.ticks, pi.rd, pi.wr = false, old.ticks, old.rd, old.wr
		}
		s.procs[pid] = pi
		return pi
	}

	groups := map[string]*ProcGroup{}
	for pid, st := range stats {
		pi := resolve(pid, 0)
		pi.ppid = st.PPID
		io := parseMeminfo(s.read(strconv.Itoa(pid) + "/io")) // same "key: value" shape; unreadable → zeros
		ticks := st.UTime + st.STime
		if pi.fresh {
			pi.fresh = false
			if first { // baseline only; a process born since the last sample counts from zero
				pi.ticks, pi.rd, pi.wr = ticks, io["read_bytes"], io["write_bytes"]
			}
		}
		p := Proc{PID: pid, Comm: st.Comm, Cmd: pi.cmd, State: st.State, RSS: st.RSS * pageSize, Conns: sock.tcpByPID[pid]}
		if !first {
			p.CPU = float64(ticks-min(ticks, pi.ticks)) / clkTck / dt * 100
			p.Rd = float64(io["read_bytes"]-min(io["read_bytes"], pi.rd)) / dt
			p.Wr = float64(io["write_bytes"]-min(io["write_bytes"], pi.wr)) / dt
		}
		pi.ticks, pi.rd, pi.wr = ticks, io["read_bytes"], io["write_bytes"]

		g := groups[pi.key]
		if g == nil {
			g = &ProcGroup{Key: pi.key, Label: pi.label}
			groups[pi.key] = g
		}
		g.CPU += p.CPU
		g.RSS += p.RSS
		g.Rd += p.Rd
		g.Wr += p.Wr
		g.Conns += p.Conns
		g.N++
		g.Procs = append(g.Procs, p)
	}
	for pid := range s.procs {
		if _, ok := stats[pid]; !ok {
			delete(s.procs, pid)
		}
	}
	r.NProcs = len(stats)
	r.panes = map[string]paneInfo{}
	for pid, st := range stats {
		pi := s.procs[pid]
		if pi == nil || !strings.HasPrefix(pi.key, "pane:") || stats[st.PPID].Comm != "zellij" {
			continue
		}
		if old, ok := r.panes[pi.key]; ok && stats[old.Root].Start > st.Start {
			continue // two roots (a pane replaced in place): the newer one is on screen
		}
		info := paneInfo{Root: pid, Comm: st.Comm}
		if g := groups[pi.key]; g != nil {
			info.CPU, info.RSS, info.N = g.CPU, g.RSS, g.N
			for _, pr := range g.Procs {
				if (pr.Comm == "claude" || pr.Comm == "codex") && !headlessAgent(s.procs[pr.PID]) && (info.AgentPID == 0 || stats[pr.PID].Start > stats[info.AgentPID].Start) {
					info.Agent, info.AgentPID = pr.Comm, pr.PID
				}
			}
		}
		fgPID := st.TPGID
		if fgPID == pid && shellComms[st.Comm] && runsScript(pi.cmd) {
			// a script (bash ./run-x.sh) has no job control: what it runs stays in its process group, so the
			// terminal's foreground is the script itself; its newest child in that group is what it waits on
			for q, qst := range stats {
				if qst.PGRP == pid && qst.PPID == pid && (fgPID == pid || qst.Start > stats[fgPID].Start || qst.Start == stats[fgPID].Start && q > fgPID) {
					fgPID = q
				}
			}
		}
		if fg, ok := stats[fgPID]; ok && fgPID > 0 {
			info.FgPID, info.FgComm, info.FgState = fgPID, fg.Comm, fg.State
			info.FgSince = btime + int64(fg.Start/clkTck)
			if fp := s.procs[fgPID]; fp != nil {
				info.FgCmd = fp.cmd
			}
		}
		if fi, err := os.Stat(s.root + "/" + strconv.Itoa(pid) + "/fd/0"); err == nil {
			info.Output = fi.ModTime().Unix()
		}
		cwdOf := pid
		if info.FgPID > 0 {
			cwdOf = info.FgPID
		}
		info.Cwd, _ = os.Readlink(s.root + "/" + strconv.Itoa(cwdOf) + "/cwd")
		r.panes[pi.key] = info
	}
	// what each pane's processes listen on, from the socket scan the security view already makes (every 10 s)
	for _, e := range sock.entries {
		if e.Proto[:3] != "tcp" || e.State != "0A" {
			continue
		}
		pi := s.procs[sock.inodePID[e.Inode]]
		if pi == nil {
			continue
		}
		if info, ok := r.panes[pi.key]; ok && !slices.Contains(info.Ports, int(e.Local.Port())) {
			info.Ports = append(info.Ports, int(e.Local.Port()))
			slices.Sort(info.Ports)
			r.panes[pi.key] = info
		}
	}
	cur := make(map[string]uint64, len(groups))
	for k, g := range groups {
		cur[k] = g.RSS
	}
	base := s.rssBase(now, cur)
	for _, g := range groups {
		if base != nil {
			d := int64(g.RSS) - int64(base[g.Key]) // a group born since counts from zero
			g.RSS1h = &d
		}
		sort.Slice(g.Procs, func(i, j int) bool {
			a, b := g.Procs[i], g.Procs[j]
			return a.CPU > b.CPU || (a.CPU == b.CPU && a.RSS > b.RSS)
		})
		g.Procs = g.Procs[:min(len(g.Procs), maxProcsPerGroup)]
		r.Groups = append(r.Groups, *g)
	}
	sort.Slice(r.Groups, func(i, j int) bool {
		a, b := r.Groups[i], r.Groups[j]
		return a.CPU > b.CPU || (a.CPU == b.CPU && a.RSS > b.RSS)
	})
	// the top 40 by CPU, plus any of the top 8 by RAM that idle below them (Who's using memory)
	r.all = r.Groups
	byRSS := slices.Clone(r.all)
	sort.SliceStable(byRSS, func(i, j int) bool { return byRSS[i].RSS > byRSS[j].RSS }) // ties keep CPU order
	top := map[string]bool{}
	for _, g := range byRSS[:min(len(byRSS), 8)] {
		top[g.Key] = true
	}
	r.Groups = nil
	for i, g := range r.all {
		if i < maxGroups || top[g.Key] {
			r.Groups = append(r.Groups, g)
		}
	}
	s.prevT = now
	return r
}

// disks lists real mounts: block devices plus the root filesystem, skipping hidden mountpoints.
func disks(mounts []byte) []Disk {
	var out []Disk
	seen := map[string]bool{}
	for _, line := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || seen[f[1]] || strings.HasPrefix(f[1], "/.") {
			continue
		}
		if !strings.HasPrefix(f[0], "/dev/") && f[1] != "/" {
			continue
		}
		var st syscall.Statfs_t
		if syscall.Statfs(f[1], &st) != nil || st.Blocks == 0 {
			continue
		}
		seen[f[1]] = true
		bs := uint64(st.Bsize)
		out = append(out, Disk{Mount: f[1], Size: st.Blocks * bs, Used: (st.Blocks - st.Bfree) * bs})
	}
	return out
}

// ---- 1 h history of cheap totals (runs even with no clients: 4 small reads / 10 s) ----

// histPoint is one point of the hour's history of cheap totals.
type histPoint struct {
	T     int64   `json:"t"`
	CPU   float64 `json:"cpu"`
	Mem   float64 `json:"mem"`
	Rx    float64 `json:"rx"`
	Tx    float64 `json:"tx"`
	Apps  uint64  `json:"apps"` // memParts, bytes
	Cache uint64  `json:"cache"`
	Kern  uint64  `json:"kern"`
	PSI   float64 `json:"psi"` // memory pressure, some avg10
}

// history keeps the last few points of CPU, memory and network totals, sampled whether or not a page is open.
type history struct {
	root    string
	mu      sync.Mutex
	pts     []histPoint
	cap     int
	prevT   time.Time
	prevCPU cpuTimes
	prevRx  uint64
	prevTx  uint64
}

// newHistory returns a history of procRoot that keeps capacity points.
func newHistory(procRoot string, capacity int) *history {
	return &history{root: procRoot, cap: capacity}
}

// tick takes one reading and appends a point to the history.
func (h *history) tick(now time.Time) {
	st, _ := os.ReadFile(h.root + "/stat")
	mi, _ := os.ReadFile(h.root + "/meminfo")
	nd, _ := os.ReadFile(h.root + "/net/dev")
	ps, _ := os.ReadFile(h.root + "/pressure/memory")
	cpu := parseCPU(st)
	m := parseMeminfo(mi)
	rx, tx := parseNetDev(nd)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.prevT.IsZero() && len(cpu) > 0 {
		dt := now.Sub(h.prevT).Seconds()
		p := histPoint{T: now.Unix(), CPU: cpuPct(h.prevCPU, cpu[0]), PSI: parsePSI(ps).Some10}
		p.Apps, p.Cache, p.Kern = memParts(m)
		if m["MemTotal"] > 0 {
			p.Mem = float64(m["MemTotal"]-m["MemAvailable"]) / float64(m["MemTotal"]) * 100
		}
		if dt > 0 && rx >= h.prevRx && tx >= h.prevTx {
			p.Rx, p.Tx = float64(rx-h.prevRx)/dt, float64(tx-h.prevTx)/dt
		}
		h.pts = append(h.pts, p)
		if len(h.pts) > h.cap {
			h.pts = h.pts[len(h.pts)-h.cap:]
		}
	}
	h.prevT, h.prevRx, h.prevTx = now, rx, tx
	if len(cpu) > 0 {
		h.prevCPU = cpu[0]
	}
}

// points returns a copy of the history, oldest first.
func (h *history) points() []histPoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]histPoint(nil), h.pts...)
}

// procStartOf reads a pid's start ticks; ok=false if it no longer exists.
func procStartOf(pid int) (procStat, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, false
	}
	return parseStat(b)
}
