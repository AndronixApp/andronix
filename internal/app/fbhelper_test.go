package app

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// Xvfb's -fbdir file: an XWD header (big-endian), a window name, colours.
func TestXWDHLine(t *testing.T) {
	var hd [25]uint32
	hd[0] = 100 + 8 // header + "Xvfb..\0" name
	hd[4], hd[5] = 800, 600
	hd[7] = 0 // LSBFirst
	hd[11], hd[12] = 32, 3200
	hd[14], hd[15], hd[16] = 0xff0000, 0x00ff00, 0x0000ff
	hd[19] = 256 // colormap entries Xvfb writes even for TrueColor
	p := filepath.Join(t.TempDir(), "Xvfb_screen0")
	f, _ := os.Create(p)
	binary.Write(f, binary.BigEndian, hd)
	f.Close()
	got, err := xwdHLine(p, 0, 0)
	if err != nil || got != "H 800 600 3200 3180 32 16 8 0\n" {
		t.Errorf("got %q %v", got, err)
	}
	// After a RandR resize: the size now, the same stride and offset.
	if got, _ := xwdHLine(p, 600, 800); got != "H 800 600 3200 3180 32 16 8 0\n" {
		t.Errorf("larger than the file accepted: %q", got)
	}
	if got, _ := xwdHLine(p, 400, 300); got != "H 400 300 3200 3180 32 16 8 0\n" {
		t.Errorf("resized: %q", got)
	}
	hd[11] = 16
	f, _ = os.Create(p)
	binary.Write(f, binary.BigEndian, hd)
	f.Close()
	if _, err := xwdHLine(p, 0, 0); err == nil {
		t.Error("16 bpp accepted")
	}
}

func TestRuneKeysym(t *testing.T) {
	for r, want := range map[rune]uint32{'a': 0x61, 'A': 0x41, ' ': 0x20, 'é': 0xe9, '\n': 0xff0d, '\t': 0xff09, '€': 0x010020ac, 'अ': 0x01000905} {
		if got := runeKeysym(r); got != want {
			t.Errorf("%q: %#x, want %#x", r, got, want)
		}
	}
}

func TestBounding(t *testing.T) {
	b := bounding([]xproto.Rectangle{{X: 10, Y: 20, Width: 5, Height: 5}, {X: 0, Y: 30, Width: 100, Height: 2}})
	if b != (xproto.Rectangle{X: 0, Y: 20, Width: 100, Height: 12}) {
		t.Errorf("got %+v", b)
	}
}

func TestKeymapFind(t *testing.T) {
	// keycodes 8..10, 2 per keycode: 8 = a/A, 9 = empty (spare), 10 = Return.
	k := &keymap{min: 8, per: 2, syms: []xproto.Keysym{0x61, 0x41, 0, 0, 0xff0d, 0}, mapped: map[uint32]xproto.Keycode{}, spare: []xproto.Keycode{9}}
	for sym, want := range map[uint32][2]int{0x61: {8, 0}, 0x41: {8, 1}, 0xff0d: {10, 0}} {
		kc, shift, ok := k.find(sym)
		if !ok || int(kc) != want[0] || shift != (want[1] == 1) {
			t.Errorf("%#x: %d %v %v", sym, kc, shift, ok)
		}
	}
	if _, _, ok := k.find(0x010020ac); ok {
		t.Error("euro found without a remap")
	}
}
