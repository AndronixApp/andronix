package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// Fewer XFCE processes for Android's phantom process killer (32 for all
// apps together; lead, Oct 7): the session's failsafe clients in
// DE_SESSION_DROP (Thunar's daemon: Thunar still opens when asked) and the
// panel plugins in DE_PANEL_DROP (the status tray, which nothing uses
// under proot, runs in its own wrapper process) are taken out of XFCE's
// system defaults. A user's own saved session or panel isn't touched.
const (
	xfceSessionFile = "etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-session.xml"
	xfcePanelFile   = "etc/xdg/xfce4/panel/default.xml"
)

// trimXfceDefaults applies DE_SESSION_DROP and DE_PANEL_DROP; it returns
// what it did, for the log.
func trimXfceDefaults(root string, de *conf.Desktop) string {
	var did []string
	edit := func(rel string, fn func(string) (string, int)) {
		p := filepath.Join(root, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		out, n := fn(string(b))
		if n > 0 && os.WriteFile(p, []byte(out), 0o644) == nil {
			did = append(did, fmt.Sprintf("%s: %d dropped", filepath.Base(rel), n))
		}
	}
	if len(de.SessionDrop) > 0 {
		edit(xfceSessionFile, func(s string) (string, int) { return dropSessionClients(s, de.SessionDrop) })
	}
	if len(de.PanelDrop) > 0 {
		edit(xfcePanelFile, func(s string) (string, int) { return dropPanelPlugins(s, de.PanelDrop) })
	}
	return strings.Join(did, ", ")
}

var (
	rePluginIDs  = regexp.MustCompile(`(?s)<property name="plugin-ids" type="array">.*?</property>`)
	reClientCmd  = regexp.MustCompile(`(?s)\s*<property name="Client(\d+)_Command" type="array">\s*<value type="string" value="([^"]+)"/>.*?</property>`)
	reClientKey  = regexp.MustCompile(`Client(\d+)_`)
	reCount      = regexp.MustCompile(`(<property name="Count" type="int" value=")(\d+)("/>)`)
	rePluginType = regexp.MustCompile(`(?s)\s*<property name="plugin-(\d+)" type="string" value="([^"]+)"(/>|>.*?</property>)`)
)

// dropSessionClients removes the failsafe sessions' clients whose command
// is one of cmds, renumbering the ones after and lowering Count.
func dropSessionClients(s string, cmds []string) (string, int) {
	drop := map[string]bool{}
	for _, c := range cmds {
		drop[c] = true
	}
	// Each failsafe session runs from its IsFailsafe to the next one.
	const mark = `<property name="IsFailsafe"`
	parts := strings.Split(s, mark)
	total := 0
	for i := 1; i < len(parts); i++ {
		var n int
		parts[i], n = dropClients(parts[i], drop)
		total += n
	}
	return strings.Join(parts, mark), total
}

// dropClients does dropSessionClients for one session's text.
func dropClients(blk string, drop map[string]bool) (string, int) {
	total := 0
	for {
		gone := -1
		for _, m := range reClientCmd.FindAllStringSubmatchIndex(blk, -1) {
			if drop[blk[m[4]:m[5]]] {
				gone, _ = strconv.Atoi(blk[m[2]:m[3]])
				break
			}
		}
		if gone < 0 {
			return blk, total
		}
		total++
		// The client's three properties, then the later ones down by one.
		re := regexp.MustCompile(`(?s)\s*<property name="Client` + strconv.Itoa(gone) + `_Command" type="array">.*?</property>|\s*<property name="Client` + strconv.Itoa(gone) + `_[A-Za-z]+" type="[a-z]+" value="[^"]*"/>`)
		blk = re.ReplaceAllString(blk, "")
		blk = reClientKey.ReplaceAllStringFunc(blk, func(k string) string {
			n, _ := strconv.Atoi(k[6 : len(k)-1])
			if n > gone {
				n--
			}
			return "Client" + strconv.Itoa(n) + "_"
		})
		blk = reCount.ReplaceAllStringFunc(blk, func(c string) string {
			m := reCount.FindStringSubmatch(c)
			n, _ := strconv.Atoi(m[2])
			return m[1] + strconv.Itoa(n-1) + m[3]
		})
	}
}

// dropPanelPlugins removes the panel plugins of the given types and their
// ids from every panel's plugin-ids.
func dropPanelPlugins(s string, types []string) (string, int) {
	drop := map[string]bool{}
	for _, t := range types {
		drop[t] = true
	}
	var ids []string
	s = rePluginType.ReplaceAllStringFunc(s, func(p string) string {
		m := rePluginType.FindStringSubmatch(p)
		if !drop[m[2]] {
			return p
		}
		ids = append(ids, m[1])
		return ""
	})
	s = rePluginIDs.ReplaceAllStringFunc(s, func(a string) string {
		for _, id := range ids {
			a = regexp.MustCompile(`\s*<value type="int" value="`+id+`"/>`).ReplaceAllString(a, "")
		}
		return a
	})
	return s, len(ids)
}
