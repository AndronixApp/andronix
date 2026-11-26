package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

// The cursor in the framebuffer, and the pointer's position (10.2.2,
// display-plan P1, helper-protocol.md):
//
//	helper -> app: V <proto> <version> <cap>...
//	                 once per connection, right after the first H: what this
//	                 helper does beyond the 2.1.5 lines. Old apps ignore it.
//	               Y <x> <y>
//	                 the pointer moved by something other than the app's M
//	                 (a program warped it, or the 250 ms poll saw it move).
//
// Xvfb draws its software cursor into the framebuffer but reports no
// damage for it. motion() covers the app's own moves; this file covers a
// cursor image change with the pointer still (arrow -> I-beam, busy) and
// pointer warps, by marking the area under the cursor damaged on
// CursorNotify and when the poll sees the pointer elsewhere. With
// ANDRONIX_CLIENT_CURSOR=1 (Xvfb -nocursor) there is nothing in the
// framebuffer to repaint and only Y is sent.

// protoLevel is the <proto> in the V line: 1 = the 10.2.2 additions.
const protoLevel = 1

// cursorBox is the last cursor image's size and hotspot, from XFixes: the
// exact area it covers around the pointer. ok false before the first one.
type cursorBox struct {
	w, h, xhot, yhot int
	ok               bool
}

// pollPointer is how often the helper asks X where the pointer is while
// an app is connected (a warp by a program has no event of its own).
const pollPointer = 250 * time.Millisecond

// vLine is the capability line for this helper: the protocol level, the
// binary's version and the capability tokens.
func vLine(version string, nocursor bool) string {
	caps := []string{"pointer", "pause", "cursor-damage"}
	if nocursor {
		caps = append(caps, "nocursor")
	}
	version = strings.Join(strings.Fields(version), "-")
	if version == "" {
		version = "-"
	}
	return fmt.Sprintf("V %d %s %s\n", protoLevel, version, strings.Join(caps, " "))
}

// cursorRect is the area the cursor cb covers with the pointer at p:
// the image placed by its hotspot, or 64x64 around p before any image is
// known (enough for the usual cursors).
func cursorRect(p [2]int, cb cursorBox) (x, y, w, h int) {
	if !cb.ok || cb.w <= 0 || cb.h <= 0 {
		return p[0] - 24, p[1] - 24, 64, 64
	}
	return p[0] - cb.xhot, p[1] - cb.yhot, cb.w, cb.h
}

// damageCursor marks the cursor's area at p damaged (nothing with -nocursor:
// the framebuffer has no cursor).
func (h *fbHelper) damageCursor(p [2]int) {
	if h.clientCursor {
		return
	}
	h.mu.Lock()
	cb := h.cursor
	h.mu.Unlock()
	if r, ok := h.clip(cursorRect(p, cb)); ok {
		h.addRect(r)
	}
}

// noteCursor records the cursor image's box (from sendCursor's reply) and,
// when the image changed, repaints the area under the pointer: the new
// image is there now and DAMAGE never said so.
func (h *fbHelper) noteCursor(img *xfixes.GetCursorImageReply, changed bool) {
	now := cursorBox{w: int(img.Width), h: int(img.Height), xhot: int(img.Xhot), yhot: int(img.Yhot), ok: true}
	h.mu.Lock()
	old := h.cursor
	h.cursor = now
	p := h.ptr
	h.mu.Unlock()
	if h.clientCursor || !changed {
		return
	}
	// The old image's area (it may have been bigger) and the new one's.
	for _, cb := range []cursorBox{old, now} {
		if r, ok := h.clip(cursorRect(p, cb)); ok {
			h.addRect(r)
		}
	}
}

// pointerPoller asks X for the pointer every pollPointer while an app is
// connected and not paused; a position the app didn't set is a warp.
// With no client or while paused the ticker is stopped (no wakeups on a
// phone with the screen off) until accept or P 1 signals pollWake.
func (h *fbHelper) pointerPoller() {
	t := time.NewTicker(pollPointer)
	defer t.Stop()
	for {
		h.mu.Lock()
		idle := h.client == nil || h.paused
		h.mu.Unlock()
		if idle {
			t.Stop()
			<-h.pollWake
			t.Reset(pollPointer)
			continue
		}
		<-t.C
		h.checkPointer()
	}
}

// signalPoll wakes pointerPoller from its idle wait (a new client, P 1).
func (h *fbHelper) signalPoll() {
	select {
	case h.pollWake <- struct{}{}:
	default:
	}
}

// checkPointer compares X's pointer position with the last one the app
// set (or this saw); when they differ the pointer was warped: both areas
// are repainted and the app gets a Y line. The query runs without h.mu,
// so an M dispatched during the round trip makes the reply stale: it is
// dropped (moveSeq changed), the next tick looks again.
func (h *fbHelper) checkPointer() {
	h.mu.Lock()
	seq := h.moveSeq
	h.mu.Unlock()
	now, ok := h.pointerNow()
	if !ok {
		return
	}
	h.mu.Lock()
	old := h.ptr
	moved := now != old && h.moveSeq == seq
	if moved {
		h.ptr = now
	}
	c := h.client
	h.mu.Unlock()
	if !moved || c == nil {
		return
	}
	h.damageCursor(old)
	h.damageCursor(now)
	h.writeLine(c, fmt.Sprintf("Y %d %d\n", now[0], now[1]))
}

// pointerNow is X's pointer position in root coordinates.
func (h *fbHelper) pointerNow() ([2]int, bool) {
	if h.queryPointer != nil {
		return h.queryPointer()
	}
	rep, err := xproto.QueryPointer(h.x, h.root).Reply()
	if err != nil {
		return [2]int{}, false
	}
	return [2]int{int(rep.RootX), int(rep.RootY)}, true
}
