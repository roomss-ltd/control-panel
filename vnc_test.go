// Test for vnc.go: the cell sizes a terminal's VNC display is built from.

package main

import "testing"

// The cells xterm draws DejaVu Sans Mono in, measured on its window (40×10 cells at each size): the display a
// terminal's VNC gets is exactly its xterm, no margin, no cut-off row.
func TestVNCCellIsXtermsOwn(t *testing.T) {
	for px, want := range map[int][2]int{13: {8, 17}, 16: {10, 19}, 20: {12, 24}, 21: {13, 25}, 26: {16, 32}, 32: {19, 38}} {
		if w, h := vncCell(px); w != want[0] || h != want[1] {
			t.Errorf("vncCell(%d) = %d×%d, xterm's is %d×%d", px, w, h, want[0], want[1])
		}
	}
}
