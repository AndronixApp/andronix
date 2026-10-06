package app

import (
	"io"
	"log"
	"net"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/AndronixApp/andronix-distros/internal/compat"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// Did the desktop draw? With telemetry on, `andronix desktop` looks at the
// Termux:X11 screen once, 20 s after the session starts, and the start
// event says drew=true or false. That's how we learn whether desktops work
// on old kernels (below 4.8 one emulator stayed black, real phones may
// not). A few rows of the root window are read into memory and only
// counted: no picture is kept, written to a file or sent.

// drewRows is how many rows are read, spread over the screen, plus the
// top and bottom edges (where panels are).
const drewRows = 12

// screenDrew connects to the X server on sock and reports whether its
// screen shows more than (near) black. ok is false when it can't tell.
func screenDrew(sock string) (drew, ok bool) {
	xgb.Logger = log.New(io.Discard, "", 0) // its notes would land in the user's terminal
	nc, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return false, false
	}
	x, err := xgb.NewConnNet(nc)
	if err != nil {
		nc.Close()
		return false, false
	}
	defer x.Close()
	setup := xproto.Setup(x)
	scr := setup.DefaultScreen(x)
	bpp := 0
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if bpp != 32 || scr.WidthInPixels == 0 || scr.HeightInPixels < 16 {
		return false, false
	}
	h := int(scr.HeightInPixels)
	ys := []int{4, h - 5}
	for i := 0; i < drewRows; i++ {
		ys = append(ys, h*(2*i+1)/(2*drewRows))
	}
	var bright, total int
	for _, y := range ys {
		r, err := xproto.GetImage(x, xproto.ImageFormatZPixmap, xproto.Drawable(scr.Root), 0, int16(y),
			scr.WidthInPixels, 1, 0xffffffff).Reply()
		if err != nil {
			return false, false
		}
		b, t := brightPixels(r.Data)
		bright, total = bright+b, total+t
	}
	return drewFrom(bright, total), total > 0
}

// brightPixels counts the 32-bit pixels in data that aren't near black
// (any colour channel above 48), and all of them.
func brightPixels(data []byte) (bright, total int) {
	for p := 0; p+3 < len(data); p += 4 {
		total++
		if max(data[p], data[p+1], data[p+2]) > 48 {
			bright++
		}
	}
	return bright, total
}

// drewFrom: at least 0.5% of the sampled pixels aren't near black. A
// desktop's wallpaper, icons and panels give far more; a black screen
// (its cursor isn't in the image) gives none.
func drewFrom(bright, total int) bool { return total > 0 && bright*200 >= total }

// kernelMajorMinor is this phone's kernel as "4.4" (the compat probe's form).
func kernelMajorMinor() string {
	return (&compat.Probe{Kernel: sys.KernelRelease()}).KernelMajorMinor()
}
