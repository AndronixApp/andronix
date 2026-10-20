package app

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"time"
	"unicode/utf8"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

// The clipboard between the app and the desktop (C lines, text only):
//
//	helper -> app: C <base64 UTF-8>   an X client copied text (CLIPBOARD)
//	app -> helper: C <base64 UTF-8>   the phone's clipboard: the helper owns
//	                                  CLIPBOARD and PRIMARY with it, so
//	                                  Ctrl+V and middle-click both paste it
//
// At most clipMax bytes each way (more is cut and logged). What the app
// sent never comes back to it, and the same text isn't sent twice (a
// clipboard manager taking over CLIPBOARD reads as a new owner).
const clipMax = 1 << 20

// clipChunk keeps each ChangeProperty under the core 256 KB request limit.
const clipChunk = 128 << 10

type clipState struct {
	win                                        xproto.Window
	clipboard, utf8, targets, text, incr, prop xproto.Atom
	owned                                      []byte // what we serve while we own the selections
	last                                       []byte // the last text either side had, against echoes
	incrBuf                                    []byte // an INCR transfer in progress
	inIncr                                     bool
	triedString                                bool
}

func internAtom(x *xgb.Conn, name string) (xproto.Atom, error) {
	r, err := xproto.InternAtom(x, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return r.Atom, nil
}

// clipInit makes the helper's selection window and asks XFixes to tell it
// when CLIPBOARD gets a new owner.
func (h *fbHelper) clipInit() error {
	c := &clipState{}
	var err error
	for _, a := range []struct {
		p    *xproto.Atom
		name string
	}{{&c.clipboard, "CLIPBOARD"}, {&c.utf8, "UTF8_STRING"}, {&c.targets, "TARGETS"}, {&c.text, "TEXT"}, {&c.incr, "INCR"}, {&c.prop, "ANDRONIX_CLIP"}} {
		if *a.p, err = internAtom(h.x, a.name); err != nil {
			return err
		}
	}
	if c.win, err = xproto.NewWindowId(h.x); err != nil {
		return err
	}
	if err := xproto.CreateWindowChecked(h.x, 0, c.win, h.root, -10, -10, 1, 1, 0, xproto.WindowClassInputOnly, 0,
		xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return err
	}
	if err := xfixes.SelectSelectionInputChecked(h.x, c.win, c.clipboard, xfixes.SelectionEventMaskSetSelectionOwner).Check(); err != nil {
		return err
	}
	h.cb = c
	return nil
}

// clipEvent handles the selection events; false if ev isn't one.
func (h *fbHelper) clipEvent(ev xgb.Event) bool {
	c := h.cb
	if c == nil {
		return false
	}
	switch e := ev.(type) {
	case xfixes.SelectionNotifyEvent:
		if e.Selection == c.clipboard && e.Owner != 0 && e.Owner != c.win {
			c.triedString = false
			xproto.ConvertSelection(h.x, c.win, c.clipboard, c.utf8, c.prop, e.SelectionTimestamp)
		}
	case xproto.SelectionNotifyEvent:
		if e.Requestor != c.win || e.Selection != c.clipboard {
			break
		}
		if e.Property == 0 { // no UTF8_STRING: try STRING (Latin-1) once
			if !c.triedString {
				c.triedString = true
				xproto.ConvertSelection(h.x, c.win, c.clipboard, xproto.AtomString, c.prop, e.Time)
			}
			break
		}
		h.clipRead(e.Target == xproto.AtomString)
	case xproto.PropertyNotifyEvent:
		if e.Window == c.win && e.Atom == c.prop && e.State == xproto.PropertyNewValue && c.inIncr {
			h.clipIncrChunk()
		}
	case xproto.SelectionRequestEvent:
		h.clipServe(e)
	case xproto.SelectionClearEvent:
		if e.Owner == c.win && e.Selection == c.clipboard {
			c.owned = nil
		}
	default:
		return false
	}
	return true
}

// clipRead reads the converted text off the helper's window.
func (h *fbHelper) clipRead(latin1 bool) {
	c := h.cb
	r, err := xproto.GetProperty(h.x, true, c.win, c.prop, xproto.GetPropertyTypeAny, 0, clipMax/4+1).Reply()
	if err != nil {
		return
	}
	if r.Type == c.incr { // a large copy comes in parts (ICCCM INCR)
		c.inIncr, c.incrBuf = true, nil
		return
	}
	data := r.Value
	if latin1 {
		data = latin1ToUTF8(data)
	}
	h.clipToApp(data)
}

// clipIncrChunk takes one part of an INCR transfer; an empty one ends it.
func (h *fbHelper) clipIncrChunk() {
	c := h.cb
	r, err := xproto.GetProperty(h.x, true, c.win, c.prop, xproto.GetPropertyTypeAny, 0, clipMax/4+1).Reply()
	if err != nil {
		c.inIncr = false
		return
	}
	if len(r.Value) == 0 {
		c.inIncr = false
		h.clipToApp(c.incrBuf)
		c.incrBuf = nil
		return
	}
	if len(c.incrBuf) <= clipMax {
		c.incrBuf = append(c.incrBuf, r.Value...)
	}
}

func (h *fbHelper) clipToApp(data []byte) {
	c := h.cb
	if len(data) > clipMax {
		fmt.Fprintf(os.Stderr, "display: copied text cut from %d to %d bytes\n", len(data), clipMax)
		data = cutUTF8(data, clipMax)
	}
	h.mu.Lock()
	same := len(data) == 0 || bytes.Equal(data, c.last)
	if !same {
		c.last = append([]byte(nil), data...)
	}
	h.mu.Unlock()
	if same {
		return
	}
	line := "C " + base64.StdEncoding.EncodeToString(data) + "\n"
	h.wmu.Lock()
	h.mu.Lock()
	cl := h.client
	h.mu.Unlock()
	if cl != nil {
		// Its own deadline: the last one (another writer's) may have passed.
		cl.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := cl.Write([]byte(line)); err != nil {
			fmt.Fprintf(os.Stderr, "display: clipboard to the app: %v\n", err)
			cl.Close()
		}
	}
	h.wmu.Unlock()
}

// clipFromApp is the app's C line: own CLIPBOARD and PRIMARY with it.
func (h *fbHelper) clipFromApp(b64 string) {
	c := h.cb
	if c == nil {
		return
	}
	cut := false
	if n := base64.StdEncoding.EncodedLen(clipMax); len(b64) > n {
		b64, cut = b64[:n], true
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return
	}
	if cut || len(data) > clipMax {
		fmt.Fprintf(os.Stderr, "display: pasted text cut to %d bytes\n", clipMax)
		data = cutUTF8(data, clipMax)
	}
	h.mu.Lock() // the X event loop reads owned and last
	c.owned = data
	c.last = append([]byte(nil), data...)
	h.mu.Unlock()
	xproto.SetSelectionOwner(h.x, c.win, c.clipboard, xproto.TimeCurrentTime)
	xproto.SetSelectionOwner(h.x, c.win, xproto.AtomPrimary, xproto.TimeCurrentTime)
	h.x.Sync()
}

// clipServe answers a paste from an X client while the helper owns the
// selections: TARGETS, or the text as UTF8_STRING, TEXT or STRING.
func (h *fbHelper) clipServe(e xproto.SelectionRequestEvent) {
	c := h.cb
	h.mu.Lock()
	data := c.owned
	h.mu.Unlock()
	prop := e.Property
	if prop == 0 { // an old client: the target names the property
		prop = e.Target
	}
	ok := data != nil && e.Owner == c.win && (e.Selection == c.clipboard || e.Selection == xproto.AtomPrimary)
	if ok {
		switch e.Target {
		case c.targets:
			atoms := []xproto.Atom{c.targets, c.utf8, c.text, xproto.AtomString}
			buf := make([]byte, 4*len(atoms))
			for i, a := range atoms {
				binary.LittleEndian.PutUint32(buf[4*i:], uint32(a))
			}
			xproto.ChangeProperty(h.x, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomAtom, 32, uint32(len(atoms)), buf)
		case c.utf8, c.text, xproto.AtomString:
			typ := c.utf8
			if e.Target == xproto.AtomString {
				typ, data = xproto.AtomString, utf8ToLatin1(data)
			}
			// In parts appended to one property: each request stays small.
			xproto.ChangeProperty(h.x, xproto.PropModeReplace, e.Requestor, prop, typ, 8, 0, nil)
			for off := 0; off < len(data); off += clipChunk {
				part := data[off:min(off+clipChunk, len(data))]
				xproto.ChangeProperty(h.x, xproto.PropModeAppend, e.Requestor, prop, typ, 8, uint32(len(part)), part)
			}
		default:
			ok = false
		}
	}
	if !ok {
		prop = 0 // refused
	}
	ev := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: prop}
	xproto.SendEvent(h.x, false, e.Requestor, 0, string(ev.Bytes()))
	h.x.Sync()
}

// cutUTF8 shortens b to at most n bytes without splitting a character.
func cutUTF8(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	for n > 0 && !utf8.RuneStart(b[n]) { // b[n] continues the character before
		n--
	}
	return b[:n]
}

func latin1ToUTF8(b []byte) []byte {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return []byte(string(r))
}

func utf8ToLatin1(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, r := range string(b) {
		if r < 0x100 {
			out = append(out, byte(r))
		} else {
			out = append(out, '?')
		}
	}
	return out
}
