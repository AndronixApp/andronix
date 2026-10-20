package app

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func TestGLine(t *testing.T) {
	if got := gLine(nil); got != "G 0\n" {
		t.Errorf("empty: %q", got)
	}
	rs := []geomRect{
		{0, 0, 720, 1600, 0, 0, 720, 1600},       // the desktop window
		{100, 200, 804, 630, 102, 228, 800, 600}, // a framed 800x600 client
		{50, 60, 200, 300, 0, 0, 0, 0},           // a frame without a client
	}
	want := "G 3 0 0 720 1600 0 0 720 1600 100 200 804 630 102 228 800 600 50 60 200 300 0 0 0 0\n"
	if got := gLine(rs); got != want {
		t.Errorf("gLine = %q, want %q", got, want)
	}
}

func decodeX(t *testing.T, line string) (serial, w, h, xhot, yhot string, px []uint32) {
	t.Helper()
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) != 7 || f[0] != "X" {
		t.Fatalf("not a full X line: %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(f[6])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		px = append(px, binary.LittleEndian.Uint32(raw[i:]))
	}
	return f[1], f[2], f[3], f[4], f[5], px
}

func TestXLine(t *testing.T) {
	px := []uint32{0xff000000, 0xffffffff, 0x00000000, 0x80808080}
	serial, w, h, xh, yh, got := decodeX(t, xLine(7, false, 2, 2, 1, 0, px))
	if serial != "7" || w != "2" || h != "2" || xh != "1" || yh != "0" || len(got) != 4 {
		t.Fatalf("header %s %s %s %s %s, %d pixels", serial, w, h, xh, yh, len(got))
	}
	for i := range px {
		if got[i] != px[i] {
			t.Errorf("pixel %d", i)
		}
	}
	if xLine(2, false, 0, 0, 0, 0, nil) != "" || xLine(2, false, 2, 2, 0, 0, px[:3]) != "" {
		t.Error("empty or short images must give no line")
	}
}

func TestXLineSerialCache(t *testing.T) {
	// The second time a connection sees a serial, only the serial goes.
	if got := xLine(42, true, 2, 2, 0, 0, []uint32{1, 2, 3, 4}); got != "X 42\n" {
		t.Errorf("seen serial: %q", got)
	}
}

func TestXLineBigCursorScaled(t *testing.T) {
	// A 96x96 HiDPI cursor (hotspot 48,24) is scaled to 64x64, not dropped.
	px := make([]uint32, 96*96)
	for i := range px {
		px[i] = uint32(i % 96) // the column, so the scaling is checkable
	}
	_, w, h, xh, yh, got := decodeX(t, xLine(3, false, 96, 96, 48, 24, px))
	if w != "64" || h != "64" || xh != "32" || yh != "16" || len(got) != 64*64 {
		t.Fatalf("scaled to %sx%s hot %s,%s (%d pixels)", w, h, xh, yh, len(got))
	}
	if got[63] != 94 || got[0] != 0 { // nearest: column 63 of 64 -> 94 of 96
		t.Errorf("nearest scaling: %d %d", got[0], got[63])
	}
	// A wide one keeps its aspect.
	_, w, h, _, _, _ = decodeX(t, xLine(4, false, 128, 32, 0, 0, make([]uint32, 128*32)))
	if w != "64" || h != "16" {
		t.Errorf("128x32 -> %sx%s", w, h)
	}
}
