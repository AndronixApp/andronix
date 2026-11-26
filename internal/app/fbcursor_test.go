package app

import (
	"bufio"
	"net"
	"sync"
	"testing"
	"time"
)

func TestVLine(t *testing.T) {
	if got := vLine("2.1.6", false); got != "V 1 2.1.6 pointer pause cursor-damage\n" {
		t.Errorf("got %q", got)
	}
	if got := vLine("2.1.6-rc1", true); got != "V 1 2.1.6-rc1 pointer pause cursor-damage nocursor\n" {
		t.Errorf("nocursor: %q", got)
	}
	// A version with spaces or none at all can't break the line's fields.
	if got := vLine("", false); got != "V 1 - pointer pause cursor-damage\n" {
		t.Errorf("empty version: %q", got)
	}
	if got := vLine("odd version", false); got != "V 1 odd-version pointer pause cursor-damage\n" {
		t.Errorf("spaced version: %q", got)
	}
}

func TestCursorRect(t *testing.T) {
	// No image known yet: 64x64 around the pointer.
	if x, y, w, h := cursorRect([2]int{100, 200}, cursorBox{}); x != 76 || y != 176 || w != 64 || h != 64 {
		t.Errorf("default box: %d %d %d %d", x, y, w, h)
	}
	// A 32x32 image with its hotspot at (4, 2): placed so the hotspot is on the pointer.
	cb := cursorBox{w: 32, h: 32, xhot: 4, yhot: 2, ok: true}
	if x, y, w, h := cursorRect([2]int{100, 200}, cb); x != 96 || y != 198 || w != 32 || h != 32 {
		t.Errorf("image box: %d %d %d %d", x, y, w, h)
	}
	// A degenerate image falls back to the default box.
	if _, _, w, h := cursorRect([2]int{0, 0}, cursorBox{ok: true}); w != 64 || h != 64 {
		t.Errorf("empty image: %dx%d", w, h)
	}
}

// clip (fbhelper.go) keeps cursor rectangles on the screen: a pointer in
// the corner gives the part of the box that's inside, one past the edge nothing.
func TestClipCursorRect(t *testing.T) {
	h := &fbHelper{w: 720, h: 1600}
	r, ok := h.clip(cursorRect([2]int{0, 0}, cursorBox{}))
	if !ok || r.X != 0 || r.Y != 0 || r.Width != 40 || r.Height != 40 {
		t.Errorf("corner: %+v %v", r, ok)
	}
	r, ok = h.clip(cursorRect([2]int{719, 1599}, cursorBox{w: 24, h: 24, ok: true}))
	if !ok || r.X != 719 || r.Y != 1599 || r.Width != 1 || r.Height != 1 {
		t.Errorf("far corner: %+v %v", r, ok)
	}
	if _, ok := h.clip(cursorRect([2]int{800, 10}, cursorBox{w: 24, h: 24, ok: true})); ok {
		t.Error("off-screen rectangle kept")
	}
}

// pollHelper is a helper with a fake pointer query: cli reads what it
// writes to the app. Client-cursor mode: no damage rectangles to drain.
func pollHelper(t *testing.T, query func() ([2]int, bool)) (*fbHelper, net.Conn) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() { srv.Close(); cli.Close() })
	h := &fbHelper{w: 720, h: 1600, wake: make(chan struct{}, 1), client: srv, clientCursor: true, queryPointer: query}
	return h, cli
}

// readLine is the next line the app gets within d, or "" if none.
func readLine(c net.Conn, d time.Duration) string {
	c.SetReadDeadline(time.Now().Add(d))
	l, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return ""
	}
	return l
}

func (h *fbHelper) ptrNow() [2]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ptr
}

// A pointer X reports somewhere else than the last M: a Y line.
func TestCheckPointerWarp(t *testing.T) {
	h, cli := pollHelper(t, func() ([2]int, bool) { return [2]int{300, 400}, true })
	h.ptr = [2]int{10, 20}
	go h.checkPointer()
	if l := readLine(cli, 2*time.Second); l != "Y 300 400\n" {
		t.Fatalf("got %q", l)
	}
	if p := h.ptrNow(); p != [2]int{300, 400} {
		t.Errorf("ptr %v", p)
	}
}

// An M before the poll: X and the helper agree, no Y.
func TestCheckPointerAfterMotion(t *testing.T) {
	h, cli := pollHelper(t, func() ([2]int, bool) { return [2]int{10, 20}, true })
	h.mu.Lock()
	h.setPtrLocked(10, 20)
	h.mu.Unlock()
	go h.checkPointer()
	if l := readLine(cli, 200*time.Millisecond); l != "" {
		t.Fatalf("got %q", l)
	}
}

// M1 (review-helper.md): an M that lands while the QueryPointer round trip
// is in flight makes the reply stale. The reply is dropped: no Y with the
// old position, and the pointer stays where the app put it.
func TestCheckPointerMotionDuringQuery(t *testing.T) {
	var h *fbHelper
	h, cli := pollHelper(t, func() ([2]int, bool) {
		h.mu.Lock() // the app's M x1 y1, dispatched between request and reply
		h.setPtrLocked(500, 600)
		h.mu.Unlock()
		return [2]int{10, 20}, true // X's answer from before the M
	})
	h.ptr = [2]int{10, 20}
	go h.checkPointer()
	if l := readLine(cli, 200*time.Millisecond); l != "" {
		t.Fatalf("stale reply sent: %q", l)
	}
	if p := h.ptrNow(); p != [2]int{500, 600} {
		t.Errorf("ptr %v, want the M's", p)
	}
}

// m2 (review-helper.md): X clamps the pointer to the screen, so the
// helper's copy must be clamped too, or an off-screen M (the app's frame
// lags the helper's size right after an R) makes every poll see a "move":
// Y and cursor damage every 250 ms until the next M.
func TestSetPtrClamped(t *testing.T) {
	h := &fbHelper{w: 720, h: 1600}
	h.mu.Lock()
	h.setPtrLocked(800, -5)
	h.mu.Unlock()
	if h.ptr != [2]int{719, 0} {
		t.Errorf("ptr %v, want 719 0", h.ptr)
	}
	h.mu.Lock()
	h.setPtrLocked(-1, 1600)
	h.mu.Unlock()
	if h.ptr != [2]int{0, 1599} {
		t.Errorf("ptr %v, want 0 1599", h.ptr)
	}
	// A poll with X at the clamped position agrees with the helper: no Y.
	h2, cli := pollHelper(t, func() ([2]int, bool) { return [2]int{719, 0}, true })
	h2.mu.Lock()
	h2.setPtrLocked(800, -5)
	h2.mu.Unlock()
	go h2.checkPointer()
	if l := readLine(cli, 200*time.Millisecond); l != "" {
		t.Fatalf("got %q", l)
	}
}

// After a resize the remembered position is pulled onto the new screen.
func TestClampPtrAfterResize(t *testing.T) {
	h := &fbHelper{w: 1600, h: 720, ptr: [2]int{1500, 700}}
	h.mu.Lock()
	h.w, h.h = 720, 1600
	h.clampPtrLocked()
	h.mu.Unlock()
	if h.ptr != [2]int{719, 700} {
		t.Errorf("ptr %v, want 719 700", h.ptr)
	}
}

// N3 (review-helper.md): the poll ticker sleeps while no app is connected
// or the app paused (battery); a connect or P 1 starts it again.
func TestPointerPollerIdles(t *testing.T) {
	var mu sync.Mutex
	n := 0
	h, _ := pollHelper(t, func() ([2]int, bool) {
		mu.Lock()
		n++
		mu.Unlock()
		return [2]int{1, 1}, true
	})
	h.pollWake = make(chan struct{}, 1)
	h.ptr = [2]int{1, 1} // where the fake X says it is: polls count, no Y is written
	c := h.client
	h.client = nil
	go h.pointerPoller()
	time.Sleep(3 * pollPointer)
	mu.Lock()
	idle := n
	mu.Unlock()
	if idle != 0 {
		t.Fatalf("%d polls with no client", idle)
	}
	h.mu.Lock()
	h.client = c
	h.mu.Unlock()
	h.signalPoll()
	deadline := time.Now().Add(4 * pollPointer)
	for {
		mu.Lock()
		got := n
		mu.Unlock()
		if got > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no poll after the client connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Paused: the ticker stops again; P 1 brings it back.
	h.pause(c, true)
	time.Sleep(2 * pollPointer) // let a tick in flight finish
	mu.Lock()
	before := n
	mu.Unlock()
	time.Sleep(3 * pollPointer)
	mu.Lock()
	during := n
	mu.Unlock()
	if during != before {
		t.Fatalf("%d polls while paused", during-before)
	}
	h.pause(c, false)
	deadline = time.Now().Add(4 * pollPointer)
	for {
		mu.Lock()
		got := n
		mu.Unlock()
		if got > during {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no poll after P 1")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
