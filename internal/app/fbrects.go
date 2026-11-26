package app

import "github.com/jezek/xgb/xproto"

// Damage rectangles queued for the app (display-plan P3.4): past rectCap
// the queue is coalesced to at most rectKeep rectangles by cheapest-union
// merging, instead of one bounding box as before, so a panel clock
// ticking far from a window drag doesn't turn every batch into a
// near-full-frame upload.
const (
	rectCap  = 256
	rectKeep = 16
)

func rectArea(r xproto.Rectangle) int { return int(r.Width) * int(r.Height) }

// rectUnion is the bounding box of a and b.
func rectUnion(a, b xproto.Rectangle) xproto.Rectangle {
	x0, y0 := min(int(a.X), int(b.X)), min(int(a.Y), int(b.Y))
	x1 := max(int(a.X)+int(a.Width), int(b.X)+int(b.Width))
	y1 := max(int(a.Y)+int(a.Height), int(b.Y)+int(b.Height))
	return xproto.Rectangle{X: int16(x0), Y: int16(y0), Width: uint16(x1 - x0), Height: uint16(y1 - y0)}
}

// mergeGrowth is the area the union of a and b covers that neither a nor
// b does: 0 when one contains the other or they tile exactly, big when
// they are far apart. The merge with the least growth wastes the least
// upload.
func mergeGrowth(a, b xproto.Rectangle) int {
	ix0, iy0 := max(int(a.X), int(b.X)), max(int(a.Y), int(b.Y))
	ix1 := min(int(a.X)+int(a.Width), int(b.X)+int(b.Width))
	iy1 := min(int(a.Y)+int(a.Height), int(b.Y)+int(b.Height))
	inter := 0
	if ix1 > ix0 && iy1 > iy0 {
		inter = (ix1 - ix0) * (iy1 - iy0)
	}
	return rectArea(rectUnion(a, b)) - (rectArea(a) + rectArea(b) - inter)
}

// coalesce reduces rs to at most n rectangles: rectangles are taken in
// order, and whenever there are n+1 the two whose union grows least are
// merged (cheapest merge). The result covers everything in rs; a lone
// rectangle far from the rest (a panel clock during a drag) stays its own
// as long as any two others are closer. O(len(rs) * n^2): n is small.
func coalesce(rs []xproto.Rectangle, n int) []xproto.Rectangle {
	if n < 1 {
		n = 1
	}
	if len(rs) <= n {
		return rs
	}
	out := make([]xproto.Rectangle, 0, n+1)
	for _, r := range rs {
		out = append(out, r)
		if len(out) <= n {
			continue
		}
		bi, bj, best := 0, 1, mergeGrowth(out[0], out[1])
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				if g := mergeGrowth(out[i], out[j]); g < best {
					bi, bj, best = i, j, g
				}
			}
		}
		out[bi] = rectUnion(out[bi], out[bj])
		out[bj] = out[len(out)-1]
		out = out[:len(out)-1]
	}
	return out
}
