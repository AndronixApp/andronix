package app

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string][2]int{"1600x720": {1600, 720}, "720x1600": {720, 1600}, "2400x2400": {2400, 2400}} {
		w, h, ok := parseSize(in)
		if !ok || w != want[0] || h != want[1] {
			t.Errorf("%s: %d %d %v", in, w, h, ok)
		}
	}
	for _, in := range []string{"", "1600", "16x7", "axb", "1600x720x24"} {
		if _, _, ok := parseSize(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
