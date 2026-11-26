package app

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
)

func rect(x, y, w, h int) xproto.Rectangle {
	return xproto.Rectangle{X: int16(x), Y: int16(y), Width: uint16(w), Height: uint16(h)}
}

// covers reports whether some rectangle in rs contains r whole.
func covers(rs []xproto.Rectangle, r xproto.Rectangle) bool {
	for _, k := range rs {
		if k.X <= r.X && k.Y <= r.Y && int(k.X)+int(k.Width) >= int(r.X)+int(r.Width) && int(k.Y)+int(k.Height) >= int(r.Y)+int(r.Height) {
			return true
		}
	}
	return false
}

func TestRectUnionGrowth(t *testing.T) {
	a, b := rect(0, 0, 10, 10), rect(20, 0, 10, 10)
	if u := rectUnion(a, b); u != rect(0, 0, 30, 10) {
		t.Errorf("union %+v", u)
	}
	if g := mergeGrowth(a, b); g != 100 { // the 10x10 gap
		t.Errorf("growth apart %d", g)
	}
	if g := mergeGrowth(a, rect(5, 5, 10, 10)); g != 50 { // overlapping: 15x15 union, 175 covered
		t.Errorf("growth overlapping %d", g)
	}
	if g := mergeGrowth(a, rect(2, 2, 4, 4)); g != 0 { // contained
		t.Errorf("growth contained %d", g)
	}
	if g := mergeGrowth(a, rect(10, 0, 10, 10)); g != 0 { // touching
		t.Errorf("growth touching %d", g)
	}
}

// The P3 case: a panel clock ticking far from a window drag must stay a
// rectangle of its own, not box in the whole screen with the drag.
func TestCoalesceClockAndDrag(t *testing.T) {
	var rs []xproto.Rectangle
	for i := 0; i < 300; i++ { // a drag's small repaints in the top-left quarter
		rs = append(rs, rect(10+i, 20+i/2, 40, 30))
	}
	clock := rect(600, 1560, 100, 30)
	rs = append(rs, clock)
	out := coalesce(rs, rectKeep)
	if len(out) > rectKeep || len(out) < 2 {
		t.Fatalf("%d rectangles", len(out))
	}
	for _, r := range rs {
		if !covers(out, r) {
			t.Fatalf("%+v not covered by %+v", r, out)
		}
	}
	// The clock is its own rectangle: nothing covering it reaches the drag.
	for _, k := range out {
		if covers([]xproto.Rectangle{k}, clock) && rectArea(k) > 2*rectArea(clock) {
			t.Errorf("the clock got boxed with the drag: %+v", k)
		}
	}
	total := 0
	for _, k := range out {
		total += rectArea(k)
	}
	if box := rectArea(bounding(rs)); total*2 > box {
		t.Errorf("coalesced area %d is more than half the bounding box %d", total, box)
	}
}

func TestCoalesceSmallInputsUntouched(t *testing.T) {
	rs := []xproto.Rectangle{rect(0, 0, 1, 1), rect(5, 5, 1, 1)}
	if out := coalesce(rs, 16); len(out) != 2 {
		t.Errorf("%d", len(out))
	}
	if out := coalesce(rs, 1); len(out) != 1 || out[0] != rect(0, 0, 6, 6) {
		t.Errorf("to one: %+v", out)
	}
	if out := coalesce(rs, 0); len(out) != 1 {
		t.Errorf("n=0 treated as 1: %+v", out)
	}
	if out := coalesce(nil, 4); len(out) != 0 {
		t.Errorf("nil: %+v", out)
	}
}

// Scattered rectangles collapse to the cap, each kept one covering what
// went into it, with less total area than one bounding box.
func TestCoalesceScattered(t *testing.T) {
	var rs []xproto.Rectangle
	for i := 0; i < 257; i++ {
		rs = append(rs, rect((i*97)%700, (i*131)%1500, 8, 8))
	}
	out := coalesce(rs, 16)
	if len(out) != 16 {
		t.Fatalf("%d", len(out))
	}
	for _, r := range rs {
		if !covers(out, r) {
			t.Fatalf("%+v lost", r)
		}
	}
	total := 0
	for _, k := range out {
		total += rectArea(k)
	}
	if total >= rectArea(bounding(rs)) {
		t.Errorf("no better than a bounding box: %d", total)
	}
}

// addRect keeps the queue at most rectCap long and never loses coverage.
func TestAddRectCap(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	h := &fbHelper{w: 720, h: 1600, wake: make(chan struct{}, 1), client: srv}
	for i := 0; i < 1000; i++ {
		h.addRect(rect(i%700, (i*7)%1590, 3, 3))
		if len(h.rects) > rectCap {
			t.Fatalf("queue %d after %d rectangles", len(h.rects), i)
		}
	}
	if len(h.rects) > rectCap || len(h.rects) < rectKeep {
		t.Errorf("queue %d", len(h.rects))
	}
	if !covers(h.rects, rect(999%700, (999*7)%1590, 3, 3)) {
		t.Error("the last rectangle is gone")
	}
}

// P 0 holds the D/F lines (damage piles up); P 1 flushes them as one batch.
func TestPauseHoldsFlush(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	h := &fbHelper{w: 720, h: 1600, wake: make(chan struct{}, 1), client: srv}
	go h.flusher()
	rd := bufio.NewReader(cli)
	h.pause(srv, true)
	h.addRect(rect(1, 2, 3, 4))
	h.addRect(rect(10, 20, 30, 40))
	cli.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if l, err := rd.ReadString('\n'); err == nil {
		t.Fatalf("paused, but got %q", l)
	}
	h.pause(srv, false)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	var got []string
	for {
		l, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("after P 1: %v (got %q)", err, got)
		}
		got = append(got, strings.TrimSpace(l))
		if l == "F\n" {
			break
		}
	}
	if strings.Join(got, "|") != "D 1 2 3 4|D 10 20 30 40|F" {
		t.Errorf("batch %q", got)
	}
}

// m1 (review-helper.md): a replaced connection's late P 0 (the app sends
// it while tearing down) must not pause the client that replaced it.
func TestPauseFromReplacedConnection(t *testing.T) {
	oldc, _ := net.Pipe()
	newc, _ := net.Pipe()
	defer oldc.Close()
	defer newc.Close()
	h := &fbHelper{w: 720, h: 1600, wake: make(chan struct{}, 1), client: newc}
	h.pause(oldc, true)
	if h.paused {
		t.Fatal("P 0 from the replaced connection paused the new client")
	}
	h.pause(newc, true)
	if !h.paused {
		t.Fatal("P 0 from the current client ignored")
	}
	h.pause(oldc, false) // nor may the old one resume it
	if !h.paused {
		t.Fatal("P 1 from the replaced connection resumed the new client")
	}
	h.pause(newc, false)
	if h.paused {
		t.Fatal("P 1 from the current client ignored")
	}
}

func TestXvfbRejected(t *testing.T) {
	log := "Unrecognized option: -nocursor\nuse: X [:<display>] [option]\n-a #                   default pointer acceleration (factor)\n"
	if !xvfbRejected(log, "-nocursor") {
		t.Error("not seen")
	}
	if xvfbRejected(log, "-noreset") {
		t.Error("the wrong flag matched")
	}
	if xvfbRejected("(EE) Fatal server error: Cannot open log\n-nocursor mentioned elsewhere\n", "-nocursor") {
		t.Error("a plain mention counts")
	}
}

// The cap is hit a few times per frame during a drag: coalescing must be
// far cheaper than a frame.
func BenchmarkCoalesce(b *testing.B) {
	var rs []xproto.Rectangle
	for i := 0; i < rectCap+1; i++ {
		rs = append(rs, rect((i*97)%700, (i*131)%1500, 8, 8))
	}
	for i := 0; i < b.N; i++ {
		coalesce(rs, rectKeep)
	}
}
