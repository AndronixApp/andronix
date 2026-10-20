package app

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jezek/xgb/xproto"
)

// The mapped-window count for the app's black-screen check (W lines):
//
//	helper -> app: W <n>   mapped, viewable top-level windows (after H on
//	                       connect, then whenever it changes, 500 ms debounced)
//	app -> helper: W       asks for it now
//
// n leaves out InputOnly windows (the helper's own) and override-redirect
// ones (menus, tooltips). A working XFCE has its desktop window and panels,
// so n >= 2 even with a black wallpaper; 0 twenty seconds in means nothing
// drew.

// watchWindows asks X for the root's structure events (map, unmap,
// create, destroy, reparent of its children).
func (h *fbHelper) watchWindows() error {
	h.winDirty = make(chan struct{}, 1)
	return xproto.ChangeWindowAttributesChecked(h.x, h.root, xproto.CwEventMask,
		[]uint32{xproto.EventMaskSubstructureNotify}).Check()
}

// windowEvent notes a structure change; false if ev isn't one.
func (h *fbHelper) windowEvent(ev any) bool {
	switch e := ev.(type) {
	case xproto.MapNotifyEvent:
		h.traceWindow("map", e.Window, e.OverrideRedirect)
	case xproto.UnmapNotifyEvent:
		h.traceWindow("unmap", e.Window, false)
	case xproto.CreateNotifyEvent, xproto.DestroyNotifyEvent, xproto.ReparentNotifyEvent:
	default:
		return false
	}
	if h.winDirty != nil {
		select {
		case h.winDirty <- struct{}{}:
		default:
		}
	}
	h.geomForget() // fbgeom.go: frames and clients may have changed
	h.geomPoke()
	return true
}

// traceWindow, when tracing, prints a window's map or unmap with its
// WM_CLASS (or the first of its children's that has one: a window
// manager's frame has none, its client is one of several children). The
// lookups are round trips, so they run off the event loop.
func (h *fbHelper) traceWindow(what string, w xproto.Window, override bool) {
	if !tracing() {
		return
	}
	t := time.Now().UnixMilli()
	go func() {
		class := h.wmClass(w)
		if class == "" {
			if tree, err := xproto.QueryTree(h.x, w).Reply(); err == nil {
				for _, c := range tree.Children {
					if class = h.wmClass(c); class != "" {
						break
					}
				}
			}
		}
		if class == "" {
			class = "?"
		}
		fmt.Printf("TRACE %d guest-window-%s win=0x%x class=%s override=%v\n", t, what, w, class, override)
	}()
}

// wmClass is a window's WM_CLASS class part ("Xfdesktop"), or "".
func (h *fbHelper) wmClass(w xproto.Window) string {
	r, err := xproto.GetProperty(h.x, false, w, xproto.AtomWmClass, xproto.AtomString, 0, 64).Reply()
	if err != nil || len(r.Value) == 0 {
		return ""
	}
	parts := strings.Split(strings.TrimRight(string(r.Value), "\x00"), "\x00")
	return parts[len(parts)-1]
}

// windowCounter sends W when the count changes, at most every 500 ms.
func (h *fbHelper) windowCounter() {
	for range h.winDirty {
		time.Sleep(500 * time.Millisecond)
		select { // what came in meanwhile is covered by this count
		case <-h.winDirty:
		default:
		}
		h.sendWindows(false)
	}
}

// mappedWindows counts the root's mapped, viewable, InputOutput children
// that aren't override-redirect.
func (h *fbHelper) mappedWindows() (int, error) {
	tree, err := xproto.QueryTree(h.x, h.root).Reply()
	if err != nil {
		return 0, err
	}
	cookies := make([]xproto.GetWindowAttributesCookie, len(tree.Children))
	for i, w := range tree.Children {
		cookies[i] = xproto.GetWindowAttributes(h.x, w)
	}
	n := 0
	for _, c := range cookies {
		a, err := c.Reply()
		if err != nil {
			continue // gone meanwhile
		}
		if a.MapState == xproto.MapStateViewable && a.Class == xproto.WindowClassInputOutput && !a.OverrideRedirect {
			n++
		}
	}
	return n, nil
}

// sendWindows writes W <n> to the app: always when asked, else only when
// n changed since the last one sent.
func (h *fbHelper) sendWindows(always bool) {
	n, err := h.mappedWindows()
	if err != nil {
		return
	}
	h.mu.Lock()
	c := h.client
	same := n == h.winSent
	h.winSent = n
	h.mu.Unlock()
	if c == nil || (same && !always) {
		return
	}
	h.writeLine(c, fmt.Sprintf("W %d\n", n))
}

// writeLine sends one line to the app between frames (the writer lock),
// with its own deadline.
func (h *fbHelper) writeLine(c net.Conn, line string) {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(line)); err != nil {
		c.Close()
	}
}
