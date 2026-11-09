// server.go is the panel's core: the config and its validation, the server that wires every part together, the
// embedded web assets, the route table and the middleware in front of it (Cloudflare Access, sessions, security
// headers), and the live stream with the sampling loop that feeds it.

package main

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// webFS is the web UI (the web/ directory), compiled into the binary.
//
//go:embed web
var webFS embed.FS

// config is the panel's settings, filled in from the command-line flags (see main). home is the owner's home
// directory.
type config struct {
	addr, cfTeam, cfAUD                        string
	pinFile, auditFile, ciDir, statusDir, home string
	agentAddr, agentTokenFile                  string
	aiURL, aiKeyFile                           string // the assistant (vm-agent): Hermes API server and its key
	hermesEnv                                  string // Hermes' .env: the Telegram settings Connections writes
	compactScope                               string // the zellij sessions the compactor types into (regexp; "": all)
	insecure                                   bool
}

// teamRe matches a valid Cloudflare Zero Trust team name.
var teamRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validateConfig rejects settings the panel must not run with: a listen address that is not loopback, a bad
// -compact-sessions regexp or agent address, or missing Cloudflare Access settings (unless
// -insecure-no-access).
func validateConfig(c config) error {
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return fmt.Errorf("bad -addr: %v", err)
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.IsLoopback() {
		return fmt.Errorf("-addr %s is not loopback: the panel must only be reachable through the tunnel", c.addr)
	}
	if _, err := regexp.Compile(c.compactScope); err != nil {
		return fmt.Errorf("bad -compact-sessions: %v", err)
	}
	if c.agentAddr != "" {
		if err := validAgentAddr(c.agentAddr); err != nil {
			return err
		}
	}
	if c.insecure {
		return nil
	}
	if !teamRe.MatchString(c.cfTeam) || c.cfAUD == "" {
		return errors.New("set -cf-team (your Zero Trust team name) and -cf-aud (the Access application AUD tag)")
	}
	return nil
}

// asset is one embedded web file: its bytes, a gzipped copy, its content type and a short content hash used to
// cache-bust it.
type asset struct {
	body, gz []byte
	ctype    string
	hash     string
}

// client is one open live stream: the view it shows, a channel of frames waiting to be sent, and the last frame
// sent (so an unchanged one is skipped).
type client struct {
	view  string
	ch    chan []byte
	last  []byte
	histT int64
}

// send queues msg unless it's what the page has already. A page too slow to take it skips this tick and gets
// the state on a later one, even if nothing changes meanwhile.
func (c *client) send(msg []byte) {
	if bytes.Equal(msg, c.last) {
		return
	}
	select {
	case c.ch <- msg:
		c.last = msg
	default:
	}
}

// server is the whole panel: the trackers and caches behind each page, the open live streams, and the handlers
// that serve them.
type server struct {
	chapters *chapters // one line per stretch of each chat, for the history rail (chapters.go)
	stt      *stt      // the chat boxes' dictation (stt.go)

	scout   scout    // stalled sessions JARVIS offers to take over (scout.go)
	sup     *featSup // the feature supervisor's memory (featsup.go)
	digests *digests // the agents' messages to each other, made readable (digest.go)

	cfg    config
	auth   *auth
	access *access
	audit  *auditLog
	actor  *actor
	assets map[string]*asset

	sys     *sysSampler
	hist    *history
	du      *duIndex
	fsx     *fsIndex // Files: the name index, and the roots the Files API may touch
	version string   // the build the page runs: a hash of index.html, which names every asset's hash
	skills  skillCache
	ci      *ciTracker
	term    *termTracker
	z       *zellij
	limits  *limiter
	arts    artifactCache
	prs     prCache
	cards   prCards
	notes   *notifier
	tokens  *tokScan
	ai      *assistant
	compact *compactor
	br      *browser // the Browser: the owner's Chromium and the agents' (browser.go)

	uploadDir string // chat image uploads, handed to agents by path

	mu         sync.Mutex
	lastWrite  map[string]time.Time // per pane: terminal typing is audited once a minute
	clients    map[*client]bool
	wake       chan struct{}
	lastJWTLog time.Time
	errMu      sync.Mutex // client-errors.jsonl
}

// newServer builds the server from cfg, opening the state files that live beside the audit log and wiring the
// parts together. It starts no listeners; main starts those and the background loops.
func newServer(cfg config) *server {
	audit := openAudit(cfg.auditFile)
	sys := newSysSampler("/proc")
	z := newZellij()
	claude := filepath.Join(cfg.home, ".claude")
	s := &server{
		cfg:       cfg,
		audit:     audit,
		auth:      newAuth(cfg.pinFile, audit, time.Now),
		sys:       sys,
		hist:      newHistory("/proc", 360),
		du:        newDuIndex(cfg.home, filepath.Join(filepath.Dir(cfg.auditFile), "du.json")),
		fsx:       newFsIndex(cfg.home, "/tmp"),
		ci:        &ciTracker{dir: cfg.ciDir, extraDirs: []string{filepath.Join(cfg.home, "ci-aws", "runners")}, titles: newPRTitles(ghTitle), progress: newCIProgressCache(ghCIJobs), remote: &ciRemote{repos: awsRunnerRepos(filepath.Join(cfg.home, "ci-aws", "runners")), gh: ghSlurp}},
		limits:    newLimiter(filepath.Join(filepath.Dir(cfg.pinFile), "limits.json"), onlineCPUs(), realProcOps()),
		z:         z,
		term:      newTermTracker(z, filepath.Join(claude, "sessions"), newTranscripts(filepath.Join(claude, "projects")), &agentsTracker{dir: cfg.statusDir}),
		actor:     &actor{ciDir: cfg.ciDir, run: runCmd},
		clients:   map[*client]bool{},
		lastWrite: map[string]time.Time{},
		uploadDir: filepath.Join(filepath.Dir(cfg.auditFile), "uploads"),
		wake:      make(chan struct{}, 1),
	}
	s.prs.fetch = ghIn
	s.notes = newNotifier(filepath.Join(filepath.Dir(cfg.auditFile), "notes.json"))
	s.tokens = newTokScan(filepath.Join(claude, "projects"), filepath.Join(filepath.Dir(cfg.auditFile), "tokens.json"))
	s.ai = newAssistant(cmp.Or(cfg.aiURL, "http://127.0.0.1:8642"), cmp.Or(cfg.aiKeyFile, filepath.Join(cfg.home, ".config/vm-agent/api-key")), filepath.Join(cfg.home, ".hermes/google_client_secret.json"))
	s.ai.stateFile = filepath.Join(filepath.Dir(cfg.auditFile), "assistant.json")
	s.ai.threadsFile = filepath.Join(filepath.Dir(cfg.auditFile), "threads.json")
	s.ai.leadFile = filepath.Join(cfg.home, ".hermes/LEAD.md")
	s.ai.teamBusy = s.teamBusy
	s.ai.loadPanel()
	s.ai.loadThreads()
	s.chapters = newChapters(filepath.Join(filepath.Dir(cfg.auditFile), "chapters.json"))
	s.scout.file = scoutFile(s.ai.threadsFile)
	s.sup = newFeatSup(filepath.Join(filepath.Dir(cfg.auditFile), "featsup.json"))
	for _, dir := range s.sup.Homes { // a requirements promote cut short by a restart
		s.recoverPromoteTxn(dir)
	}
	s.hookRecord()
	s.digests = newDigests(filepath.Join(filepath.Dir(cfg.auditFile), "digests.json"))
	s.stt = newSTT(filepath.Dir(cfg.auditFile))
	if cfg.aiURL != "" {
		s.ai.watch()() // from the start: a background delegation's result needs a turn even with no page open
	}
	if !cfg.insecure {
		s.access = newAccess(cfg.cfTeam, cfg.cfAUD)
	}
	s.compact = newCompactor(filepath.Join(filepath.Dir(cfg.auditFile), "compact.json"), cfg.compactScope)
	s.term.compactOf = s.compact.of
	s.term.paused = func(key string) bool {
		for _, l := range s.limits.list() {
			if l.Key == key && l.Paused {
				return true
			}
		}
		return false
	}
	s.br = newBrowser(s, filepath.Dir(cfg.auditFile), cfg.home)
	s.loadAssets()
	return s
}

// ---- static assets: embedded, pre-gzipped, cache-busted by content hash ----

// loadAssets reads the embedded web files, gives each a content hash, rewrites index.html so it names every
// other asset with its hash (?v=...), and pre-compresses everything. The page version is a hash of the
// rewritten index.html.
func (s *server) loadAssets() {
	s.assets = map[string]*asset{}
	fs.WalkDir(webFS, "web", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := webFS.ReadFile(p)
		sum := sha256.Sum256(b)
		name := strings.TrimPrefix(p, "web/")
		ct := mime.TypeByExtension(path.Ext(name))
		switch path.Ext(name) {
		case ".json":
			ct = "application/manifest+json"
		case ".svg":
			ct = "image/svg+xml"
		case ".woff2":
			ct = "font/woff2"
		}
		s.assets[name] = &asset{body: b, ctype: ct, hash: hex.EncodeToString(sum[:4])}
		return nil
	})
	if idx := s.assets["index.html"]; idx != nil {
		html := string(idx.body)
		for name, a := range s.assets {
			if name != "index.html" {
				html = strings.ReplaceAll(html, `"`+name+`"`, `"`+name+"?v="+a.hash+`"`)
			}
		}
		idx.body = []byte(html)
		sum := sha256.Sum256(idx.body)
		s.version = hex.EncodeToString(sum[:6])
		idx.hash = s.version // its ETag changes with any asset it names
	}
	for _, a := range s.assets {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(a.body)
		zw.Close()
		a.gz = buf.Bytes()
	}
}

// serveAsset serves an embedded web file, gzipped when the client accepts it. A request carrying the file's
// current hash is cached for a year; index.html is revalidated with its ETag.
func (s *server) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	a := s.assets[name]
	if a == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	if name != "index.html" && r.URL.Query().Get("v") == a.hash {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Vary", "Accept-Encoding")
	h.Set("ETag", `W/"`+a.hash+`"`) // a launch revalidates index.html (304) instead of downloading it again
	body := a.body
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// ---- middleware ----

// ctxKey is the request context key under which requireAccess stores the signed-in identity.
type ctxKey struct{}

// identity returns the Cloudflare Access email requireAccess verified for r ("local" with -insecure-no-access).
func identity(r *http.Request) string { e, _ := r.Context().Value(ctxKey{}).(string); return e }

// clientIP returns the caller's address as Cloudflare reports it, else the socket's. It is for logs only, never
// for a trust decision.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("Cf-Connecting-Ip"); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// handler returns the whole HTTP handler: every route (all behind a signed-in session except the login),
// wrapped in the Cloudflare Access check, the cross-origin protection and the security headers.
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.serveAsset)
	mux.HandleFunc("GET /{file}", s.serveAsset)
	mux.HandleFunc("GET /novnc/{path...}", s.serveAsset) // noVNC: an ES module tree (vnc.js imports it)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.withSession(s.logout))
	mux.HandleFunc("GET /api/me", s.withSession(s.me))
	mux.HandleFunc("POST /api/client-error", s.withSession(s.clientError))
	mux.HandleFunc("GET /api/stream", s.withSession(s.stream))
	mux.HandleFunc("POST /api/action/{name}", s.withSession(s.action))
	mux.HandleFunc("GET /api/pane/stream", s.withSession(s.paneStream))
	mux.HandleFunc("GET /api/pane/chat", s.withSession(s.paneChat))
	mux.HandleFunc("POST /api/pane/input", s.withSession(s.paneInput))
	mux.HandleFunc("POST /api/pane/codexmodel", s.withSession(s.codexModel))
	mux.HandleFunc("POST /api/pane/fit", s.withSession(s.paneFit))
	mux.HandleFunc("GET /api/pane/vnc", s.withSession(s.paneVNC))
	mux.HandleFunc("POST /api/pane/write", s.withSession(s.paneWrite))
	mux.HandleFunc("POST /api/pane/upload", s.withSession(s.paneUpload))
	mux.HandleFunc("GET /api/upload/{name}", s.withSession(s.uploadGet))
	mux.HandleFunc("GET /api/pane/files", s.withSession(s.paneFiles))
	mux.HandleFunc("GET /api/pane/file", s.withSession(s.paneFile))
	mux.HandleFunc("GET /api/pane/file/comments", s.withSession(s.paneFileComments))
	mux.HandleFunc("POST /api/pane/file/comments", s.withSession(s.paneFileComments))
	mux.HandleFunc("POST /api/term/new", s.withSession(s.termNew))
	mux.HandleFunc("POST /api/term/close", s.withSession(s.termClose))
	mux.HandleFunc("POST /api/term/move", s.withSession(s.termMove))
	mux.HandleFunc("POST /api/term/rename", s.withSession(s.termRename))
	mux.HandleFunc("GET /api/pane/peek", s.withSession(s.panePeek))
	mux.HandleFunc("POST /api/limit", s.withSession(s.limit))
	mux.HandleFunc("GET /api/disk", s.withSession(s.diskView))
	mux.HandleFunc("GET /api/pane/skills", s.withSession(s.paneSkills))
	mux.HandleFunc("GET /api/pane/pr", s.withSession(s.panePR))
	mux.HandleFunc("POST /api/compact", s.withSession(s.compactSettings))
	mux.HandleFunc("POST /api/pane/compact", s.withSession(s.paneCompact))
	mux.HandleFunc("GET /api/notes", s.withSession(s.notesList))
	mux.HandleFunc("GET /api/tokens", s.withSession(s.tokensGet))
	mux.HandleFunc("POST /api/pane/unqueue", s.withSession(s.paneUnqueue))
	mux.HandleFunc("POST /api/pane/takebox", s.withSession(s.paneTakeBox))
	mux.HandleFunc("GET /api/term/list", s.withSession(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.term.listing()) }))
	mux.HandleFunc("POST /api/notes/seen", s.withSession(s.notesSeen))
	mux.HandleFunc("GET /api/dirs", s.withSession(s.dirs))
	mux.HandleFunc("GET /api/disk/check", s.withSession(s.diskCheck))
	mux.HandleFunc("POST /api/disk/delete", s.withSession(s.diskDelete))
	mux.HandleFunc("GET /api/fs/list", s.withSession(s.fsList))
	mux.HandleFunc("GET /api/fs/raw", s.withSession(s.fsRaw))
	mux.HandleFunc("GET /api/fs/thumb", s.withSession(s.fsThumb))
	mux.HandleFunc("PUT /api/fs/file", s.withSession(s.fsWrite))
	mux.HandleFunc("POST /api/fs/xlsx", s.withSession(s.fsXlsx))
	mux.HandleFunc("POST /api/fs/timing", s.withSession(s.fsTiming))
	mux.HandleFunc("POST /api/fs/upload", s.withSession(s.fsUpload))
	mux.HandleFunc("POST /api/fs/mkdir", s.withSession(s.fsMkdir))
	mux.HandleFunc("POST /api/fs/rename", s.withSession(s.fsRename))
	mux.HandleFunc("POST /api/fs/move", s.withSession(s.fsMoveTo))
	mux.HandleFunc("POST /api/fs/copy", s.withSession(s.fsCopy))
	mux.HandleFunc("POST /api/fs/delete", s.withSession(s.fsDelete))
	mux.HandleFunc("GET /api/fs/search", s.withSession(s.fsSearch))
	mux.HandleFunc("GET /api/fs/grep", s.withSession(s.fsGrep))
	mux.HandleFunc("GET /api/fs/repos", s.withSession(s.fsRepos))
	mux.HandleFunc("POST /api/fs/clone", s.withSession(s.fsClone))
	mux.HandleFunc("GET /api/browser/ws", s.withSession(s.br.serveWS))
	mux.HandleFunc("GET /api/browser/vnc", s.withSession(s.br.serveVNC))
	mux.HandleFunc("GET /api/browser/ports", s.withSession(s.br.ports))
	mux.HandleFunc("GET /api/browser/projects", s.withSession(s.br.projects))
	mux.HandleFunc("POST /api/browser/run", s.withSession(s.br.run))
	mux.HandleFunc("POST /api/browser/stop", s.withSession(s.br.stop))
	mux.HandleFunc("GET /api/browser/icon", s.withSession(s.br.favicon))
	mux.HandleFunc("GET /api/browser/devtools/f/{ver}/{path...}", s.withSession(s.br.devtoolsFile))
	mux.HandleFunc("GET /api/browser/devtools/ws/{tab}", s.withSession(s.br.devtoolsWS))
	mux.HandleFunc("GET /api/browser/trails", s.withSession(s.br.trails))
	mux.HandleFunc("GET /api/browser/trail", s.withSession(s.br.trail))
	mux.HandleFunc("GET /api/browser/trail/shot", s.withSession(s.br.trailShot))
	mux.HandleFunc("GET /api/browser/trail/call", s.withSession(s.br.trailCallGet))
	mux.HandleFunc("POST /api/browser/trail/edit", s.withSession(s.br.trailEdit))
	mux.HandleFunc("GET /api/ai/stream", s.withSession(s.aiStream))
	mux.HandleFunc("POST /api/ai/card", s.withSession(s.aiCard))
	mux.HandleFunc("GET /api/ai/history", s.withSession(s.aiHistory))
	mux.HandleFunc("POST /api/ai/send", s.withSession(s.aiSend))
	mux.HandleFunc("POST /api/ai/stop", s.withSession(s.aiStop))
	mux.HandleFunc("POST /api/ai/unqueue", s.withSession(s.aiUnqueue))
	mux.HandleFunc("POST /api/ai/session", s.withSession(s.aiSession))
	mux.HandleFunc("GET /api/ai/model", s.withSession(s.aiModel))
	mux.HandleFunc("POST /api/ai/model", s.withSession(s.aiModel))
	mux.HandleFunc("GET /api/ai/google", s.withSession(s.aiGoogle))
	mux.HandleFunc("GET /api/ai/file", s.withSession(s.aiFile))
	mux.HandleFunc("GET /api/ai/telegram", s.withSession(s.aiTelegram))
	mux.HandleFunc("POST /api/ai/telegram", s.withSession(s.aiTelegram))
	mux.HandleFunc("POST /api/ai/restart", s.withSession(s.aiRestart))
	mux.HandleFunc("POST /api/ai/thread", s.withSession(s.aiThreadNew))
	mux.HandleFunc("POST /api/ai/thread/archive", s.withSession(s.aiThreadArchive))
	mux.HandleFunc("GET /api/ai/thread/files", s.withSession(s.aiThreadFiles))
	mux.HandleFunc("GET /api/ai/chapters", s.withSession(s.aiChapters))
	mux.HandleFunc("GET /api/ai/thread/team", s.withSession(s.aiThreadTeam))
	mux.HandleFunc("POST /api/ai/scout", s.withSession(s.aiScout))
	mux.HandleFunc("POST /api/term/feature/answer", s.withSession(s.termFeatureAnswer))
	mux.HandleFunc("POST /api/term/feature/accept", s.withSession(s.termFeatureAccept))
	mux.HandleFunc("POST /api/term/feature/promote", s.withSession(s.termFeaturePromote))
	mux.HandleFunc("POST /api/term/feature/discard", s.withSession(s.termFeatureDiscard))
	mux.HandleFunc("POST /api/term/feature/bind", s.withSession(s.termFeatureBind))
	mux.HandleFunc("POST /api/term/feature/approve", s.withSession(s.termFeatureApprove))
	mux.HandleFunc("POST /api/term/feature/verify", s.withSession(s.termFeatureVerify))
	mux.HandleFunc("POST /api/term/feature/release", s.withSession(s.termFeatureRelease))
	mux.HandleFunc("GET /api/ai/digests", s.withSession(s.aiDigests))
	mux.HandleFunc("GET /api/term/features", s.withSession(s.termFeatures))
	mux.HandleFunc("GET /api/term/feature", s.withSession(s.termFeature))
	mux.HandleFunc("POST /api/term/feature/report", s.withSession(s.termFeatureReport))
	mux.HandleFunc("POST /api/term/feature/doc", s.withSession(s.termFeatureDoc))
	mux.HandleFunc("POST /api/term/feature/brief", s.withSession(s.termFeatureBrief))
	mux.HandleFunc("GET /api/term/feature/events", s.withSession(s.featEventsGet))
	mux.HandleFunc("POST /api/term/feature/seen", s.withSession(s.featSeenPost))
	mux.HandleFunc("POST /api/term/feature/reviewed", s.withSession(s.featReviewedPost))
	mux.HandleFunc("POST /api/term/feature/ticket/reply", s.withSession(s.featTicketReply))
	mux.HandleFunc("POST /api/term/feature/message", s.withSession(s.featMessagePost))
	mux.HandleFunc("GET /api/term/feature/message", s.withSession(s.featMessageGet))
	s.registerV8(mux) // routes other files add through v8Register (featv8.go)
	mux.HandleFunc("POST /api/stt", s.withSession(s.sttPost))
	mux.HandleFunc("GET /api/nav", s.withSession(s.navInfo))
	mux.HandleFunc("POST /api/ai/google", s.withSession(s.aiGoogle))
	hermes := s.withSession(hermesProxy("http://127.0.0.1:9119").ServeHTTP)
	for _, p := range []string{"/hermes/", "/assets/", "/fonts/", "/fonts-terminal/", "/ds-assets/"} { // the dashboard's lazy chunks ask for its absolute asset paths, unprefixed
		mux.Handle(p, hermes)
	}
	return securityHeaders(http.NewCrossOriginProtection().Handler(s.requireAccess(mux)))
}

// securityHeaders sets the Content-Security-Policy and framing headers: a strict policy for the panel, a
// framable one for the embedded single-chat view and the DevTools frame, and a no-framing one for the proxied
// Hermes dashboard.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		if strings.HasPrefix(r.URL.Path, "/hermes/") { // Hermes' dashboard (its own app): no inline-free CSP, still unframeable
			hd.Set("Content-Security-Policy", "frame-ancestors 'none'")
		} else if strings.HasPrefix(r.URL.Path, "/api/browser/devtools/f/") { // Chromium's DevTools, in a frame of the Browser
			hd.Set("Content-Security-Policy", brDTCSP)
			hd.Set("X-Content-Type-Options", "nosniff")
			hd.Set("Referrer-Policy", "no-referrer")
			hd.Set("X-Frame-Options", "SAMEORIGIN")
			h.ServeHTTP(w, r)
			return
		} else if r.URL.Path == "/" && r.URL.Query().Has("embed") { // one chat, in a tile of the panel's own split view (web/split.js)
			hd.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; base-uri 'none'; form-action 'self'; frame-ancestors 'self'")
			hd.Set("X-Content-Type-Options", "nosniff")
			hd.Set("Referrer-Policy", "no-referrer")
			hd.Set("X-Frame-Options", "SAMEORIGIN")
			h.ServeHTTP(w, r)
			return
		} else {
			// style-src allows inline styles: Mermaid's diagrams carry <style> and style attributes. Scripts stay
			// 'self' only, and with img/font/connect locked to 'self' an injected style can't send anything out.
			hd.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		}
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

// requireAccess admits only requests carrying a valid Cloudflare Access token.
func (s *server) requireAccess(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email := "local"
		if s.access != nil {
			var err error
			email, err = s.access.verify(r.Header.Get("Cf-Access-Jwt-Assertion"))
			if err != nil {
				s.mu.Lock()
				logIt := time.Since(s.lastJWTLog) > time.Minute // don't let a local spammer flood the audit log
				if logIt {
					s.lastJWTLog = time.Now()
				}
				s.mu.Unlock()
				if logIt { // RemoteAddr, not Cf-Connecting-Ip: an unverified request can claim any header
					host, _, _ := net.SplitHostPort(r.RemoteAddr)
					s.audit.add("access-reject", trunc(err.Error(), 80)+" "+r.Method+" "+trunc(r.URL.Path, 100), host)
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, email)))
	})
}

// sessionToken returns the session cookie's value, or "".
func sessionToken(r *http.Request) string {
	c, err := r.Cookie("cp_session")
	if err != nil {
		return ""
	}
	return c.Value
}

// withSession wraps h so it only runs for a request with a valid session for the signed-in identity; otherwise
// it answers 401.
func (s *server) withSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.validSession(sessionToken(r), identity(r)) {
			writeJSON(w, 401, map[string]any{"error": "login required", "retryIn": int(s.auth.lockedFor().Seconds())})
			return
		}
		h(w, r)
	}
}

// writeJSON answers with code and v as uncached JSON.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// readJSON decodes at most 4 KB of the request body into v.
func readJSON(r *http.Request, v any) error { return readJSONN(r, v, 4096) }

// readJSONN decodes at most limit bytes of the request body into v.
func readJSONN(r *http.Request, v any, limit int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v)
}

// umask is the process's file-creation mask, read once before anything runs: writeFileAtomic applies it to perm
// as os.WriteFile would.
var umask = func() fs.FileMode { m := syscall.Umask(0); syscall.Umask(m); return fs.FileMode(m) }()

// writeFileAtomic replaces p with b: a fresh temp file beside it (O_EXCL, so a link planted at a fixed temp name
// is never written through), then a rename, so a reader sees the old file or the new one, never half of one.
func writeFileAtomic(p string, b []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Chmod(f.Name(), perm&^umask)
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// pinError maps checkPIN errors to responses; returns true if the request was answered.
func (s *server) pinError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errLocked):
		writeJSON(w, 429, map[string]any{"error": err.Error(), "retryIn": int(s.auth.lockedFor().Seconds())})
	case errors.Is(err, errNoPIN):
		writeJSON(w, 503, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, 401, map[string]any{"error": err.Error()})
	}
	return true
}

// ---- auth endpoints ----

// login handles POST /api/login: it checks the PIN (counting failures towards the lockout) and sets the session
// cookie.
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ PIN string }
	readJSON(r, &body)
	if s.pinError(w, s.auth.checkPIN(body.PIN, clientIP(r))) {
		return
	}
	tok := s.auth.newSession(identity(r))
	http.SetCookie(w, &http.Cookie{Name: "cp_session", Value: tok, Path: "/", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionIdle.Seconds())})
	s.audit.add("login", identity(r), clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// logout handles POST /api/logout: it ends the session and clears the cookie.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.endSession(sessionToken(r))
	http.SetCookie(w, &http.Cookie{Name: "cp_session", Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// clientError logs an uncaught error from a browser, with its user agent, to client-errors.jsonl beside the
// audit log: a failure only one device shows (Safari on a phone) can then be read on the VM. Past 256 KB
// the log rolls over to .1, so a page stuck in an error loop can't fill the disk.
func (s *server) clientError(w http.ResponseWriter, r *http.Request) {
	var e struct{ Kind, Msg, Stack, View string }
	if readJSONN(r, &e, 8192) != nil {
		writeJSON(w, 400, map[string]any{"error": "bad report"})
		return
	}
	line, _ := json.Marshal(map[string]any{"t": time.Now().Unix(), "kind": trunc(e.Kind, 20), "msg": trunc(e.Msg, 500),
		"stack": trunc(e.Stack, 3000), "view": trunc(e.View, 20), "ua": trunc(r.UserAgent(), 300), "ip": clientIP(r)})
	path := filepath.Join(filepath.Dir(s.cfg.auditFile), "client-errors.jsonl")
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if fi, err := os.Stat(path); err == nil && fi.Size() > 256<<10 {
		os.Rename(path, path+".1")
	}
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
	w.WriteHeader(204)
}

// me handles GET /api/me: who the panel thinks is signed in, and whether Access checking is off.
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"email": identity(r), "insecure": s.cfg.insecure, "home": s.cfg.home})
}

// ---- actions ----

// action handles POST /api/action/{name}, the allowlisted actions that need no PIN beyond the session: kill a
// process, restart a CI runner. Each is audited.
func (s *server) action(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PID    int
		Runner string
	}
	readJSON(r, &body)
	name := r.PathValue("name")
	var do func() error
	var what string
	switch name {
	case "kill":
		what = fmt.Sprintf("kill %d (%s)", body.PID, trunc(procCmdline(body.PID), 80))
		do = func() error { return s.actor.kill(body.PID) }
	case "restart-runner":
		what = "restart runner " + body.Runner
		do = func() error { return s.actor.restartRunner(body.Runner) }
	default:
		writeJSON(w, 404, map[string]any{"error": "no such action"})
		return
	}
	if err := do(); err != nil {
		s.audit.add("action-fail", what+": "+err.Error(), clientIP(r))
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	s.audit.add("action", what, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "msg": "done: " + what})
}

// ---- live stream ----

// views lists the live-stream views a page can ask for.
var views = map[string]bool{"res": true, "ci": true, "term": true, "ai": true, "files": true, "browser": true} // ai, files, browser: badges only, they fetch their own data

// sse is an open event stream (gzipped when the client accepts it).
type sse struct {
	out   io.Writer
	flush func()
	close func()
}

// startSSE starts a server-sent-events response (gzipped when the client accepts it) and tells the browser to
// retry after 3 s.
func startSSE(w http.ResponseWriter, r *http.Request) (*sse, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	e := &sse{out: w, flush: flusher.Flush, close: func() {}}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		zw := gzip.NewWriter(w)
		h.Set("Content-Encoding", "gzip")
		e.out, e.close = zw, func() { zw.Close() }
		e.flush = func() { zw.Flush(); flusher.Flush() }
	}
	w.WriteHeader(200)
	fmt.Fprint(e.out, "retry: 3000\n\n")
	e.flush()
	return e, true
}

// data sends b as one event.
func (e *sse) data(b []byte) {
	fmt.Fprintf(e.out, "data: %s\n\n", b)
	e.flush()
}

// send sends v as one JSON event.
func (e *sse) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Println("marshal:", err)
		return
	}
	e.data(b)
}

// ping sends a comment line that keeps the connection (and any proxy in front) from idling out.
func (e *sse) ping() {
	fmt.Fprint(e.out, ": ping\n\n")
	e.flush()
}

// stream handles GET /api/stream?view=...: the live feed a page keeps open. It ends when the session does.
func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	if !views[view] {
		view = "res"
	}
	e, ok := startSSE(w, r)
	if !ok {
		return
	}
	defer e.close()

	c, done := s.subscribe(view)
	defer done()
	tok, email := sessionToken(r), identity(r)
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-c.ch:
			e.data(msg)
		case <-ping.C:
			if !s.auth.peekSession(tok, email) {
				return // session gone: the page's reconnect gets a 401 and shows the PIN screen
			}
			e.ping()
		}
	}
}

// subscribe adds a client of view and wakes the sampler, so its first frame comes at once; done removes it.
func (s *server) subscribe(view string) (c *client, done func()) {
	c = &client{view: view, ch: make(chan []byte, 2)}
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return c, func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}
}

// clientCount returns how many live streams are open.
func (s *server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// historyLoop keeps 1 h of cheap totals so charts are full the moment a page opens.
func (s *server) historyLoop() {
	for t := time.NewTicker(10 * time.Second); ; <-t.C { // first tick runs immediately
		s.hist.tick(time.Now())
	}
}

// sampleLoop looks every 2 s while a page is open, and every 10 s with none: enough for the
// notifications to catch what happens while you're away (zellij is asked at most every 30 s then).
func (s *server) sampleLoop() {
	go s.fsx.run() // here, not in newServer: test servers don't walk the disk
	var st sampleState
	for {
		s.tick(time.Now(), &st)
		every := 2 * time.Second
		if s.clientCount() == 0 {
			every = 10 * time.Second
		}
		select {
		case <-time.After(every):
		case <-s.wake:
		}
	}
}

// sampleState is what the sampling loop carries between ticks: the last socket scan and CI poll, and when each
// ran.
type sampleState struct {
	sock         sockScan
	sockAt, ciAt time.Time
	ci           CI
}

// observeTeams runs the agent-team bookkeeping for one tick: what changed in the agents (notes.go), what each
// thread's team needs (featsup.go), and its lead told of both (threads.go, teamDeliver). It is the tick's one
// entry for that, and the tests'.
func (s *server) observeTeams(now time.Time, tm Terminals, ci CI, res Resources) {
	teams := map[string]bool{}
	s.ai.mu.Lock()
	for _, t := range s.ai.threads {
		if t.Status != "archived" {
			teams[t.Team] = true
		}
	}
	s.ai.mu.Unlock()
	s.notes.setTeams(teams)
	s.notes.observe(now, tm, ci, res, func(p TPane) string {
		return lastSaid(cmp.Or(p.Agent.path, s.term.tr.path(p.Agent.sid)))
	})
	s.supervise(now, tm)
	s.reopenStale()
	s.teamDeliver(now)
}

// reopenStale checks every done feature against its record as it is now, and reopens any whose record no longer passes the done gate.
func (s *server) reopenStale() {
	var teams []string
	s.ai.mu.Lock()
	for _, t := range s.ai.threads {
		if t.Status == "done" && t.Team != "" {
			teams = append(teams, t.Team)
		}
	}
	s.ai.mu.Unlock()
	for _, team := range teams {
		s.featReopen(team)
	}
}

// tick takes one sample of the machine, terminals and CI, runs the team bookkeeping, and sends each open stream
// the frame for its view plus the sidebar badges.
func (s *server) tick(now time.Time, st *sampleState) {
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	wantView := map[string]bool{}
	for c := range s.clients {
		clients = append(clients, c)
		wantView[c.view] = true
	}
	s.mu.Unlock()

	// ports and connections are shown only on a page: with none open, nothing needs the sockets
	if now.Sub(st.sockAt) >= 10*time.Second && len(clients) > 0 {
		st.sock, st.sockAt = scanSockets("/proc"), now
	}
	res := s.sys.sample(now, st.sock)
	if wantView["files"] { // its disk-usage chart
		s.du.refresh(now, false)
	}
	if wantView["res"] {
		res.Limits = s.limits.list()
	}
	tm := s.term.update(now, s.sys.liveSessions(), res, s.sys.keyOf, wantView["term"] || s.scout.full.Swap(false)) // or the scout asked for a full read (scout.go)
	s.compactStep(now, tm)
	s.cards.warmAll(tm, s.prCard)
	for _, ss := range tm.Sessions { // each agent's lines, as they change
		for _, tb := range ss.Tabs {
			for _, p := range tb.Panes {
				if p.Agent != nil && p.Agent.path != "" {
					p.Agent.Add, p.Agent.Del = s.arts.lines(p.Agent.path)
				}
			}
		}
	}
	cc := s.compact.config()
	tm.Compact = &cc
	lbl := s.term.labels()
	for i := range res.Groups {
		if l, ok := lbl[res.Groups[i].Key]; ok {
			res.Groups[i].Label = l
		}
	}
	if now.Sub(st.ciAt) >= 5*time.Second {
		st.ci, st.ciAt = s.ci.poll(now, runnerAlive), now
	}
	busy := 0
	for _, r := range st.ci.Runners {
		if r.State == "busy" {
			busy++
		}
	}
	memPct := 0.0
	if res.MemTotal > 0 {
		memPct = float64(res.MemTotal-res.MemAvail) / float64(res.MemTotal) * 100
	}
	s.observeTeams(now, tm, st.ci, res)
	note, unread := s.notes.badge()
	var disk Disk // the sidebar's Resources preview: the biggest disk (the one the work lives on)
	for _, d := range res.Disks {
		if d.Size > disk.Size {
			disk = d
		}
	}
	diskPct := 0.0
	if disk.Size > 0 {
		diskPct = float64(disk.Used) / float64(disk.Size) * 100
	}
	badges := map[string]any{
		"cpu": res.CPU, "mem": memPct, "disk": diskPct, "diskFree": disk.Size - disk.Used, "waiting": tm.Waiting, "asking": tm.Asking, "working": tm.Working,
		"ciBusy": busy, "ciFail": st.ci.FailedDay, "note": note, "unread": unread, "agentBrowsers": s.br.liveAgents(),
	}
	hist := s.hist.points()
	for _, c := range clients {
		var data any
		switch c.view {
		case "res":
			var newPts []histPoint
			for _, p := range hist {
				if p.T > c.histT {
					newPts = append(newPts, p)
				}
			}
			if len(newPts) > 0 {
				c.histT = newPts[len(newPts)-1].T
			}
			data = map[string]any{"res": res, "hist": newPts}
		case "ci":
			data = st.ci
		case "term":
			data = tm
		}
		msg, err := json.Marshal(map[string]any{"view": c.view, "badges": badges, "data": data, "v": s.version})
		if err != nil {
			log.Println("marshal:", err)
			continue
		}
		c.send(msg)
	}
}

// procComm returns the process name of pid from /proc, or "".
func procComm(pid int) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return strings.TrimSpace(string(b))
}

// commRunning reports whether any process is named name.
func commRunning(name string) bool {
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		if pid, err := strconv.Atoi(d.Name()); err == nil && procComm(pid) == name {
			return true
		}
	}
	return false
}
