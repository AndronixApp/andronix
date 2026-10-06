package app

import "testing"

func TestDrew(t *testing.T) {
	px := func(n int, b, g, r byte) []byte {
		out := make([]byte, 0, 4*n)
		for i := 0; i < n; i++ {
			out = append(out, b, g, r, 0)
		}
		return out
	}
	// A black screen: nothing.
	if b, tot := brightPixels(px(1000, 0, 0, 0)); drewFrom(b, tot) {
		t.Error("black counted as drawn")
	}
	// Our wallpaper's base colour (#15110e) is dark too.
	if b, tot := brightPixels(px(1000, 0x0e, 0x11, 0x15)); drewFrom(b, tot) {
		t.Error("the wallpaper's base colour counted as drawn")
	}
	// The same with an orange wordmark across 2% of the pixels.
	row := append(px(980, 0x0e, 0x11, 0x15), px(20, 0x20, 0x80, 0xf0)...)
	if b, tot := brightPixels(row); !drewFrom(b, tot) {
		t.Error("a drawn desktop counted as black")
	}
	if drewFrom(0, 0) {
		t.Error("no pixels counted as drawn")
	}
}
