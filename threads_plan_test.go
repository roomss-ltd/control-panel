package main

import "testing"

func TestParsePlan(t *testing.T) {
	p := parsePlan("Plan:\n- [x] Read the code\n- [~] Write it\n2. [ ] Test it\n* [>] Also doing\nnot a step")
	want := []planStep{{"Read the code", "done"}, {"Write it", "doing"}, {"Test it", "todo"}, {"Also doing", "doing"}}
	if len(p) != len(want) {
		t.Fatalf("got %v", p)
	}
	for i := range want {
		if p[i] != want[i] {
			t.Fatalf("step %d: got %v want %v", i, p[i], want[i])
		}
	}
	if planDone(p) != 1 {
		t.Fatal("planDone")
	}
}
