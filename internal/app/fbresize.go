package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
)

// Resizing the built-in display (R w h, rotation): Xvfb's RandR allows any
// size up to the one it started with, and only the visible size changes:
// the -fbdir file and its stride stay. So Xvfb starts at the largest size
// the app may ask for (--max-size) and is set to the asked size at once.

// screenScale is the screen's millimetres per pixel, kept on every resize
// so the DPI stays what it was started with.
type screenScale struct{ x, y float64 }

func scaleOf(x *xgb.Conn) screenScale {
	s := xproto.Setup(x).DefaultScreen(x)
	if s.WidthInPixels == 0 || s.HeightInPixels == 0 {
		return screenScale{25.4 / 96, 25.4 / 96}
	}
	return screenScale{float64(s.WidthInMillimeters) / float64(s.WidthInPixels), float64(s.HeightInMillimeters) / float64(s.HeightInPixels)}
}

// maxScreenSize is the largest size RandR allows (Xvfb: its start size).
func maxScreenSize(x *xgb.Conn, root xproto.Window) (uint16, uint16, error) {
	r, err := randr.GetScreenSizeRange(x, root).Reply()
	if err != nil {
		return 0, 0, err
	}
	return r.MaxWidth, r.MaxHeight, nil
}

// setScreenSize does what `xrandr --fb WxH --output screen --mode WxH`
// does: a mode of that size on the output (made if missing), the CRTC on
// it, and the screen size, in an order that keeps the CRTC inside the
// screen. cur is the screen's size now.
func setScreenSize(x *xgb.Conn, root xproto.Window, sc screenScale, curW, curH, w, h uint16) error {
	res, err := randr.GetScreenResources(x, root).Reply()
	if err != nil {
		return fmt.Errorf("randr resources: %w", err)
	}
	if len(res.Crtcs) == 0 || len(res.Outputs) == 0 {
		return fmt.Errorf("randr: no crtc or output")
	}
	crtc, out := res.Crtcs[0], res.Outputs[0]
	var mode randr.Mode
	for _, m := range res.Modes {
		if m.Width == w && m.Height == h {
			mode = randr.Mode(m.Id)
			break
		}
	}
	if mode == 0 {
		name := fmt.Sprintf("%dx%d", w, h)
		mi := randr.ModeInfo{Width: w, Height: h, DotClock: uint32(w) * uint32(h) * 60,
			HsyncStart: w, HsyncEnd: w, Htotal: w, VsyncStart: h, VsyncEnd: h, Vtotal: h, NameLen: uint16(len(name))}
		r, err := randr.CreateMode(x, root, mi, name).Reply()
		if err != nil {
			return fmt.Errorf("randr mode %s: %w", name, err)
		}
		mode = r.Mode
	}
	if err := randr.AddOutputModeChecked(x, out, mode).Check(); err != nil {
		return fmt.Errorf("randr add mode: %w", err)
	}
	size := func(w, h uint16) error {
		mmW, mmH := uint32(float64(w)*sc.x+0.5), uint32(float64(h)*sc.y+0.5)
		return randr.SetScreenSizeChecked(x, root, w, h, max(mmW, 1), max(mmH, 1)).Check()
	}
	// Big enough for the old CRTC and the new one, then the CRTC, then
	// exactly the new size.
	bigW, bigH := max(curW, w), max(curH, h)
	if bigW != curW || bigH != curH {
		if err := size(bigW, bigH); err != nil {
			return fmt.Errorf("randr screen size %dx%d: %w", bigW, bigH, err)
		}
	}
	r, err := randr.SetCrtcConfig(x, crtc, xproto.TimeCurrentTime, res.ConfigTimestamp, 0, 0, mode, randr.RotationRotate0, []randr.Output{out}).Reply()
	if err != nil {
		return fmt.Errorf("randr crtc: %w", err)
	}
	if r.Status != randr.SetConfigSuccess {
		return fmt.Errorf("randr crtc: status %d", r.Status)
	}
	if bigW != w || bigH != h {
		if err := size(w, h); err != nil {
			return fmt.Errorf("randr screen size %dx%d: %w", w, h, err)
		}
	}
	return nil
}

// resizeDisplay connects to display and sets its size (before the desktop
// session starts, so it begins at the app's size).
func resizeDisplay(display string, w, h uint16) error {
	x, err := xgb.NewConnDisplay(display)
	if err != nil {
		return err
	}
	defer x.Close()
	if err := randr.Init(x); err != nil {
		return err
	}
	s := xproto.Setup(x).DefaultScreen(x)
	if s.WidthInPixels == w && s.HeightInPixels == h {
		return nil
	}
	return setScreenSize(x, s.Root, scaleOf(x), s.WidthInPixels, s.HeightInPixels, w, h)
}

// parseSize reads WIDTHxHEIGHT (reSize's form).
func parseSize(s string) (int, int, bool) {
	if !reSize.MatchString(s) {
		return 0, 0, false
	}
	a, b, _ := strings.Cut(s, "x")
	w, _ := strconv.Atoi(a)
	h, _ := strconv.Atoi(b)
	return w, h, w > 0 && h > 0
}

// refreshScreen makes every window repaint, as xrefresh does: a full-screen
// override-redirect window with no background is mapped and destroyed, and
// each window it covered gets an Expose. After a resize some clients
// (xfdesktop growing taller) resize their window but never paint the new
// part; this makes them.
func refreshScreen(x *xgb.Conn, root xproto.Window, w, h uint16) {
	wid, err := xproto.NewWindowId(x)
	if err != nil {
		return
	}
	if err := xproto.CreateWindowChecked(x, 0, wid, root, 0, 0, w, h, 0, xproto.WindowClassInputOutput, 0,
		xproto.CwBackPixmap|xproto.CwOverrideRedirect, []uint32{xproto.BackPixmapNone, 1}).Check(); err != nil {
		return
	}
	xproto.MapWindow(x, wid)
	xproto.DestroyWindow(x, wid)
	x.Sync()
}
