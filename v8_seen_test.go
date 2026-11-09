package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// v8Post POSTs body to path as the owner and returns the status and decoded JSON answer.
func v8Post(t *testing.T, v *supEnv, h map[string]string, path, body string) (int, map[string]any) {
	t.Helper()
	r := v.e.do(t, "POST", path, body, h)
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	var out map[string]any
	json.Unmarshal(b, &out)
	return r.StatusCode, out
}

// Seen: ids of this feature's events only; a bad batch records nothing and says which ids and why; the frontier moves
// only over contiguous ids; it survives a restart; it never reviews.
func TestV8Seen(t *testing.T) {
	v := newSupEnv(t, "")
	for i := 0; i < 5; i++ {
		v.e.s.featEvent(supTeam, featEv{Kind: "message"})
	}
	h := v.e.authed(v.e.login(t))
	before, _ := os.ReadFile(v.e.s.sup.file)
	c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["ev-2","ev-9","x-1"]}`)
	if c != 400 || !strings.Contains(toJSON(out["bad"]), `"ev-9"`) || !strings.Contains(toJSON(out["bad"]), `"x-1"`) || strings.Contains(toJSON(out["bad"]), `"ev-2"`) {
		t.Fatalf("bad batch: %d %v", c, out)
	}
	if now, _ := os.ReadFile(v.e.s.sup.file); string(now) != string(before) {
		t.Fatal("a refused batch recorded something")
	}
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"other-feature","ids":["ev-2"]}`); c != 400 {
		t.Fatalf("another feature's id: %d %v", c, out)
	}
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["ev-4"]}`); c != 200 || out["frontier"] != "ev-0" || out["unseen"] != float64(4) {
		t.Fatalf("seeing ev-4 marked earlier ones: %d %v", c, out)
	}
	v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["ev-1","ev-2"]}`)
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["ev-3","ev-3"]}`); c != 200 || out["frontier"] != "ev-4" || out["unseen"] != float64(1) {
		t.Fatalf("contiguous frontier: %v", out)
	}
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":[]}`); c != 200 || out["frontier"] != "ev-4" {
		t.Fatalf("after a restart: %v", out)
	}
	if len(v.e.s.sup.Reviewed[supTeam]) != 0 {
		t.Fatal("seen reviewed something")
	}
}

// Reviewed: a separate, explicit write, refused when what the owner saw has since changed.
func TestV8Reviewed(t *testing.T) {
	v := newSupEnv(t, "")
	e1, _ := v.e.s.featEvent(supTeam, featEv{Kind: "decision", Step: "aaaaaaaaaaaa", Rev: "r1"})
	h := v.e.authed(v.e.login(t))
	if c, _ := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"ev-7","rev":"r1"}`); c != 400 {
		t.Fatalf("unknown id: %d", c)
	}
	if c, out := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"`+e1.ID+`","rev":"r0"}`); c != 409 || out["changed"] == nil {
		t.Fatalf("old rev: %d %v", c, out)
	}
	e2, _ := v.e.s.featEvent(supTeam, featEv{Kind: "decision", Step: "aaaaaaaaaaaa", Rev: "r2"})
	if c, out := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"`+e1.ID+`","rev":"r1"}`); c != 409 || out["changed"] != e2.ID {
		t.Fatalf("superseded: %d %v", c, out)
	}
	if c, _ := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"`+e2.ID+`","rev":"r2"}`); c != 200 {
		t.Fatalf("review: %d", c)
	}
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	if v.e.s.sup.Reviewed[supTeam][e2.ID] != "r2" {
		t.Fatal("not kept across a restart")
	}
	if s := v.e.s.sup.Seen[supTeam]; s.Frontier != 0 || len(s.Above) != 0 {
		t.Fatal("reviewing marked seen")
	}
}

// Compaction drops the oldest events that aren't awaiting review, wherever they are; events awaiting review stay; past
// twice the cap of those, a new event is refused (said, never silent loss).
func TestV8CompactionKeepsUnreviewed(t *testing.T) {
	old := maxFeatEvents
	t.Cleanup(func() { maxFeatEvents = old })
	maxFeatEvents = 20
	v := newSupEnv(t, "")
	v.e.s.featEvent(supTeam, featEv{Kind: "message"})
	d, _ := v.e.s.featEvent(supTeam, featEv{Kind: "decision"})
	for i := 0; i < maxFeatEvents+5; i++ {
		v.e.s.featEvent(supTeam, featEv{Kind: "message"})
	}
	evs := v.e.s.sup.Events[supTeam]
	if evs[0].ID != d.ID || len(evs) != maxFeatEvents || evs[len(evs)-1].Seq != int64(maxFeatEvents+7) {
		t.Fatalf("compaction: first %s, %d kept", evs[0].ID, len(evs))
	}
	for i := 0; i < 2*maxFeatEvents; i++ {
		v.e.s.featEvent(supTeam, featEv{Kind: "decision"})
	}
	if _, err := v.e.s.featEvent(supTeam, featEv{Kind: "message"}); err == nil || !strings.Contains(err.Error(), "await") {
		t.Fatalf("no backpressure: %v", err)
	}
	if v.e.s.sup.Events[supTeam][0].ID != d.ID {
		t.Fatal("a decision awaiting review was dropped")
	}
}

// Seen across compaction and many sparse receipts: the frontier crosses compacted ids (they're gone, not seen);
// receipts of retained events are never dropped, over a restart too.
func TestV8SeenCompactedAndSparse(t *testing.T) {
	old := maxFeatEvents
	t.Cleanup(func() { maxFeatEvents = old })
	maxFeatEvents = 10
	v := newSupEnv(t, "")
	for i := 0; i < 15; i++ {
		v.e.s.featEvent(supTeam, featEv{Kind: "message"})
	}
	h := v.e.authed(v.e.login(t))
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["ev-3"]}`); c != 400 || !strings.Contains(toJSON(out), "compacted") {
		t.Fatalf("a compacted id: %d %v", c, out)
	}
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["ev-6","ev-7","ev-8","ev-9","ev-10","ev-11","ev-12","ev-13","ev-14","ev-15"]}`); c != 200 || out["frontier"] != "ev-15" || out["unseen"] != float64(0) || out["retainedFrom"] != "ev-6" {
		t.Fatalf("the frontier over a compacted prefix: %d %v", c, out)
	}

	maxFeatEvents = 5000
	w := newSupEnv(t, "")
	w.e.s.sup.mu.Lock()
	for i := int64(1); i <= 2400; i++ {
		w.e.s.sup.Events[supTeam] = append(w.e.s.sup.Events[supTeam], featEv{ID: "ev-" + itoa64(i), Seq: i, Kind: "message"})
	}
	w.e.s.sup.EvSeq[supTeam] = 2400
	w.e.s.sup.save()
	w.e.s.sup.mu.Unlock()
	hw := w.e.authed(w.e.login(t))
	see := func(odd bool) map[string]any {
		var out map[string]any
		for b := int64(0); b < 12; b++ {
			var ids []string
			for i := b*200 + 1; i <= b*200+200; i++ {
				if i%2 == 1 == odd {
					ids = append(ids, "ev-"+itoa64(i))
				}
			}
			_, out = v8Post(t, w, hw, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":`+toJSON(ids)+`}`)
		}
		return out
	}
	if out := see(false); out["unseen"] != float64(1200) {
		t.Fatalf("1200 sparse receipts: %v", out)
	}
	w.e.s.sup = newFeatSup(w.e.s.sup.file)
	if out := see(true); out["frontier"] != "ev-2400" || out["unseen"] != float64(0) {
		t.Fatalf("after a restart, the rest: %v", out)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
