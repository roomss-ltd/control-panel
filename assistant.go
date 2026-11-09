package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// The Assistant view: the owner's Hermes agent (projects/vm-agent) through its API server on loopback.
// Every channel's conversation (Telegram's DM sessions, this panel's "panel" session) merged into one chat
// in time order, each message labelled with its channel; what the page sends goes to the panel session. Connections (keys,
// MCP servers, skills, cron) are Hermes' own dashboard, proxied at /hermes/.

// The panel chat's session name, how many items a chat page holds, and the message that wakes a turn after background work.
const (
	aiPanelSession = "panel"
	aiWindow       = 300 // items a chat opens on; scrolling up pages back this many at a time
	// aiWake starts the turn Hermes leaves to its API client when a background delegation finishes: its result
	// sits in the panel session and is folded into the next turn. Not shown: the event row already is.
	aiWake = "[Your background work finished: its results are above. Answer what I asked for now.]"
)

// aiItem is one row of the chat: a message, a thought, a tool call, an error or an event.
type aiItem struct {
	K    string  `json:"k"` // session:message id (stable, the page's dedupe key)
	T    float64 `json:"t"`
	Ch   string  `json:"ch"`   // panel, telegram, agent
	Role string  `json:"role"` // user, assistant (an answer), think, note (narration between tool calls), tool, error, event
	Text string  `json:"text,omitempty"`
	Name string  `json:"name,omitempty"` // tool name (Text: a preview of its input); event: its kind
	Res  string  `json:"res,omitempty"`  // tool: the start of what it returned
	Run  bool    `json:"run,omitempty"`  // tool: no result yet
}

// aiStep: one step of the turn in progress, as the page draws its trace.
type aiStep struct {
	K     string  `json:"k"` // think, note, tool
	Text  string  `json:"text,omitempty"`
	Name  string  `json:"name,omitempty"`
	State string  `json:"state,omitempty"` // tool: run, ok, err
	T0    float64 `json:"t0,omitempty"`
	Dur   float64 `json:"dur,omitempty"`
}

// aiLive: the panel turn in progress.
type aiLive struct {
	Run   string   `json:"run,omitempty"`
	User  string   `json:"user"`
	Text  string   `json:"text"` // the answer so far
	Steps []aiStep `json:"steps,omitempty"`
	Since float64  `json:"since"`
}

// aiState is the assistant's connection state as the page shows it.
type aiState struct {
	Up        bool              `json:"up"`
	Err       string            `json:"err,omitempty"`
	Platforms map[string]string `json:"platforms,omitempty"` // channel -> connected, retrying, …
	Model     string            `json:"model,omitempty"`     // as of the last panel turn
	Pick      string            `json:"pick,omitempty"`      // the model picked for the panel chat ("" = Hermes' default)
}

// aiQueued: a message the owner sent while JARVIS was answering; it goes out when that turn ends, oldest first.
type aiQueued struct {
	ID     string  `json:"id"`
	Text   string  `json:"text"`
	T      float64 `json:"t"`
	images []string
}

// aiConv: one conversation the panel drives: Main ("") or a thread (its id). Each has its own turn in
// progress, the messages queued behind it, the turns Hermes didn't keep, and the delegation result last woken for.
type aiConv struct {
	live   *aiLive
	cancel context.CancelFunc
	queue  []aiQueued
	failed []aiItem
	woke   string
	held   int // turns Hermes refused in a row (restarting, down): the queue waits for it instead of failing message by message
}

// aiSess is what the panel knows of one Hermes session: when it was last active, its message count and its parsed items.
type aiSess struct {
	active float64
	count  int
	items  []aiItem
}

// assistant is the panel's client for Hermes: it merges every channel's conversation into one chat, runs turns, and keeps the threads.
type assistant struct {
	base, keyFile string
	hc            *http.Client
	google        string // where the Google OAuth client file goes (Hermes' google-workspace skill)
	stateFile     string // the panel chat's session and model (aisession.go)
	threadsFile   string // the threads (threads.go)
	leadFile      string // the product leads' playbook (vm-agent hermes/LEAD.md), sent with every thread turn
	// the feature record a thread owns (featdoc.go), when the server has one: its plan as PLAN.md has it (nil: no
	// PLAN.md), a lead's checklist applied to it, and what each lead turn is told of it. Called with mu held.
	featPlan   func(t *thread) []planStep
	featAdapt  func(t *thread, flat string) (planEdit, error) // the new PLAN.md, not yet written
	featBlock  func(t *thread) string
	featDone   func(t *thread, plan []byte) string // why it can't be done with that PLAN.md (nil: the current one)
	featAttach func(t *thread) error               // whether its record can be joined to its (new) team
	// teamBusy: which of these zellij sessions has an agent at work right now (server: the panes' states). nil: none known
	teamBusy func(sessions []string) bool

	mu       sync.Mutex
	sess     map[string]*aiSess
	items    []aiItem            // Main: every channel, oldest first
	thItems  map[string][]aiItem // each thread's conversation, oldest first
	convs    map[string]*aiConv  // "" = Main, else a thread id
	threads  []*thread           // threads.go
	state    aiState
	panel    aiPanel       // the panel chat's current Hermes session and model
	queued   int           // queue ids
	changed  chan struct{} // closed and replaced on every change
	watchers int
	looping  bool
}

// newAssistant makes an assistant for the Hermes API at base, using the API key in keyFile; google is where its Google OAuth client file goes.
func newAssistant(base, keyFile, google string) *assistant {
	return &assistant{base: base, keyFile: keyFile, google: google, sess: map[string]*aiSess{}, convs: map[string]*aiConv{}, thItems: map[string][]aiItem{}, changed: make(chan struct{}), panel: aiPanel{Session: aiPanelSession},
		hc: &http.Client{Transport: &http.Transport{Proxy: nil}}} // loopback: never the agent sessions' proxy
}

// bump wakes everyone waiting for a change to the chat (a.mu held).
func (a *assistant) bump() { // a.mu held
	close(a.changed)
	a.changed = make(chan struct{})
}

// do sends one request to the Hermes API with its key and returns the response; the caller closes the body.
func (a *assistant) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	key, err := os.ReadFile(a.keyFile)
	if err != nil {
		return nil, errors.New("the assistant isn't set up: no API key (run projects/vm-agent/setup.sh)")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(key)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) { // nothing reached it
			return nil, refusedErr{awayErr{errors.New("the assistant isn't running (Hermes gateway down)")}}
		}
		return nil, errors.New("the assistant isn't running (Hermes gateway down)")
	}
	return resp, nil
}

// getJSON GETs path from the Hermes API and decodes the JSON answer into v.
func (a *assistant) getJSON(ctx context.Context, path string, v any) error {
	resp, err := a.do(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("hermes %s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

// ---- reading Hermes' sessions ----

// hSession is a Hermes session as its API lists it.
type hSession struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	Count      int     `json:"message_count"`
	LastActive float64 `json:"last_active"`
	Started    float64 `json:"started_at"`
}

// hMessage is a message of a Hermes session as its API returns it.
type hMessage struct {
	ID         json.RawMessage `json:"id"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Reasoning  string          `json:"reasoning"`
	Timestamp  float64         `json:"timestamp"`
	Kind       string          `json:"display_kind"` // a user row Hermes wrote itself: async_delegation_complete, process_complete, …
}

// text of a message's content: a string, or a list of parts with text.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct{ Type, Text string }
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// hToolCall is a tool call inside an assistant message.
type hToolCall struct {
	ID       string                           `json:"id"`
	Function struct{ Name, Arguments string } `json:"function"`
}

// calls: what a tool call ran. Hermes wraps deferred tools (MCP ones among them) as tool_call{calls: [...]}.
func (c hToolCall) calls() (names, args []string) {
	if c.Function.Name == "tool_call" {
		var w struct {
			Calls []struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"calls"`
		}
		if json.Unmarshal([]byte(c.Function.Arguments), &w) == nil && len(w.Calls) > 0 {
			for _, x := range w.Calls {
				names, args = append(names, x.Name), append(args, string(x.Arguments))
			}
			return
		}
	}
	return []string{c.Function.Name}, []string{c.Function.Arguments}
}

// toolResult: a tool message's text without Hermes' untrusted-content wrapper or the MCP {"result": …} envelope.
func toolResult(s string) string {
	if strings.HasPrefix(s, "<untrusted_tool_result") {
		if i := strings.Index(s, "\n\n"); i >= 0 {
			s = s[i+2:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "</untrusted_tool_result>")
	}
	var env struct {
		Result *string `json:"result"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &env) == nil && env.Result != nil {
		s = *env.Result
	}
	return trunc(strings.TrimSpace(s), 1500)
}

// toolCalls: a list, or that list JSON-encoded in a string.
func toolCalls(raw json.RawMessage) []hToolCall {
	var calls []hToolCall
	if json.Unmarshal(raw, &calls) == nil {
		return calls
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		json.Unmarshal([]byte(s), &calls)
	}
	return calls
}

// toolPreview: the gist of a tool call's input, one line.
func toolPreview(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return trunc(args, 160)
	}
	if len(m) == 0 {
		return ""
	}
	for _, k := range []string{"command", "query", "url", "path", "text", "prompt", "goal", "name"} {
		if v, ok := m[k].(string); ok && v != "" {
			return trunc(strings.Join(strings.Fields(v), " "), 160)
		}
	}
	keys := slices.Sorted(maps.Keys(m)) // no main argument: its scalars, "session=dev-1 · pane=3"
	var parts []string
	for _, k := range keys {
		switch v := m[k].(type) {
		case string, float64, bool:
			parts = append(parts, k+"="+strings.Join(strings.Fields(fmt.Sprint(v)), " "))
		}
	}
	return trunc(strings.Join(parts, " · "), 160)
}

// toItems: a session's messages as chat items. An assistant message that calls tools is the turn's trace:
// its reasoning (think), what it says while working (note) and the calls (tool, with their results once
// the tool messages arrive); one that doesn't is an answer.
func toItems(sessID, ch string, msgs []hMessage) []aiItem {
	var out []aiItem
	byCall := map[string]int{} // tool call id -> its first item
	for _, m := range msgs {
		id := sessID + ":" + strings.Trim(string(m.ID), `"`)
		switch m.Role {
		case "user":
			switch m.Kind {
			case "", "steer": // the owner (steer: said while a turn ran)
				if contentText(m.Content) == aiWake {
					break
				}
				out = append(out, aiItem{K: id, T: m.Timestamp, Ch: ch, Role: "user", Text: contentText(m.Content)})
			case "hidden":
			default: // Hermes waking itself: a subagent or background process finished, a model switch, …
				out = append(out, aiItem{K: id, T: m.Timestamp, Ch: ch, Role: "event", Name: m.Kind, Text: contentText(m.Content)})
			}
		case "assistant":
			if m.Kind == "failed_turn" { // Hermes' stand-in for a turn the model refused or that failed
				out = append(out, aiItem{K: id, T: m.Timestamp, Ch: ch, Role: "error", Name: "failed_turn", Text: contentText(m.Content)})
				break
			}
			calls := toolCalls(m.ToolCalls)
			if r := strings.TrimSpace(m.Reasoning); r != "" {
				out = append(out, aiItem{K: id + ":think", T: m.Timestamp, Ch: ch, Role: "think", Text: trunc(r, 4000)})
			}
			if t := strings.TrimSpace(contentText(m.Content)); t != "" {
				role := "assistant"
				if len(calls) > 0 {
					role = "note"
				}
				out = append(out, aiItem{K: id, T: m.Timestamp, Ch: ch, Role: role, Text: t})
			}
			n := 0
			for _, c := range calls {
				names, args := c.calls()
				for i := range names {
					if i == 0 && c.ID != "" {
						byCall[c.ID] = len(out)
					}
					out = append(out, aiItem{K: id + ":" + strconv.Itoa(n), T: m.Timestamp, Ch: ch, Role: "tool", Name: names[i], Text: toolPreview(args[i]), Run: true})
					n++
				}
			}
		case "tool":
			if i, ok := byCall[m.ToolCallID]; ok {
				out[i].Res, out[i].Run = toolResult(contentText(m.Content)), false
				for j := i + 1; j < len(out) && out[j].Role == "tool" && out[j].Run && out[j].T == out[i].T; j++ {
					out[j].Run = false // the rest of one wrapped batch: answered by the same message
				}
			}
		}
	}
	return out
}

// channelOf: which chat a session belongs to ("" = not shown: cron runs, other API clients, …).
func channelOf(s hSession) string {
	switch {
	case s.Source == "telegram":
		return "telegram"
	case s.ID == aiPanelSession || strings.HasPrefix(s.ID, aiPanelSession+"-"): // every conversation the panel started
		return "panel"
	case strings.HasPrefix(s.ID, "agent-"): // a Claude agent's, through vm-agent's `jarvis` command
		return "agent"
	case strings.HasPrefix(s.ID, threadPrefix): // a product lead's (threads.go)
		return "thread"
	}
	return ""
}

// refresh re-reads what changed in Hermes: its health, and the messages of sessions that moved.
func (a *assistant) refresh(ctx context.Context) {
	var st aiState
	var health struct {
		Platforms map[string]struct {
			State string `json:"state"`
		} `json:"platforms"`
	}
	var sessions []hSession
	if err := a.getJSON(ctx, "/health/detailed", &health); err != nil {
		st.Err = err.Error()
	} else {
		st.Up = true
		st.Platforms = map[string]string{}
		for k, p := range health.Platforms {
			st.Platforms[k] = p.State
		}
		for _, src := range []string{"telegram", "api_server"} {
			var l struct{ Data []hSession }
			if err := a.getJSON(ctx, "/api/sessions?limit=200&source="+src, &l); err == nil {
				sessions = append(sessions, l.Data...)
			}
		}
	}
	type fetched struct {
		id    string
		s     hSession
		items []aiItem
	}
	var got []fetched
	a.mu.Lock()
	known := map[string]aiSess{}
	for id, s := range a.sess {
		known[id] = *s
	}
	panel := a.panel.Session
	a.mu.Unlock()
	pending := map[string]string{} // conversation -> a delegation result on it that no turn has answered yet
	for _, s := range sessions {
		ch := channelOf(s)
		if ch == "" {
			continue
		}
		if k, ok := known[s.ID]; ok && k.active == s.LastActive && k.count == s.Count {
			continue
		}
		msgs, _ := a.history(ctx, s.ID)
		if c, ok := convOf(s.ID, panel); ok {
			pending[c] = unanswered(msgs, time.Now())
		}
		got = append(got, fetched{s.ID, s, toItems(s.ID, ch, msgs)})
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if st.Up {
		st.Model = a.state.Model
	}
	st.Pick = a.state.Pick
	changed := len(got) > 0 || st.Up != a.state.Up || st.Err != a.state.Err || fmt.Sprint(st.Platforms) != fmt.Sprint(a.state.Platforms)
	a.state = st
	for _, f := range got {
		a.sess[f.id] = &aiSess{active: f.s.LastActive, count: f.s.Count, items: f.items}
	}
	if len(got) > 0 {
		a.rebuild()
	}
	for c, p := range pending {
		if cv := a.conv(c); p != "" && p != cv.woke && cv.live == nil && len(cv.queue) == 0 && (c == "" || a.thread(c) != nil) {
			cv.woke = p
			a.start(c, aiWake, nil)
			changed = true
		}
	}
	if changed {
		a.bump()
	}
}

// history: session sess's messages, oldest first, a page at a time. err: a page Hermes didn't give (msgs: those before it).
func (a *assistant) history(ctx context.Context, sess string) (msgs []hMessage, err error) {
	for off := 0; ; off += 500 {
		var page struct{ Data []hMessage }
		if err := a.getJSON(ctx, fmt.Sprintf("/api/sessions/%s/messages?order=oldest&limit=500&offset=%d", url.PathEscape(sess), off), &page); err != nil {
			return msgs, err
		}
		msgs = append(msgs, page.Data...)
		if len(page.Data) < 500 || off > 20000 {
			return msgs, nil
		}
	}
}

// unanswered: the id of the last message when it's a background delegation's result (Hermes' API server
// only records it: the client owns the next turn) from the last half hour, "" otherwise.
func unanswered(msgs []hMessage, now time.Time) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Kind == "hidden" {
			continue
		}
		if m.Role == "user" && m.Kind == "async_delegation_complete" && now.Sub(time.Unix(int64(m.Timestamp), 0)) < 30*time.Minute {
			return strings.Trim(string(m.ID), `"`)
		}
		return ""
	}
	return ""
}

// rebuild sorts every session's items into their conversation: a thread's session into its thread, the rest into Main.
func (a *assistant) rebuild() { // a.mu held
	var all []aiItem
	th := map[string][]aiItem{}
	for id, s := range a.sess {
		if t, ok := strings.CutPrefix(id, threadPrefix); ok {
			th[t] = append(th[t], s.items...)
		} else {
			all = append(all, s.items...)
		}
	}
	for c, cv := range a.convs {
		if c == "" {
			all = append(all, cv.failed...)
		} else {
			th[c] = append(th[c], cv.failed...)
		}
	}
	for _, r := range a.panel.Resets { // where the panel chat started over
		all = append(all, aiItem{K: "new:" + r.Session, T: r.T, Ch: "panel", Role: "event", Name: "new_chat"})
	}
	byT := func(x, y aiItem) int { return cmp.Compare(x.T, y.T) }
	slices.SortStableFunc(all, byT)
	for _, its := range th {
		slices.SortStableFunc(its, byT)
	}
	a.items, a.thItems = all, th
}

// convOf: which conversation the panel drives a Hermes session for ("" Main's current one, or a thread id).
func convOf(sess, panel string) (string, bool) {
	if t, ok := strings.CutPrefix(sess, threadPrefix); ok {
		return t, true
	}
	return "", sess == panel
}

// conv returns conversation c ("" is Main, else a thread id), creating it on first use (a.mu held).
func (a *assistant) conv(c string) *aiConv { // a.mu held
	cv := a.convs[c]
	if cv == nil {
		cv = &aiConv{}
		a.convs[c] = cv
	}
	return cv
}

// itemsOf: a conversation's items (a.mu held).
func (a *assistant) itemsOf(c string) []aiItem {
	if c == "" {
		return a.items
	}
	return a.thItems[c]
}

// watch keeps the chats fresh: Hermes is asked every 2 s while a page shows them, every 15 s otherwise
// (a delegation finishing with no page open still gets its answer).
func (a *assistant) watch() func() {
	a.mu.Lock()
	a.watchers++
	first := !a.looping
	a.looping = true
	a.mu.Unlock()
	if first {
		go func() {
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				a.refresh(ctx)
				cancel()
				a.mu.Lock()
				idle := a.watchers == 0
				a.mu.Unlock()
				if idle {
					time.Sleep(15 * time.Second)
				} else {
					time.Sleep(2 * time.Second)
				}
			}
		}()
	}
	return func() {
		a.mu.Lock()
		a.watchers--
		a.mu.Unlock()
	}
}

// ---- sending: one turn at a time per conversation, streamed into its live turn ----

// send starts a turn in conversation c (Main "" or a thread), or queues the message behind the one running
// (queued). images: data URLs, sent to the model with the text (Hermes' image_url parts).
func (a *assistant) send(c, text string, images ...string) (queued bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sendLocked(c, text, images...)
}

// sendLocked is send with a.mu already held.
func (a *assistant) sendLocked(c, text string, images ...string) (queued bool, err error) { // a.mu held
	if c != "" && a.thread(c) == nil {
		return false, errNoThread
	}
	cv := a.conv(c)
	if cv.live == nil && len(cv.queue) == 0 { // a queue without a live turn: held while Hermes is away, this goes after it
		a.start(c, text, images)
		return false, nil
	}
	if len(cv.queue) >= 10 {
		return false, errors.New("10 messages are already waiting: let JARVIS catch up")
	}
	a.queued++
	cv.queue = append(cv.queue, aiQueued{ID: strconv.Itoa(a.queued), Text: text, T: float64(time.Now().UnixMilli()) / 1000, images: images})
	a.bump()
	return true, nil
}

// start begins a turn of conversation c in the background (a.mu held).
func (a *assistant) start(c, text string, images []string) { // a.mu held
	ctx, cancel := context.WithCancel(context.Background())
	cv := a.conv(c)
	cv.live, cv.cancel = &aiLive{User: text, Since: float64(time.Now().UnixMicro()) / 1e6}, cancel
	a.bump()
	go a.run(ctx, c, text, images)
}

// unqueue takes back a queued message that hasn't gone out yet.
func (a *assistant) unqueue(c, id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cv := a.conv(c)
	i := slices.IndexFunc(cv.queue, func(q aiQueued) bool { return q.ID == id })
	if i < 0 {
		return false
	}
	cv.queue = slices.Delete(cv.queue, i, i+1)
	a.bump()
	return true
}

// run is one turn from start to finish: stream it, refresh the history, record any error, then send the next queued message.
func (a *assistant) run(ctx context.Context, c, text string, images []string) {
	reply, model, kept, err := a.stream(ctx, c, text, images)
	if ctx.Err() != nil && err == nil {
		err = errors.New("stopped")
	}
	rctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	a.refresh(rctx) // the turn's messages, now in Hermes' history
	cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	cv := a.conv(c)
	ch := "panel"
	if c != "" {
		ch = "thread"
	}
	var away awayErr
	if errors.As(err, &away) && ctx.Err() == nil { // Hermes is restarting or down: hold the queue, try again, say so once
		since := cv.live.Since
		cv.queue = append([]aiQueued{{ID: "held:" + strconv.FormatFloat(since, 'f', 6, 64), Text: text, T: since, images: images}}, cv.queue...)
		if cv.held == 0 {
			cv.failed = append(cv.failed, aiItem{K: fmt.Sprintf("failed:%f:e", since), T: float64(time.Now().UnixMicro()) / 1e6, Ch: ch, Role: "error",
				Text: fmt.Sprintf("Hermes isn't taking messages (%v). Messages wait here and go out in order when it's back.", err)})
			cv.failed = cv.failed[max(0, len(cv.failed)-40):]
			a.rebuild()
		}
		cv.held++
		if c != "" {
			teamNotKept(a, c, text, true)
		}
		cv.live, cv.cancel = nil, nil
		time.AfterFunc(min(aiHoldWait<<min(cv.held-1, 4), time.Minute), func() { // 5 s doubling, at most a minute
			a.mu.Lock()
			defer a.mu.Unlock()
			if cv := a.conv(c); cv.live == nil && (c == "" || a.thread(c) != nil) {
				a.next(c)
			}
		})
		a.bump()
		return
	}
	cv.held = 0
	if !kept { // Hermes didn't keep this turn (provider error, stop before any reply): keep it here
		since := cv.live.Since
		msg := cmp.Or(reply, "")
		if err != nil {
			msg = strings.TrimSpace(msg + "\n\n" + err.Error())
		}
		cv.failed = append(cv.failed, aiItem{K: fmt.Sprintf("failed:%f:u", since), T: since, Ch: ch, Role: "user", Text: text})
		if msg != "" {
			end := max(float64(time.Now().UnixMicro())/1e6, since+1e-6) // when it failed: before the next turn starts
			cv.failed = append(cv.failed, aiItem{K: fmt.Sprintf("failed:%f:e", since), T: end, Ch: ch, Role: "error", Text: msg})
		}
		cv.failed = cv.failed[max(0, len(cv.failed)-40):]
		a.rebuild()
	}
	if kept && c != "" {
		teamAcked(a, c, text) // a team hand-off Hermes kept: its lead has it (teamdeliver.go)
	} else if c != "" {
		var r refusedErr
		teamNotKept(a, c, text, errors.As(err, &r))
	}
	if model != "" && c == "" {
		a.state.Model = model
	}
	cv.live, cv.cancel = nil, nil
	a.next(c)
	a.bump()
}

// next starts the first message queued in conversation c, if any (a.mu held, no live turn)
func (a *assistant) next(c string) {
	cv := a.conv(c)
	for len(cv.queue) > 0 { // what the owner sent meanwhile
		next := cv.queue[0]
		cv.queue = cv.queue[1:]
		if c != "" && teamCancelQueued(a, c, next.Text) { // its comments' feature has another lead now
			continue
		}
		if c != "" {
			teamStarted(a, c, next.Text)
		}
		a.start(c, next.Text, next.images)
		break
	}
	a.bump()
}

// refusedErr: Hermes turned a turn down before storing its message (a 4xx or 503, no connection, no session)
type refusedErr struct{ error }

// Unwrap returns the wrapped error.
func (e refusedErr) Unwrap() error { return e.error }

// aiHoldWait is the first wait for Hermes to come back (tests shorten it).
var aiHoldWait = 5 * time.Second // the first wait for Hermes to come back (tests shorten it)

// awayErr: refused because Hermes is down or restarting (503 draining, no connection): worth waiting for, not failing
type awayErr struct{ error }

// Unwrap returns the wrapped error.
func (e awayErr) Unwrap() error { return e.error }

// stream runs one turn of conversation c on its Hermes session and mirrors its events into its live turn.
// kept: Hermes stored it. Main's turns carry the threads' summary, a thread's the lead playbook (threads.go).
func (a *assistant) stream(ctx context.Context, c, text string, images []string) (reply, model string, kept bool, err error) {
	a.mu.Lock()
	sess, model := a.panel.Session, a.panel.Model
	title := "Control panel" // Hermes wants every session's title to be its own
	if sess != aiPanelSession {
		title += " · " + strings.TrimPrefix(sess, aiPanelSession+"-")
	}
	if c != "" {
		sess, title = threadPrefix+c, "Thread · "+c
	}
	instructions := a.instructions(c)
	a.mu.Unlock()
	r, err := a.do(ctx, "POST", "/api/sessions", map[string]any{"id": sess, "title": title})
	if err != nil {
		return "", "", false, refusedErr{err} // the message itself hasn't gone yet
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	r.Body.Close()
	if r.StatusCode != 201 && r.StatusCode != 409 { // 409: it exists already
		return "", "", false, refusedErr{fmt.Errorf("hermes couldn't open the conversation: %s %s", r.Status, trunc(string(b), 300))}
	}
	var msg any = text
	if len(images) > 0 {
		parts := []map[string]any{{"type": "text", "text": text}}
		for _, u := range images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
		}
		msg = parts
	}
	body := map[string]any{"message": msg}
	if instructions != "" {
		body["instructions"] = instructions
	}
	if model != "" {
		body["model"] = model
	}
	resp, err := a.do(ctx, "POST", "/api/sessions/"+url.PathEscape(sess)+"/chat/stream", body)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err = fmt.Errorf("hermes: %s %s", resp.Status, trunc(string(b), 300))
		if resp.StatusCode == 503 {
			err = awayErr{err}
		}
		if resp.StatusCode == 503 || resp.StatusCode >= 400 && resp.StatusCode < 500 { // turned away before its turn ran
			err = refusedErr{err}
		}
		return "", "", false, err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var event string
	last := time.Time{}
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e struct {
			RunID    string `json:"run_id"`
			Delta    string `json:"delta"`
			ToolName string `json:"tool_name"`
			Text     string `json:"text"`
			Args     any    `json:"args"`
			Already  bool   `json:"already_streamed"`
			Content  string `json:"content"`
			Error    any    `json:"error"`
			Messages []any  `json:"messages"`
			Runtime  struct {
				Provider, Model string
			} `json:"runtime"`
		}
		json.Unmarshal([]byte(data), &e)
		a.mu.Lock()
		l := a.conv(c).live
		switch event {
		case "run.started":
			l.Run = e.RunID
			if e.Runtime.Model != "" {
				model = strings.Trim(e.Runtime.Provider+"/"+e.Runtime.Model, "/")
			}
		case "assistant.delta":
			l.Text += e.Delta
		case "assistant.commentary": // what it says while it works: the text streamed so far was narration
			if e.Already {
				l.Text = ""
			}
			l.Steps = append(l.Steps, aiStep{K: "note", Text: e.Text})
		case "tool.started":
			args, _ := json.Marshal(e.Args)
			l.Steps = append(l.Steps, aiStep{K: "tool", Name: e.ToolName, Text: toolPreview(string(args)), State: "run", T0: float64(time.Now().UnixMilli()) / 1000})
		case "tool.completed", "tool.failed":
			for i := len(l.Steps) - 1; i >= 0; i-- {
				if st := &l.Steps[i]; st.K == "tool" && st.State == "run" && st.Name == e.ToolName {
					st.State, st.Dur = "ok", float64(time.Now().UnixMilli())/1000-st.T0
					if event == "tool.failed" {
						st.State = "err"
					}
					break
				}
			}
		case "assistant.completed":
			reply = e.Content
			l.Text = e.Content
		case "run.completed":
			kept = len(e.Messages) > 0
		case "run.failed":
			err = fmt.Errorf("the turn failed: %v", e.Error)
		}
		// deltas come many a second: the page hears about them at most 5 times a second
		if event != "assistant.delta" || time.Since(last) > 200*time.Millisecond {
			last = time.Now()
			a.bump()
		}
		a.mu.Unlock()
	}
	return reply, model, kept, err
}

// stop cancels the running turn of conversation c, in Hermes too.
func (a *assistant) stop(c string) {
	a.mu.Lock()
	cv := a.conv(c)
	run, cancel := "", cv.cancel
	if cv.live != nil {
		run = cv.live.Run
	}
	a.mu.Unlock()
	if run != "" {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		if r, err := a.do(ctx, "POST", "/v1/runs/"+url.PathEscape(run)+"/stop", map[string]any{}); err == nil {
			r.Body.Close()
		}
		c()
	}
	if cancel != nil {
		cancel()
	}
}

// ---- HTTP ----

// aiStream: the chat's last aiWindow items (every channel, each labelled), then what's appended, the turn
// in progress and the channels' state, as they change.
func (s *server) aiStream(w http.ResponseWriter, r *http.Request) {
	e, ok := startSSE(w, r)
	if !ok {
		return
	}
	defer e.close()
	a := s.ai
	defer a.watch()()
	tok, email := sessionToken(r), identity(r)
	c := r.URL.Query().Get("thread") // "" Main
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	look := time.NewTicker(5 * time.Second) // the threads' states follow their teams' panes, which don't wake this
	defer look.Stop()
	lastKey, lastLive, lastState, lastThreads, lastQueue, lastSig, sent := "", "", "", "", "", "", false
	for {
		a.mu.Lock()
		items := a.itemsOf(c)
		cv := a.conv(c)
		live, _ := json.Marshal(cv.live)
		state, _ := json.Marshal(a.state)
		threads, _ := json.Marshal(a.threadViews())
		queue, _ := json.Marshal(cv.queue)
		wait := a.changed
		a.mu.Unlock()
		msg := map[string]any{}
		at := slices.IndexFunc(items, func(it aiItem) bool { return it.K == lastKey })
		changed := at >= 0 && aiSig(items[max(0, at+1-aiWindow):at+1]) != lastSig // a tool's result landed on a sent step
		switch {
		case !sent || (lastKey != "" && at < 0) || changed: // first, the window moved under us, or sent items changed: start over
			msg["kind"] = "init"
			msg["items"] = items[max(0, len(items)-aiWindow):]
			msg["more"] = len(items) > aiWindow
		case at < len(items)-1 || (lastKey == "" && len(items) > 0):
			msg["kind"] = "add"
			msg["items"] = items[at+1:]
		}
		if len(items) > 0 {
			lastKey, lastSig = items[len(items)-1].K, aiSig(items[max(0, len(items)-aiWindow):])
		}
		if string(live) != lastLive || !sent {
			msg["live"], lastLive = json.RawMessage(live), string(live)
		}
		if string(state) != lastState || !sent {
			msg["state"], lastState = json.RawMessage(state), string(state)
		}
		if string(queue) != lastQueue || !sent {
			msg["queue"], lastQueue = json.RawMessage(queue), string(queue)
		}
		if string(threads) != lastThreads || !sent {
			msg["threads"], lastThreads = json.RawMessage(threads), string(threads)
		}
		sent = true
		if len(msg) > 0 {
			if msg["kind"] == nil {
				msg["kind"] = "add"
			}
			b, _ := json.Marshal(msg)
			e.data(b)
		}
		select {
		case <-r.Context().Done():
			return
		case <-wait:
		case <-look.C:
		case <-ping.C:
			if !s.auth.peekSession(tok, email) {
				return
			}
			e.ping()
		}
	}
}

// aiSig: what the page has of these items, to notice when a sent one changes (a tool's result arriving).
func aiSig(its []aiItem) string {
	var b strings.Builder
	for _, it := range its {
		fmt.Fprintf(&b, "%s|%t|%d|%d;", it.K, it.Run, len(it.Res), len(it.Text))
	}
	return b.String()
}

// aiHistory pages back: the aiWindow items before `before` (an item key).
func (s *server) aiHistory(w http.ResponseWriter, r *http.Request) {
	a := s.ai
	a.mu.Lock()
	items := a.itemsOf(r.URL.Query().Get("thread"))
	a.mu.Unlock()
	at := slices.IndexFunc(items, func(it aiItem) bool { return it.K == r.URL.Query().Get("before") })
	if at < 0 {
		writeJSON(w, 404, map[string]any{"error": "unknown message"})
		return
	}
	from := max(0, at-aiWindow)
	writeJSON(w, 200, map[string]any{"items": items[from:at], "more": from > 0})
}

// aiSend (POST /api/ai/send) sends the owner's message (with attached photos and files) to Main or to a thread.
func (s *server) aiSend(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Thread string // "" Main
		Text   string
		Images []string // paths the page uploaded (/api/pane/upload): photos, which the model sees
		Files  []string // and any other file, which JARVIS opens itself
	}
	if err := readJSONN(r, &b, 1<<20); err != nil || (strings.TrimSpace(b.Text) == "" && len(b.Images)+len(b.Files) == 0) || !utf8.ValidString(b.Text) || len(b.Text) > maxPaste {
		writeJSON(w, 400, map[string]any{"error": "type a message (up to 100 KB)"})
		return
	}
	text, images, err := s.aiAttach(strings.TrimSpace(b.Text), b.Images, b.Files)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if b.Thread != "" {
		text = "[From the owner, in the panel]\n" + text
	}
	queued, err := s.ai.send(b.Thread, text, images...)
	if err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	if b.Thread != "" {
		s.ai.ownerAnswered(b.Thread)
	}
	s.audit.add("assistant", cmp.Or(b.Thread, "main")+" "+strconv.Quote(trunc(b.Text, 80)), clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "queued": queued})
}

// aiUnqueue takes back a message still waiting for JARVIS to finish.
func (s *server) aiUnqueue(w http.ResponseWriter, r *http.Request) {
	var b struct{ ID, Thread string }
	if err := readJSONN(r, &b, 1<<10); err != nil || !s.ai.unqueue(b.Thread, b.ID) {
		writeJSON(w, 404, map[string]any{"error": "already sent"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// aiAttach takes what a message carries out of the uploads (they're cleared after a week) into
// ~/jarvis-files/uploads and names each file in the text, so JARVIS can open it and the page can show it.
// Photos: "[Attached image: <path>]", and data URLs for the model to see (within Hermes' 10 MB request limit:
// the page scales them down first). Any other file: "[Attached file: <path>]", opened with JARVIS's own tools.
// Only the page's own uploads; all of them checked before any is moved.
func (s *server) aiAttach(text string, images, files []string) (string, []string, error) {
	if len(images) > 6 || len(images)+len(files) > 10 {
		return "", nil, errors.New("up to 10 files a message, 6 of them photos")
	}
	upload := func(p string) error {
		if name := filepath.Base(p); filepath.Dir(p) != s.uploadDir || !uploadNameRe.MatchString(name) {
			return errors.New("not an upload: " + name)
		}
		if _, err := os.Stat(p); err != nil {
			return errors.New("upload gone: " + filepath.Base(p))
		}
		return nil
	}
	var urls, lines []string
	total := 0
	for _, p := range images {
		if err := upload(p); err != nil {
			return "", nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", nil, err
		}
		ct := http.DetectContentType(b)
		if imageExt[ct] == "" {
			return "", nil, errors.New("not an image: " + filepath.Base(p))
		}
		if total += len(b); total > 6<<20 {
			return "", nil, errors.New("photos too large together (6 MB a message)")
		}
		urls = append(urls, "data:"+ct+";base64,"+base64.StdEncoding.EncodeToString(b))
	}
	for _, p := range files {
		if err := upload(p); err != nil {
			return "", nil, err
		}
	}
	dir := filepath.Join(s.cfg.home, "jarvis-files/uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	keep := func(p, kind string) error {
		dst := filepath.Join(dir, filepath.Base(p))
		if os.Rename(p, dst) != nil { // on another filesystem: copy it over
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, 0o600); err != nil {
				return err
			}
			os.Remove(p)
		}
		lines = append(lines, "[Attached "+kind+": "+dst+"]")
		return nil
	}
	for _, p := range images {
		if err := keep(p, "image"); err != nil {
			return "", nil, err
		}
	}
	for _, p := range files {
		if err := keep(p, "file"); err != nil {
			return "", nil, err
		}
	}
	if len(lines) > 0 {
		text = strings.TrimSpace(text + "\n\n" + strings.Join(lines, "\n"))
	}
	return text, urls, nil
}

// aiStop (POST /api/ai/stop) stops the running turn of Main or of a thread.
func (s *server) aiStop(w http.ResponseWriter, r *http.Request) {
	var b struct{ Thread string }
	readJSONN(r, &b, 1<<10)
	s.ai.stop(b.Thread)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// aiGoogle stores the Google OAuth client (Desktop app JSON from Google Cloud) where Hermes'
// google-workspace skill looks for it; the agent then runs the sign-in in chat.
func (s *server) aiGoogle(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		_, errC := os.Stat(s.ai.google)
		_, errT := os.Stat(filepath.Join(filepath.Dir(s.ai.google), "google_token.json"))
		writeJSON(w, 200, map[string]any{"client": errC == nil, "token": errT == nil})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var c map[string]struct {
		ClientID string `json:"client_id"`
	}
	if json.Unmarshal(body, &c) != nil || (c["installed"].ClientID == "" && c["web"].ClientID == "") {
		writeJSON(w, 400, map[string]any{"error": "that isn't a Google OAuth client file (client_secret_….json, Desktop app)"})
		return
	}
	tmp := s.ai.google + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil || os.Rename(tmp, s.ai.google) != nil {
		writeJSON(w, 500, map[string]any{"error": "couldn't save it"})
		return
	}
	s.audit.add("assistant-google-client", "saved", clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// aiFile serves a file the assistant made or points to (an absolute path under the home directory or /tmp)
// for the chat's file cards and inline images. Images, PDFs and plain text open in the browser; anything else
// (HTML and SVG included: they could run in the panel's origin) downloads.
func (s *server) aiFile(w http.ResponseWriter, r *http.Request) {
	p := filepath.Clean(r.URL.Query().Get("path"))
	under := func(p string) bool { return strings.HasPrefix(p, s.cfg.home+"/") || strings.HasPrefix(p, "/tmp/") }
	real, err := filepath.EvalSymlinks(p)
	if !filepath.IsAbs(p) || !under(p) || err != nil || !under(real) {
		writeJSON(w, 404, map[string]any{"error": "no such file"})
		return
	}
	f, err := os.Open(real)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "no such file"})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeJSON(w, 404, map[string]any{"error": "not a file"})
		return
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(real)))
	inline := (strings.HasPrefix(ct, "image/") && !strings.Contains(ct, "svg")) || ct == "application/pdf" || strings.HasPrefix(ct, "text/plain")
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	if ct == "" || !inline {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": filepath.Base(real)}))
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// dashToken: the dashboard's session token, which its API wants on every call. A loopback dashboard hands it to
// whoever loads its page; the proxy reads it from there (again after a restart mints a new one).
type dashToken struct {
	base string
	mu   sync.Mutex
	tok  string
}

// dashTokenRe finds the session token Hermes' dashboard embeds in its page.
var dashTokenRe = regexp.MustCompile(`__HERMES_SESSION_TOKEN__="([A-Za-z0-9_\-.]+)"`)

// get returns the dashboard's session token, fetching it from the dashboard's page the first time.
func (d *dashToken) get() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tok != "" {
		return d.tok
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}).Get(d.base + "/")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if m := dashTokenRe.FindSubmatch(b); m != nil {
		d.tok = string(m[1])
	}
	return d.tok
}

// reset forgets the token, so the next get fetches a new one.
func (d *dashToken) reset() { d.mu.Lock(); d.tok = ""; d.mu.Unlock() }

// hermesProxy: Hermes' dashboard (keys, MCP servers, skills, cron, channels) under /hermes/, behind the
// panel's Access + PIN; its API also serves the Assistant's control room. Hermes sees a loopback client (its
// Host and Origin guards pass) with the prefix, and the session token its API asks for.
func hermesProxy(target string) http.Handler {
	u, _ := url.Parse(target)
	tok := &dashToken{base: target}
	return &httputil.ReverseProxy{
		ModifyResponse: func(r *http.Response) error {
			if r.StatusCode == 401 { // a restarted dashboard minted a new token
				tok.reset()
			}
			return nil
		},
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			if strings.Contains(r.In.URL.Path, "/api/") {
				r.Out.Header.Set("X-Hermes-Session-Token", tok.get())
			}
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/hermes")
			r.Out.URL.RawPath = ""
			r.Out.Header.Set("X-Forwarded-Prefix", "/hermes")
			r.Out.Header.Del("Origin") // the panel already refused cross-origin requests
			r.Out.Header.Del("Referer")
			r.Out.Header.Del("Cf-Access-Jwt-Assertion") // the panel's credentials stay with the panel
			r.Out.Header.Del("Cookie")
			for _, c := range r.In.Cookies() {
				if c.Name != "cp_session" && c.Name != "CF_Authorization" {
					r.Out.AddCookie(c)
				}
			}
		},
		Transport: &http.Transport{Proxy: nil},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "The Hermes dashboard isn't running (it starts with projects/vm-agent/run-agent.sh).", 502)
		},
	}
}
