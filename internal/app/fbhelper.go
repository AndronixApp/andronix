package app

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math/bits"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/damage"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// The standalone app's built-in display (option A): Xvfb keeps the screen
// in a file the app maps (-fbdir), and this helper, an X client inside the
// distro, tells the app which rectangles changed (DAMAGE) and plays its
// input into the session (XTEST). The app connects to the ctl socket (in
// its private storage). Newline-terminated ASCII lines:
//
//	helper -> app: H <w> <h> <stride> <offset> <bpp> <rshift> <gshift> <bshift>
//	                 (on connect, and again after the file is replaced)
//	               D <x> <y> <w> <h>   a damaged rectangle
//	               F                   end of a batch: upload now
//	app -> helper: M <x> <y>           pointer move
//	               B <n> <1|0>         button down/up (1 left, 2 middle,
//	                                   3 right, 4/5 wheel up/down, 6/7
//	                                   wheel left/right, 8/9 back/forward)
//	               K <keysym hex> <1|0> key down/up
//	               T <utf8 text>       type text (an IME commit)
//	               R <w> <h>           resize the screen (rotation), up to
//	                                   the --max-size Xvfb started with;
//	                                   always answered with an H line (the
//	                                   current size if it was refused). The
//	                                   stride and offset stay the same.
//	               W                   the mapped-window count now (fbwindows.go;
//	                                   the helper sends W <n> on its own too)
//	               C <base64>          the phone's clipboard (see fbclip.go;
//	                                   the helper sends C too)
//	               G                   the window geometry now (fbgeom.go;
//	                                   the helper sends G and X on its own too)
//	               Q                   close
//
// When the helper dies its socket closes, and the app falls back to VNC.

// fbHelper is the helper's state: the X connection and the app client.
type fbHelper struct {
	x        *xgb.Conn
	root     xproto.Window
	w, h     uint16 // the screen's size now (RandR)
	maxW     uint16 // Xvfb's start size, the largest RandR allows
	maxH     uint16
	sc       screenScale
	mu       sync.Mutex
	wmu      sync.Mutex // one writer at a time to the app (H, then D lines)
	client   net.Conn
	fb       string // the -fbdir file, for the H line
	rects    []xproto.Rectangle
	wake     chan struct{}
	keys     *keymap
	buttons  uint16
	ptr      [2]int // the pointer's last position
	shiftKC  xproto.Keycode
	shifted  map[uint32]bool // keysyms pressed with Shift held for them
	keysDown map[uint32]xproto.Keycode
	cb       *clipState    // nil: no clipboard sharing
	winDirty chan struct{} // the window count may have changed (fbwindows.go)
	winSent  int           // the last W sent; -1 before the first
	started  time.Time     // the helper's start (early resizes reload the desktop twice)
	geom     *geomState    // nil: no G/X lines (fbgeom.go)
}

// FBHelper runs the helper on display (":1") with the app's socket at
// sock, until the X server goes away. ready is touched once it listens.
func FBHelper(display, sock, fb, ready string) error {
	x, err := xgb.NewConnDisplay(display)
	if err != nil {
		return fmt.Errorf("connect %s: %w", display, err)
	}
	defer x.Close()
	for _, init := range []func(*xgb.Conn) error{damage.Init, xfixes.Init, xtest.Init, randr.Init} {
		if err := init(x); err != nil {
			return err
		}
	}
	if _, err := xfixes.QueryVersion(x, 5, 0).Reply(); err != nil {
		return fmt.Errorf("xfixes: %w", err)
	}
	if _, err := damage.QueryVersion(x, 1, 1).Reply(); err != nil {
		return fmt.Errorf("damage: %w", err)
	}
	scr := xproto.Setup(x).DefaultScreen(x)
	h := &fbHelper{x: x, root: scr.Root, w: scr.WidthInPixels, h: scr.HeightInPixels, wake: make(chan struct{}, 1), fb: fb,
		shifted: map[uint32]bool{}, keysDown: map[uint32]xproto.Keycode{}}
	if h.keys, err = loadKeymap(x); err != nil {
		return err
	}
	h.sc = scaleOf(x)
	if h.maxW, h.maxH, err = maxScreenSize(x, scr.Root); err != nil {
		h.maxW, h.maxH = h.w, h.h // no resizing then
	}
	h.shiftKC, _, _ = h.keys.find(0xffe1) // Shift_L
	if err := h.clipInit(); err != nil {
		fmt.Fprintf(os.Stderr, "display: no clipboard sharing: %v\n", err)
	}
	h.winSent = -1
	h.started = time.Now()
	go serveAgentInHelper() // the launch agent, once the session is up
	if err := h.watchWindows(); err != nil {
		fmt.Fprintf(os.Stderr, "display: no window count: %v\n", err)
	} else {
		go h.windowCounter()
		if err := h.geomInit(); err != nil {
			fmt.Fprintf(os.Stderr, "display: no cursor image: %v\n", err)
		}
	}
	id, err := damage.NewDamageId(x)
	if err != nil {
		return err
	}
	if err := damage.CreateChecked(x, id, xproto.Drawable(scr.Root), damage.ReportLevelRawRectangles).Check(); err != nil {
		return fmt.Errorf("damage create: %w", err)
	}

	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer ln.Close()
	os.Chmod(sock, 0o600)
	if ready != "" {
		os.WriteFile(ready, []byte(fmt.Sprintf("%dx%d\n", h.w, h.h)), 0o600)
		sendTrace("guest-helper-ready")
		defer os.Remove(ready)
	}
	go h.accept(ln)
	go h.flusher()
	for {
		ev, xerr := x.WaitForEvent()
		if ev == nil && xerr == nil {
			return nil // the X server went away
		}
		switch e := ev.(type) {
		case damage.NotifyEvent:
			h.addRect(e.Area)
		case xproto.MappingNotifyEvent:
			if km, err := loadKeymap(x); err == nil {
				h.mu.Lock()
				h.keys = km
				h.mu.Unlock()
			}
		default:
			if !h.windowEvent(ev) && !h.geomEvent(ev) {
				h.clipEvent(ev)
			}
		}
	}
}

// accept takes one app connection at a time; a new one replaces the old.
func (h *fbHelper) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		// The H line goes first: no other writer until it's out.
		h.wmu.Lock()
		h.mu.Lock()
		if h.client != nil {
			h.client.Close()
		}
		h.client = c
		h.rects = []xproto.Rectangle{{X: 0, Y: 0, Width: h.w, Height: h.h}} // a full first frame
		w, hh := h.w, h.h
		h.mu.Unlock()
		hl, err := xwdHLine(h.fb, w, hh)
		if err == nil {
			c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err = c.Write([]byte(hl))
		}
		h.wmu.Unlock()
		if err != nil {
			c.Close()
			continue
		}
		h.signal()
		go func() { // the count right after H (the app's first frame comes with it)
			if h.winDirty != nil {
				h.sendWindows(true)
			}
			h.geomNewClient()
			h.sendGeometry(true)
			h.sendCursor(true)
		}()
		go h.readInput(c)
	}
}

func (h *fbHelper) signal() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// addRect queues a damaged rectangle; many small ones become their
// bounding box, so a busy screen never backs up the X connection.
func (h *fbHelper) addRect(r xproto.Rectangle) {
	h.mu.Lock()
	if h.client != nil {
		if len(h.rects) >= 64 {
			b := bounding(append(h.rects, r))
			h.rects = append(h.rects[:0], b)
		} else {
			h.rects = append(h.rects, r)
		}
	}
	h.mu.Unlock()
	h.signal()
}

// flusher sends queued rectangles to the app (a writer of its own, so a
// slow app never blocks reading X events).
func (h *fbHelper) flusher() {
	for range h.wake {
		h.wmu.Lock()
		h.mu.Lock()
		rs, c := h.rects, h.client
		h.rects = nil
		h.mu.Unlock()
		if c == nil || len(rs) == 0 {
			h.wmu.Unlock()
			continue
		}
		buf := make([]byte, 0, len(rs)*20+2)
		for _, r := range rs {
			// Within the current size: damage from before a resize may be larger.
			if r, ok := h.clip(int(r.X), int(r.Y), int(r.Width), int(r.Height)); ok {
				buf = fmt.Appendf(buf, "D %d %d %d %d\n", r.X, r.Y, r.Width, r.Height)
			}
		}
		buf = append(buf, "F\n"...)
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(buf); err != nil {
			c.Close()
		}
		h.wmu.Unlock()
	}
}

func bounding(rs []xproto.Rectangle) xproto.Rectangle {
	x0, y0, x1, y1 := int(rs[0].X), int(rs[0].Y), int(rs[0].X)+int(rs[0].Width), int(rs[0].Y)+int(rs[0].Height)
	for _, r := range rs[1:] {
		x0, y0 = min(x0, int(r.X)), min(y0, int(r.Y))
		x1, y1 = max(x1, int(r.X)+int(r.Width)), max(y1, int(r.Y)+int(r.Height))
	}
	return xproto.Rectangle{X: int16(x0), Y: int16(y0), Width: uint16(x1 - x0), Height: uint16(y1 - y0)}
}

// readInput plays the app's lines through XTEST.
func (h *fbHelper) readInput(c net.Conn) {
	defer func() {
		h.releaseAll()
		c.Close()
	}()
	rd := bufio.NewReaderSize(c, 64<<10)
	for {
		line, err := readCapped(rd, 2<<20) // a C line: 1 MB as base64, and some
		if err != nil {
			return
		}
		cmd, rest, _ := strings.Cut(line, " ")
		f := strings.Fields(rest)
		switch {
		case cmd == "M" && len(f) == 2:
			x, _ := strconv.Atoi(f[0])
			y, _ := strconv.Atoi(f[1])
			h.motion(x, y)
		case cmd == "B" && len(f) == 2:
			n, _ := strconv.Atoi(f[0])
			h.button(n, f[1] == "1")
		case cmd == "K" && len(f) == 2:
			sym, err := strconv.ParseUint(f[0], 16, 32)
			if err == nil {
				h.key(uint32(sym), f[1] == "1")
			}
		case cmd == "T":
			for _, r := range rest {
				sym := runeKeysym(r)
				h.key(sym, true)
				h.key(sym, false)
			}
		case cmd == "R" && len(f) == 2:
			w, _ := strconv.Atoi(f[0])
			hh, _ := strconv.Atoi(f[1])
			h.resize(c, w, hh)
		case cmd == "C":
			h.clipFromApp(strings.TrimSpace(rest))
		case cmd == "W":
			if h.winDirty != nil {
				h.sendWindows(true)
			}
		case cmd == "G":
			h.sendGeometry(true)
		case cmd == "Q":
			return
		}
	}
}

// readCapped reads one line (without its newline), keeping at most max
// bytes of it: a longer one is cut, not a reason to drop the app.
func readCapped(rd *bufio.Reader, max int) (string, error) {
	var b []byte
	for {
		part, err := rd.ReadSlice('\n')
		if len(b) < max {
			b = append(b, part[:min(len(part), max-len(b))]...)
		}
		switch err {
		case nil:
			return strings.TrimRight(string(b), "\r\n"), nil
		case bufio.ErrBufferFull:
			continue
		default:
			return "", err
		}
	}
}

// runeKeysym is the X keysym for a typed character: Latin-1 keysyms are
// the code point, others 0x01000000|cp; newline and tab are keys.
func runeKeysym(r rune) uint32 {
	switch {
	case r == '\n' || r == '\r':
		return 0xff0d
	case r == '\t':
		return 0xff09
	case r == '\b':
		return 0xff08
	case r >= 0x20 && r <= 0x7e || r >= 0xa0 && r <= 0xff:
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}

func (h *fbHelper) fake(typ byte, detail byte, x, y int16) {
	xtest.FakeInput(h.x, typ, detail, 0, h.root, x, y, 0)
}

// motion moves the pointer. Xvfb draws its cursor into the framebuffer
// but reports no damage for it, so the areas around the old and the new
// position are sent as damaged (64x64, enough for the usual cursors).
func (h *fbHelper) motion(x, y int) {
	h.mu.Lock()
	h.fake(xproto.MotionNotify, 0, int16(x), int16(y))
	h.x.Sync()
	old := h.ptr
	h.ptr = [2]int{x, y}
	h.mu.Unlock()
	time.Sleep(2 * time.Millisecond) // let Xvfb redraw the cursor first
	for _, p := range [][2]int{old, {x, y}} {
		if r, ok := h.clip(p[0]-24, p[1]-24, 64, 64); ok {
			h.addRect(r)
		}
	}
}

// clip limits a rectangle to the screen.
func (h *fbHelper) clip(x, y, w, hh int) (xproto.Rectangle, bool) {
	h.mu.Lock()
	sw, sh := int(h.w), int(h.h)
	h.mu.Unlock()
	x0, y0 := max(x, 0), max(y, 0)
	x1, y1 := min(x+w, sw), min(y+hh, sh)
	if x1 <= x0 || y1 <= y0 {
		return xproto.Rectangle{}, false
	}
	return xproto.Rectangle{X: int16(x0), Y: int16(y0), Width: uint16(x1 - x0), Height: uint16(y1 - y0)}, true
}

func (h *fbHelper) button(n int, down bool) {
	if n < 1 || n > 9 { // 4/5 wheel, 6/7 horizontal wheel, 8/9 back/forward
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	typ := byte(xproto.ButtonRelease)
	if down {
		typ = xproto.ButtonPress
		h.buttons |= 1 << (n - 1)
	} else {
		h.buttons &^= 1 << (n - 1)
	}
	h.fake(typ, byte(n), 0, 0)
	h.x.Sync()
}

func (h *fbHelper) key(sym uint32, down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !down {
		kc, ok := h.keysDown[sym]
		if !ok {
			return
		}
		h.fake(xproto.KeyRelease, byte(kc), 0, 0)
		if h.shifted[sym] && h.shiftKC != 0 {
			h.fake(xproto.KeyRelease, byte(h.shiftKC), 0, 0)
		}
		delete(h.keysDown, sym)
		delete(h.shifted, sym)
		h.x.Sync()
		return
	}
	kc, shift, ok := h.keys.find(sym)
	if !ok {
		var err error
		if kc, err = h.keys.remap(h.x, sym); err != nil {
			return
		}
		shift = false
	}
	if shift && h.shiftKC != 0 {
		h.fake(xproto.KeyPress, byte(h.shiftKC), 0, 0)
	}
	h.fake(xproto.KeyPress, byte(kc), 0, 0)
	h.keysDown[sym], h.shifted[sym] = kc, shift
	h.x.Sync()
}

// releaseAll lets go of every key and button when the app disconnects.
func (h *fbHelper) releaseAll() {
	h.mu.Lock()
	for sym, kc := range h.keysDown {
		h.fake(xproto.KeyRelease, byte(kc), 0, 0)
		if h.shifted[sym] && h.shiftKC != 0 {
			h.fake(xproto.KeyRelease, byte(h.shiftKC), 0, 0)
		}
	}
	h.keysDown, h.shifted = map[uint32]xproto.Keycode{}, map[uint32]bool{}
	for bit := 0; bit < 9; bit++ {
		if h.buttons&(1<<bit) != 0 {
			h.fake(xproto.ButtonRelease, byte(bit+1), 0, 0)
		}
	}
	h.buttons = 0
	h.mu.Unlock()
	h.x.Sync()
}

// resize is R w h: the screen to w x h through RandR (up to Xvfb's start
// size), then an H line for the size it has (the old one if refused) and
// a full frame.
func (h *fbHelper) resize(c net.Conn, w, hh int) {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	h.mu.Lock()
	curW, curH := h.w, h.h
	h.mu.Unlock()
	switch {
	case w < 1 || hh < 1 || w > int(h.maxW) || hh > int(h.maxH):
		fmt.Fprintf(os.Stderr, "display: resize to %dx%d refused (max %dx%d)\n", w, hh, h.maxW, h.maxH)
	case w == int(curW) && hh == int(curH):
	default:
		if err := setScreenSize(h.x, h.root, h.sc, curW, curH, uint16(w), uint16(hh)); err != nil {
			fmt.Fprintf(os.Stderr, "display: resize to %dx%d: %v\n", w, hh, err)
			break
		}
		h.mu.Lock()
		h.w, h.h = uint16(w), uint16(hh)
		h.mu.Unlock()
		// Clients that resize their windows without painting the new
		// area get an Expose: now, and again once they've handled the
		// RandR event and resized (xfdesktop takes a moment).
		refreshScreen(h.x, h.root, uint16(w), uint16(hh))
		go h.afterResize(curW, curH, uint16(w), uint16(hh))
	}
	h.mu.Lock()
	curW, curH = h.w, h.h
	h.rects = []xproto.Rectangle{{X: 0, Y: 0, Width: curW, Height: curH}}
	h.mu.Unlock()
	if hl, err := xwdHLine(h.fb, curW, curH); err == nil {
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte(hl)); err != nil {
			c.Close()
		}
	}
	h.signal()
}

// xwdHLine reads Xvfb's -fbdir file header (XWD, big-endian CARD32
// fields) into the H line: size, stride, pixel offset, bpp, colour shifts.
// w and h, when not 0, are the screen's size now: after a RandR resize
// the header still has Xvfb's start size, but the stride stays.
func xwdHLine(path string, w, h uint16) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var hd [25]uint32
	if err := binary.Read(f, binary.BigEndian, &hd); err != nil {
		return "", err
	}
	headerSize, width, height, bpp, stride, ncolors := hd[0], hd[4], hd[5], hd[11], hd[12], hd[19]
	if bpp != 32 || hd[7] != 0 { // LSBFirst 32 bpp is what the app uploads
		return "", fmt.Errorf("unexpected framebuffer: %d bpp, byte order %d", bpp, hd[7])
	}
	if w != 0 && h != 0 && uint32(w) <= width && uint32(h) <= height {
		width, height = uint32(w), uint32(h)
	}
	shift := func(mask uint32) int { return bits.TrailingZeros32(mask) }
	return fmt.Sprintf("H %d %d %d %d %d %d %d %d\n", width, height, stride, headerSize+ncolors*12, bpp,
		shift(hd[14]), shift(hd[15]), shift(hd[16])), nil
}

// keymap is the server's keycode -> keysyms table, with spare keycodes
// (no keysyms at all) for keysyms the layout doesn't have (Unicode text),
// reused round-robin the way x11vnc does.
type keymap struct {
	min    xproto.Keycode
	per    int
	syms   []xproto.Keysym
	spare  []xproto.Keycode
	next   int
	mapped map[uint32]xproto.Keycode
}

func loadKeymap(x *xgb.Conn) (*keymap, error) {
	s := xproto.Setup(x)
	n := byte(s.MaxKeycode - s.MinKeycode + 1)
	rep, err := xproto.GetKeyboardMapping(x, s.MinKeycode, n).Reply()
	if err != nil {
		return nil, err
	}
	k := &keymap{min: s.MinKeycode, per: int(rep.KeysymsPerKeycode), syms: rep.Keysyms, mapped: map[uint32]xproto.Keycode{}}
	for i := 0; i < int(n); i++ {
		empty := true
		for j := 0; j < k.per; j++ {
			if k.syms[i*k.per+j] != 0 {
				empty = false
			}
		}
		if empty {
			k.spare = append(k.spare, k.min+xproto.Keycode(i))
		}
	}
	return k, nil
}

// find returns the keycode for sym and whether it needs Shift (the
// keysym is in the keycode's second column).
func (k *keymap) find(sym uint32) (xproto.Keycode, bool, bool) {
	if kc, ok := k.mapped[sym]; ok {
		return kc, false, true
	}
	for col := 0; col < 2 && col < k.per; col++ {
		for i := 0; i*k.per+col < len(k.syms); i++ {
			if uint32(k.syms[i*k.per+col]) == sym {
				return k.min + xproto.Keycode(i), col == 1, true
			}
		}
	}
	return 0, false, false
}

// remap puts sym on the next spare keycode (both columns, so no Shift).
func (k *keymap) remap(x *xgb.Conn, sym uint32) (xproto.Keycode, error) {
	if len(k.spare) == 0 {
		return 0, fmt.Errorf("no spare keycode")
	}
	kc := k.spare[k.next%len(k.spare)]
	k.next++
	for s, c := range k.mapped {
		if c == kc {
			delete(k.mapped, s)
		}
	}
	syms := make([]xproto.Keysym, k.per)
	syms[0] = xproto.Keysym(sym)
	if k.per > 1 {
		syms[1] = xproto.Keysym(sym)
	}
	if err := xproto.ChangeKeyboardMappingChecked(x, 1, kc, byte(k.per), syms).Check(); err != nil {
		return 0, err
	}
	k.mapped[sym] = kc
	time.Sleep(20 * time.Millisecond) // let clients see the MappingNotify first
	return kc, nil
}

// DisplayStart is `andronix display start WxH :N [dpi] [--max-size MxM]`
// inside the distro: Xvfb with its screen in /tmp/.andronix-d/N/Xvfb_screen0
// (at the max size, then set to WxH through RandR), the desktop session on
// it, then the helper in the foreground until Xvfb ends.
func DisplayStart(args []string, maxSize string, safe bool) error {
	if len(args) < 2 || !reSize.MatchString(args[0]) {
		return ui.Errorf("usage", "Usage", "andronix display start WIDTHxHEIGHT :N [dpi] [--max-size WxH]", "")
	}
	w, h, _ := parseSize(args[0])
	start := args[0]
	if maxSize != "" {
		mw, mh, ok := parseSize(maxSize)
		if !ok || mw < w || mh < h {
			return ui.Errorf("bad_maximum_size", "Bad maximum size", "'"+maxSize+"' must be WIDTHxHEIGHT and at least "+args[0]+".", "")
		}
		start = maxSize
	}
	num, err := strconv.Atoi(strings.TrimPrefix(args[1], ":"))
	if err != nil {
		return ui.Errorf("bad_display", "Bad display", "'"+args[1]+"' is not a display number.", "")
	}
	sendTrace("guest-display-start")
	if _, err := sys.LookPath("Xvfb"); err != nil {
		return ui.Errorf("xvfb_isn_t_installed", "Xvfb isn't installed", "The built-in display needs Xvfb.", "Run andronix display again from the app.")
	}
	hm := home()
	if _, err := os.Stat(filepath.Join(hm, ".config/tigervnc/xstartup")); err != nil {
		syncSkel("/etc/skel", hm, os.Getuid(), os.Getgid())
	}
	xstartup := filepath.Join(hm, ".config/tigervnc/xstartup")
	if _, err := os.Stat(xstartup); err != nil {
		xstartup = filepath.Join(hm, ".vnc/xstartup")
	}
	if safe {
		// A saved session can bring back what made the desktop black.
		if c := filepath.Join(hm, ".cache/sessions"); exists(c) {
			os.Rename(c, c+".andronix-safe-"+time.Now().Format("20060102-150405"))
		}
	}
	if pidAlive(fmt.Sprintf("/tmp/.X%d-lock", num)) {
		return ui.Errorf("display_in_use", fmt.Sprintf("Display :%d is in use", num), "Another X server runs on it.", "Stop it first: andronix display stop")
	}
	os.Remove(fmt.Sprintf("/tmp/.X%d-lock", num))
	os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", num))
	dir := fmt.Sprintf("/tmp/.andronix-d/%d", num)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// -noreset: when its last client goes (the resize below, before the
	// session connects) Xvfb would reset to its start size.
	xargs := []string{fmt.Sprintf(":%d", num), "-screen", "0", start + "x24", "-fbdir", dir, "-nolisten", "tcp", "-noreset"}
	if len(args) > 2 {
		xargs = append(xargs, "-dpi", args[2])
	}
	logf := fmt.Sprintf("/tmp/andronix-display-%d.log", num)
	lg, err := os.Create(logf)
	if err != nil {
		return err
	}
	defer lg.Close()
	// The desktop starts at the app's size, not the max.
	up := func() {
		if start == args[0] {
			return
		}
		err := resizeDisplay(fmt.Sprintf(":%d", num), uint16(w), uint16(h))
		fmt.Fprintf(lg, "andronix: screen %s (Xvfb %s): %v\n", args[0], start, err)
		sendTrace("guest-randr-done")
	}
	// The framebuffer file (start is the square max, 4 bytes a pixel)
	// plus the log's cap.
	var sw, sh int64
	fmt.Sscanf(start, "%dx%d", &sw, &sh)
	if out, err := startXServer("Xvfb", xargs, num, xstartup, lg, logf, up, sw*sh*4+xServerFileCap); err != nil {
		return ui.Errorf("the_display_didn_t_start", "The display didn't start", lastNonEmpty(string(out), 3), "Log: "+logf)
	}
	sendTrace("guest-session-started")
	fmt.Printf("Xvfb :%d %s (up to %s), framebuffer in %s\n", num, args[0], start, dir)
	return FBHelper(fmt.Sprintf(":%d", num), fmt.Sprintf("/tmp/.andronix-d/%d.sock", num),
		filepath.Join(dir, "Xvfb_screen0"), fmt.Sprintf("/tmp/.andronix-d/%d.ready", num))
}

// afterResize helps clients catch up with a resize the helper made, from
// a screen of oldW x oldH. All windows get repainted (refreshScreen) at 0.3
// and 1.5 s. xfdesktop sometimes keeps its old size and never paints the
// new area (two resizes in quick succession do it every time; android
// 5640: one fold right at READY); a reload doesn't help, another resize
// done slowly does. So 4 s in, if the new area is still unwritten in the
// framebuffer file (all zero bytes, which no X drawing leaves), the screen
// is resized 1 px smaller and back with a pause, at most twice.
// ANDRONIX_FOLD_FIX: "check" (default, the above), "reload" (also
// xfdesktop --reload), "restart" (xfdesktop and the panel afresh) or "off".
func (h *fbHelper) afterResize(oldW, oldH, w, hh uint16) {
	same := func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.w == w && h.h == hh
	}
	fix := os.Getenv("ANDRONIX_FOLD_FIX")
	if fix == "" {
		fix = "check"
	}
	for _, d := range []time.Duration{300 * time.Millisecond, 1200 * time.Millisecond} {
		time.Sleep(d)
		if !same() {
			return // resized again meanwhile; that one does this
		}
		refreshScreen(h.x, h.root, w, hh)
	}
	switch fix {
	case "off":
		return
	case "reload":
		askDesktop("desktop-reload")
	case "restart":
		askDesktop("desktop-restart")
		return
	}
	// A whole screen that's still unwritten is a desktop that hasn't drawn
	// yet (a resize right at READY), not a stale one: resizing then, while
	// XFCE's clients are starting, made them lose windows (sv2-gpu). So it
	// waits until the desktop has drawn part of the screen and mapped its
	// windows, and isn't in its first 15 s, checking every 2.5 s for 45 s.
	tries := 0
	for end := time.Now().Add(45 * time.Second); tries < 2 && time.Now().Before(end); {
		time.Sleep(2500 * time.Millisecond)
		if !same() {
			return
		}
		blank, painted := h.unwritten(w, hh)
		if blank == 0 {
			return // all drawn
		}
		if painted == 0 || time.Since(h.started) < 15*time.Second {
			continue // not drawn yet: XFCE is still starting
		}
		if !h.fullScreenMapped(w, hh) {
			continue // the desktop window isn't up yet (or not at this size)
		}
		tries++
		fmt.Fprintf(os.Stderr, "display: %dx%d left unpainted after a resize; resizing again\n", w, hh)
		h.wmu.Lock()
		err := setScreenSize(h.x, h.root, h.sc, w, hh, w, hh-1)
		h.wmu.Unlock()
		if err != nil {
			return
		}
		time.Sleep(2500 * time.Millisecond)
		h.wmu.Lock()
		setScreenSize(h.x, h.root, h.sc, w, hh-1, w, hh)
		h.wmu.Unlock()
		refreshScreen(h.x, h.root, w, hh)
	}
}

// fullScreenMapped reports whether a mapped window covers the whole w x hh
// screen: the desktop's (xfdesktop) once it's up and has taken the size.
// A desktop that kept a stale size has it, one still starting doesn't.
func (h *fbHelper) fullScreenMapped(w, hh uint16) bool {
	tree, err := xproto.QueryTree(h.x, h.root).Reply()
	if err != nil {
		return false
	}
	for _, c := range tree.Children {
		a, err := xproto.GetWindowAttributes(h.x, c).Reply()
		if err != nil || a.MapState != xproto.MapStateViewable || a.Class != xproto.WindowClassInputOutput {
			continue
		}
		g, err := xproto.GetGeometry(h.x, xproto.Drawable(c)).Reply()
		if err == nil && g.X <= 0 && g.Y <= 0 && int(g.X)+int(g.Width) >= int(w) && int(g.Y)+int(g.Height) >= int(hh) {
			return true
		}
	}
	return false
}

// unwritten counts the points of an 8x8 grid over the screen that are
// still four zero bytes in the framebuffer file (blank: no X drawing leaves
// those, even black is 000000ff) and those that aren't (painted). A stale
// desktop leaves both; one that hasn't drawn yet leaves only blank.
func (h *fbHelper) unwritten(w, hh uint16) (blank, painted int) {
	f, err := os.Open(h.fb)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var hd [25]uint32
	if binary.Read(f, binary.BigEndian, &hd) != nil {
		return 0, 0
	}
	off, stride := int64(hd[0]+hd[19]*12), int64(hd[12])
	b := make([]byte, 4)
	for j := 0; j < 8; j++ {
		y := (2*j + 1) * int(hh) / 16
		for i := 0; i < 8; i++ {
			x := (2*i + 1) * int(w) / 16
			if _, err := f.ReadAt(b, off+int64(y)*stride+int64(x)*4); err != nil {
				return 0, 0
			}
			if b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 0 {
				blank++
			} else {
				painted++
			}
		}
	}
	return blank, painted
}
