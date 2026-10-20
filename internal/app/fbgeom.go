package app

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

// Window geometry and the cursor image, for the app's zero-copy GPU windows
// (the VirGL server's frames drawn by the app over Xvfb's framebuffer):
//
//	helper -> app: G <n> [<fx> <fy> <fw> <fh> <cx> <cy> <cw> <ch>]*n
//	                 the viewable top-level windows, bottom to top: the
//	                 frame (what covers the screen) and the client inside it
//	                 (what the program draws: the frame's inside when no
//	                 window manager put WM_STATE on anything in it).
//	                 Unlike W (fbwindows.go), G includes override-redirect
//	                 windows (menus, tooltips: each its own client), since
//	                 they cover others; the app's black-screen check keeps
//	                 using W. After H on connect, then whenever windows map,
//	                 unmap, move, resize or restack, or a client moves inside
//	                 its frame (30 ms debounce).
//	               X <serial> <w> <h> <xhot> <yhot> <base64>
//	                 the X cursor (premultiplied ARGB, 32 bits per pixel,
//	                 little-endian), the first time this connection sees the
//	                 serial; bigger than 64x64 is scaled down to fit (the
//	                 hotspot with it).
//	               X <serial>
//	                 that cursor again (the app keeps the images by serial).
//	                 Xvfb draws the cursor into the framebuffer, which a
//	                 zero-copy window covers, so the app draws it there.
//	app -> helper: G   asks for the geometry now
//
// Older apps ignore both lines.

// geomRect is one window: its frame and its client, in root coordinates.
type geomRect struct{ fx, fy, fw, fh, cx, cy, cw, ch int }

type geomState struct {
	dirty       chan struct{}
	cursorDirty chan struct{}
	wmState     xproto.Atom
	mu          sync.Mutex
	lastG       string
	clients     map[xproto.Window]xproto.Window // frame -> client (0: none), until a map/unmap/reparent
	watched     map[xproto.Window]bool          // clients with StructureNotify selected
	sentSerials map[uint32]bool                 // cursors this connection has the image of
	lastSerial  uint32
	cursorSent  bool // an X line went to this connection
}

// geomInit starts the senders and asks XFixes for cursor changes. Window
// events come from watchWindows' SubstructureNotify on the root, plus
// StructureNotify on each client found (a client moving inside its frame).
func (h *fbHelper) geomInit() error {
	g := &geomState{dirty: make(chan struct{}, 1), cursorDirty: make(chan struct{}, 1),
		clients: map[xproto.Window]xproto.Window{}, watched: map[xproto.Window]bool{}, sentSerials: map[uint32]bool{}}
	if a, err := xproto.InternAtom(h.x, false, uint16(len("WM_STATE")), "WM_STATE").Reply(); err == nil {
		g.wmState = a.Atom
	}
	h.geom = g
	go h.geomSender()
	go h.cursorSender()
	return xfixes.SelectCursorInputChecked(h.x, h.root, xfixes.CursorNotifyMaskDisplayCursor).Check()
}

// geomEvent handles a move/resize/restack (ConfigureNotify) or a cursor
// change; false if ev is neither.
func (h *fbHelper) geomEvent(ev any) bool {
	if h.geom == nil {
		return false
	}
	switch ev.(type) {
	case xproto.ConfigureNotifyEvent:
		h.geomPoke()
	case xfixes.CursorNotifyEvent:
		select {
		case h.geom.cursorDirty <- struct{}{}:
		default:
		}
	default:
		return false
	}
	return true
}

// geomPoke notes that the windows may have changed. Called for maps,
// unmaps, reparents and destroys too (windowEvent): those also forget the
// frame -> client cache.
func (h *fbHelper) geomPoke() {
	if h.geom == nil {
		return
	}
	select {
	case h.geom.dirty <- struct{}{}:
	default:
	}
}

// geomForget drops the frame -> client cache (a window mapped, unmapped,
// reparented or was destroyed).
func (h *fbHelper) geomForget() {
	if h.geom == nil {
		return
	}
	h.geom.mu.Lock()
	h.geom.clients = map[xproto.Window]xproto.Window{}
	h.geom.mu.Unlock()
}

// geomSender sends G when the geometry changed, at most every 30 ms (a
// dragged window moves smoothly; the app keeps drawing meanwhile).
func (h *fbHelper) geomSender() {
	for range h.geom.dirty {
		time.Sleep(30 * time.Millisecond)
		select {
		case <-h.geom.dirty:
		default:
		}
		h.sendGeometry(false)
	}
}

// cursorSender sends X for the newest cursor: one sender, so an older image
// never arrives after a newer one.
func (h *fbHelper) cursorSender() {
	for range h.geom.cursorDirty {
		h.sendCursor(false)
	}
}

// windowsGeometry lists the viewable InputOutput children of the root,
// bottom to top (QueryTree's order), with their clients. Round trips are
// pipelined (all requests, then the replies): every one goes through
// proot's ptrace.
func (h *fbHelper) windowsGeometry() ([]geomRect, error) {
	tree, err := xproto.QueryTree(h.x, h.root).Reply()
	if err != nil {
		return nil, err
	}
	n := len(tree.Children)
	attrC := make([]xproto.GetWindowAttributesCookie, n)
	geoC := make([]xproto.GetGeometryCookie, n)
	for i, w := range tree.Children {
		attrC[i] = xproto.GetWindowAttributes(h.x, w)
		geoC[i] = xproto.GetGeometry(h.x, xproto.Drawable(w))
	}
	type win struct {
		frame, client xproto.Window
		r             geomRect
		override      bool
		inner         [2]int // the frame's own size without borders (an override window's client)
	}
	var wins []win
	for i, w := range tree.Children {
		a, err1 := attrC[i].Reply()
		fg, err2 := geoC[i].Reply()
		if err1 != nil || err2 != nil || a.MapState != xproto.MapStateViewable || a.Class != xproto.WindowClassInputOutput {
			continue
		}
		bw := 2 * int(fg.BorderWidth)
		wins = append(wins, win{frame: w, override: a.OverrideRedirect, inner: [2]int{int(fg.Width), int(fg.Height)},
			r: geomRect{fx: int(fg.X), fy: int(fg.Y), fw: int(fg.Width) + bw, fh: int(fg.Height) + bw}})
	}
	// Clients: cached per frame; new frames looked up (WM_STATE walk) and watched.
	h.geom.mu.Lock()
	for i := range wins {
		if c, ok := h.geom.clients[wins[i].frame]; ok {
			wins[i].client = c // known (0: none)
			continue
		}
		wins[i].client = 0xffffffff // to look up
	}
	h.geom.mu.Unlock()
	for i := range wins {
		if wins[i].client != 0xffffffff {
			continue
		}
		c := h.clientOf(wins[i].frame, 2)
		wins[i].client = c
		h.geom.mu.Lock()
		h.geom.clients[wins[i].frame] = c
		watch := c != 0 && !h.geom.watched[c]
		if watch {
			h.geom.watched[c] = true
		}
		h.geom.mu.Unlock()
		if watch { // a client moving inside its frame (decorations on or off) has no root event
			xproto.ChangeWindowAttributes(h.x, c, xproto.CwEventMask, []uint32{xproto.EventMaskStructureNotify})
		}
	}
	cgC := make([]xproto.GetGeometryCookie, len(wins))
	tcC := make([]xproto.TranslateCoordinatesCookie, len(wins))
	for i, w := range wins {
		if w.client != 0 {
			cgC[i] = xproto.GetGeometry(h.x, xproto.Drawable(w.client))
			tcC[i] = xproto.TranslateCoordinates(h.x, w.client, h.root, 0, 0)
		}
	}
	out := make([]geomRect, 0, len(wins))
	for i, w := range wins {
		r := w.r
		if w.client != 0 {
			cg, err1 := cgC[i].Reply()
			tc, err2 := tcC[i].Reply()
			if err1 == nil && err2 == nil {
				r.cx, r.cy, r.cw, r.ch = int(tc.DstX), int(tc.DstY), int(cg.Width), int(cg.Height)
			}
		} else {
			// A menu (override-redirect), or any window when no window manager runs (no WM_STATE,
			// e.g. safe mode's no_wm): the window draws itself.
			r.cx, r.cy, r.cw, r.ch = r.fx, r.fy, w.inner[0], w.inner[1]
		}
		out = append(out, r)
	}
	return out, nil
}

// clientOf is w itself or a descendant (up to depth levels down) that has
// WM_STATE: the program's window inside a window manager's frame.
func (h *fbHelper) clientOf(w xproto.Window, depth int) xproto.Window {
	if h.geom.wmState == 0 {
		return 0
	}
	if p, err := xproto.GetProperty(h.x, false, w, h.geom.wmState, xproto.GetPropertyTypeAny, 0, 0).Reply(); err == nil && p.Format != 0 {
		return w
	}
	if depth == 0 {
		return 0
	}
	tree, err := xproto.QueryTree(h.x, w).Reply()
	if err != nil {
		return 0
	}
	for i := len(tree.Children) - 1; i >= 0; i-- {
		if c := h.clientOf(tree.Children[i], depth-1); c != 0 {
			return c
		}
	}
	return 0
}

// gLine is the G line for rs.
func gLine(rs []geomRect) string {
	var b strings.Builder
	fmt.Fprintf(&b, "G %d", len(rs))
	for _, r := range rs {
		fmt.Fprintf(&b, " %d %d %d %d %d %d %d %d", r.fx, r.fy, r.fw, r.fh, r.cx, r.cy, r.cw, r.ch)
	}
	b.WriteByte('\n')
	return b.String()
}

// sendGeometry writes G to the app: always when asked, else only when it changed.
func (h *fbHelper) sendGeometry(always bool) {
	if h.geom == nil {
		return
	}
	rs, err := h.windowsGeometry()
	if err != nil {
		return
	}
	line := gLine(rs)
	h.mu.Lock()
	c := h.client
	h.mu.Unlock()
	h.geom.mu.Lock()
	same := line == h.geom.lastG
	h.geom.lastG = line
	h.geom.mu.Unlock()
	if c == nil || (same && !always) {
		return
	}
	h.writeLine(c, line)
}

// geomNewClient forgets what the previous app connection was sent (a new
// connection gets every cursor image and the geometry again).
func (h *fbHelper) geomNewClient() {
	if h.geom == nil {
		return
	}
	h.geom.mu.Lock()
	h.geom.sentSerials = map[uint32]bool{}
	h.geom.cursorSent = false
	h.geom.lastG = ""
	h.geom.mu.Unlock()
}

// cursorMax is the largest cursor sent; bigger ones (HiDPI themes) are
// scaled down to fit, never dropped (the app would keep the old cursor).
const cursorMax = 64

// fitCursor scales a w x h image (and its hotspot) down to fit cursorMax,
// nearest neighbour, keeping the aspect ratio; smaller ones are unchanged.
func fitCursor(w, hh, xhot, yhot int, px []uint32) (int, int, int, int, []uint32) {
	if w <= cursorMax && hh <= cursorMax {
		return w, hh, xhot, yhot, px
	}
	s := float64(cursorMax) / float64(max(w, hh))
	nw, nh := max(1, int(float64(w)*s)), max(1, int(float64(hh)*s))
	out := make([]uint32, nw*nh)
	for y := 0; y < nh; y++ {
		sy := min(hh-1, int(float64(y)/s))
		for x := 0; x < nw; x++ {
			out[y*nw+x] = px[sy*w+min(w-1, int(float64(x)/s))]
		}
	}
	return nw, nh, min(nw-1, int(float64(xhot)*s)), min(nh-1, int(float64(yhot)*s)), out
}

// xLine is the X line for a cursor: the full form the first time this
// connection gets the serial (seen[serial] false), else the short one.
// "" if the image is empty or short.
func xLine(serial uint32, seen bool, w, hh, xhot, yhot int, pixels []uint32) string {
	if seen {
		return fmt.Sprintf("X %d\n", serial)
	}
	if w <= 0 || hh <= 0 || len(pixels) < w*hh {
		return ""
	}
	w, hh, xhot, yhot, pixels = fitCursor(w, hh, xhot, yhot, pixels)
	raw := make([]byte, 4*w*hh)
	for i := 0; i < w*hh; i++ {
		binary.LittleEndian.PutUint32(raw[4*i:], pixels[i])
	}
	return fmt.Sprintf("X %d %d %d %d %d %s\n", serial, w, hh, xhot, yhot, base64.StdEncoding.EncodeToString(raw))
}

// sendCursor writes X when the cursor changed (or always, on connect).
func (h *fbHelper) sendCursor(always bool) {
	if h.geom == nil {
		return
	}
	img, err := xfixes.GetCursorImage(h.x).Reply()
	if err != nil {
		return
	}
	h.mu.Lock()
	c := h.client
	h.mu.Unlock()
	if c == nil {
		return
	}
	g := h.geom
	g.mu.Lock()
	same := g.cursorSent && img.CursorSerial == g.lastSerial
	seen := g.sentSerials[img.CursorSerial]
	g.mu.Unlock()
	if same && !always {
		return
	}
	line := xLine(img.CursorSerial, seen, int(img.Width), int(img.Height), int(img.Xhot), int(img.Yhot), img.CursorImage)
	if line == "" {
		return
	}
	h.writeLine(c, line)
	g.mu.Lock()
	g.sentSerials[img.CursorSerial] = true
	g.lastSerial = img.CursorSerial
	g.cursorSent = true
	g.mu.Unlock()
}
