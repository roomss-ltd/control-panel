package main

import (
	"testing"
	"time"
)

func TestStretches(t *testing.T) {
	now := time.Unix(10_000, 0)
	items := []aiItem{
		{K: "a", T: 100, Role: "user", Text: "clean the disk"},
		{K: "a1", T: 101, Role: "assistant", Text: "freed 268 GB"},
		{K: "x", T: 102, Role: "user", Text: "[From the Claude agent in s:1, on your team] done"}, // not the owner: stays in a's stretch
		{K: "b", T: 200, Role: "user", Text: "start a thread"},
		{K: "b1", T: 201, Role: "assistant", Text: "opened it"},
		{K: "c", T: 9_990, Role: "user", Text: "and now?"}, // the last, still fresh: not over yet
	}
	got := stretches(items, map[string]string{"b": "done before"}, false, now)
	if len(got) != 1 || got[0].key != "a" {
		t.Fatalf("want only a (b already summed up, c not over): %+v", got)
	}
	if got := stretches(items, nil, false, now.Add(time.Hour)); len(got) != 3 || got[0].key != "c" {
		t.Fatalf("an hour on, c is over and comes first: %+v", got)
	}
	if got := stretches(items, nil, true, now.Add(time.Hour)); len(got) != 2 {
		t.Fatalf("a live turn keeps the last stretch open: %+v", got)
	}
}
