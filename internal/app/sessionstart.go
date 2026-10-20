package app

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// XFCE's session starts its failsafe clients by priority group (xfwm4 15,
// xfsettingsd 20, xfce4-panel 25, Thunar --daemon 30, xfdesktop 35) and
// waits for each group to register before the next: 1.3-5.6 s of a warm
// start on Device Farm phones (sv2-startup). One group starts them
// together. Thunar's daemon stays: without it xfdesktop starts Thunar over
// D-Bus anyway (twice, racing) and maps its window only after that.

// sessionChannel is xfce4-session's xfconf channel file, under an XDG dir.
const sessionChannel = "xfce4/xfconf/xfce-perchannel-xml/xfce4-session.xml"

// writeSessionLayer writes base/xfce4/.../xfce4-session.xml: the distro's
// own channel (root/etc/xdg) with its failsafe clients in one priority
// group; standalone also turns off saving the
// session on exit (a saved session would bring the order back). Nothing
// when the distro has no xfce4-session channel.
func writeSessionLayer(root, base string, standalone bool) error {
	b, err := os.ReadFile(filepath.Join(root, "etc/xdg", sessionChannel))
	if err != nil {
		return nil
	}
	var doc xnode
	if xml.Unmarshal(b, &doc) != nil || doc.XMLName.Local != "channel" {
		return nil
	}
	if !parallelFailsafe(&doc) {
		return nil
	}
	if standalone {
		doc.setProp([]string{"general", "SaveOnExit"}, "bool", "false")
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	buf.WriteString("\n")
	return writeFile(filepath.Join(base, sessionChannel), buf.String())
}

// parallelFailsafe gives the channel's failsafe clients (sessions/Failsafe,
// ClientN_Priority) one priority, the lowest they had. False if there's no
// failsafe session to change.
func parallelFailsafe(doc *xnode) bool {
	fs := doc.child("sessions")
	if fs != nil {
		fs = fs.child("Failsafe")
	}
	if fs == nil {
		return false
	}
	low := -1
	var prios []*xnode
	for i := range fs.Nodes {
		p := &fs.Nodes[i]
		name := p.attr("name")
		if p.XMLName.Local != "property" || !strings.HasPrefix(name, "Client") || !strings.HasSuffix(name, "_Priority") {
			continue
		}
		n, err := strconv.Atoi(p.attr("value"))
		if err != nil {
			continue
		}
		prios = append(prios, p)
		if low < 0 || n < low {
			low = n
		}
	}
	if len(prios) == 0 {
		return false
	}
	for _, p := range prios {
		p.setAttr("value", strconv.Itoa(low))
	}
	return true
}

// child is the property named name directly under n, or nil.
func (n *xnode) child(name string) *xnode {
	if n == nil {
		return nil
	}
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == "property" && n.Nodes[i].attr("name") == name {
			return &n.Nodes[i]
		}
	}
	return nil
}
