// cdp.go is a minimal Chrome DevTools Protocol client, over a pipe or a websocket, used by the Browser feature.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// cdpMsg is one DevTools Protocol message: a call (ID, Method, Params), its reply (ID with Result or Error), or
// an event (Method and Params, no ID).
type cdpMsg struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

// cdpError is the error a Chromium returns for a call.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("cdp %d: %s", e.Code, e.Message) }

// errCDPClosed is what calls get when the connection closes before their reply.
var errCDPClosed = errors.New("the browser connection closed")

// cdpDebug turns on logging of every message (set BR_DEBUG).
var cdpDebug = os.Getenv("BR_DEBUG") != ""

// cdpConn speaks the Chrome DevTools Protocol to one Chromium: the owner's over --remote-debugging-pipe (fd 3
// in, fd 4 out, messages ending in a NUL), an agent's over its localhost websocket. Calls wait for their reply
// by id; events go, in order, to one handler on its own goroutine, so a handler may make calls of its own (the
// reader never waits on it).
type cdpConn struct {
	send func([]byte) error
	stop func()

	mu     sync.Mutex
	id     int64
	wait   map[int64]chan cdpMsg
	queue  []cdpMsg // events the handler hasn't had yet
	qcond  *sync.Cond
	closed bool
	done   chan struct{}

	onEvent func(m cdpMsg)
}

// newCDP returns a connection that writes with send and runs stop when it closes. The caller feeds it replies
// and events through readLoop.
func newCDP(send func([]byte) error, stop func(), onEvent func(cdpMsg)) *cdpConn {
	c := &cdpConn{send: send, stop: stop, wait: map[int64]chan cdpMsg{}, done: make(chan struct{}), onEvent: onEvent}
	c.qcond = sync.NewCond(&c.mu)
	go c.dispatch()
	return c
}

// cdpPipe: Chromium reads commands on the pipe it has as fd 3 (w here) and writes on fd 4 (r here).
func cdpPipe(w io.WriteCloser, r io.Reader, onEvent func(cdpMsg)) *cdpConn {
	var wmu sync.Mutex
	c := newCDP(func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := w.Write(append(b, 0))
		return err
	}, func() { w.Close() }, onEvent)
	br := bufio.NewReaderSize(r, 1<<20)
	go c.readLoop(func() ([]byte, error) {
		b, err := br.ReadBytes(0)
		if err != nil {
			return nil, err
		}
		return b[:len(b)-1], nil
	})
	return c
}

// cdpWS returns a connection over an agent Chromium's debugging websocket.
func cdpWS(ws *wsConn, onEvent func(cdpMsg)) *cdpConn {
	c := newCDP(func(b []byte) error { return ws.write(wsText, b) }, ws.close, onEvent)
	go c.readLoop(func() ([]byte, error) {
		_, b, err := ws.read()
		return b, err
	})
	return c
}

// readLoop reads messages with next until it fails, handing replies to their callers and queueing events for
// the handler; then it closes the connection.
func (c *cdpConn) readLoop(next func() ([]byte, error)) {
	defer c.close()
	for {
		b, err := next()
		if err != nil {
			return
		}
		var m cdpMsg
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		c.mu.Lock()
		if m.ID != 0 {
			if ch := c.wait[m.ID]; ch != nil {
				delete(c.wait, m.ID)
				ch <- m
			}
		} else if m.Method != "" {
			c.queue = append(c.queue, m)
			c.qcond.Signal()
		}
		c.mu.Unlock()
	}
}

// dispatch runs the event handler on queued events in order, on its own goroutine.
func (c *cdpConn) dispatch() {
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.closed {
			c.qcond.Wait()
		}
		if len(c.queue) == 0 {
			c.mu.Unlock()
			return
		}
		m := c.queue[0]
		c.queue[0] = cdpMsg{}
		c.queue = c.queue[1:]
		c.mu.Unlock()
		if c.onEvent != nil {
			c.onEvent(m)
		}
	}
}

// close ends the connection: pending calls fail and the handler stops. Closing twice is fine.
func (c *cdpConn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for id, ch := range c.wait {
		close(ch)
		delete(c.wait, id)
	}
	c.qcond.Broadcast()
	close(c.done)
	c.mu.Unlock()
	if c.stop != nil {
		c.stop()
	}
}

// call sends method(params) to a target's session ("" is the browser itself) and waits for the reply.
func (c *cdpConn) call(session, method string, params any, out any) error {
	return c.callT(30*time.Second, session, method, params, out)
}

// callT calls a protocol method (in a page session, or "" for the browser itself) and decodes the result into
// out, giving up after timeout.
func (c *cdpConn) callT(timeout time.Duration, session, method string, params any, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	ch := make(chan cdpMsg, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errCDPClosed
	}
	c.id++
	id := c.id
	c.wait[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(cdpMsg{ID: id, Method: method, Params: raw, SessionID: session})
	if err := c.send(b); err != nil {
		c.close()
		return errCDPClosed
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case m, ok := <-ch:
		if !ok {
			return errCDPClosed
		}
		if cdpDebug {
			log.Printf("cdp call %s %s -> %v %s", method, trunc(string(raw), 200), m.Error, trunc(string(m.Result), 100))
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-t.C:
		c.mu.Lock()
		delete(c.wait, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: no answer in %s", method, timeout)
	}
}

// fire sends without waiting for the reply (input events: the next one shouldn't queue behind the last one's
// answer), in order: it's written before fire returns. Its reply has no waiter and is dropped.
func (c *cdpConn) fire(session, method string, params any) {
	raw, _ := json.Marshal(params)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.id++
	id := c.id
	c.mu.Unlock()
	b, _ := json.Marshal(cdpMsg{ID: id, Method: method, Params: raw, SessionID: session})
	if c.send(b) != nil {
		c.close()
	}
}

// relay sends another client's message as it came (DevTools', browserdevtools.go): in order, written before relay
// returns, without waiting; its reply goes to done, on a goroutine of its own. Nothing is done when the connection
// closes first: the client is gone with it.
func (c *cdpConn) relay(session, method string, params json.RawMessage, done func(cdpMsg)) {
	ch := make(chan cdpMsg, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.id++
	id := c.id
	c.wait[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(cdpMsg{ID: id, Method: method, Params: params, SessionID: session})
	if c.send(b) != nil {
		c.close()
		return
	}
	go func() {
		if m, ok := <-ch; ok {
			done(m)
		}
	}()
}
