package main

import (
	"strings"
	"testing"
)

func TestDigestDue(t *testing.T) {
	long := strings.Repeat("dense agent text with IDs #123 and hashes. ", 10)
	its := []aiItem{
		{K: "a", Role: "user", Text: "[From the Claude agent in chris:0, on your team]\n" + long}, // an agent: due
		{K: "b", Role: "user", Text: "[From the owner, in the panel]\n" + long},                   // the owner: never
		{K: "c", Role: "user", Text: "[From Main JARVIS]\nshort and fine"},                        // short: reads fine
		{K: "d", Role: "assistant", Text: long},                                                   // JARVIS's own answer
		{K: "e", Role: "user", Text: "[Team update from the panel]\n" + long},                     // due
		{K: "f", Role: "user", Text: "[From Main JARVIS]\n" + long},                               // done already
		{K: "g", Role: "user", Text: "[Thread context]\n" + long},                                 // context, not a message
	}
	got := digestDue(its, map[string]string{"f": "x"})
	if len(got) != 2 || got[0].key != "e" || got[1].key != "a" || strings.HasPrefix(got[1].text, "[From") {
		t.Fatalf("got %+v", got)
	}
}
