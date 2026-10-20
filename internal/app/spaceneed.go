package app

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// spaceNeedDEs is the order the desktop picker shows: the real desktops
// first, then "none" (the command line, no desktop).
var spaceNeedDEs = []string{"xfce", "lxqt", "mate", "kde", "none"}

// spaceNeedResult is one distro's space picture: the same numbers
// "Checking your phone" uses at install time (spaceNeed, FreeMB(Home)), so
// the app's desktop picker greys out what won't fit from one source of
// truth. Read-only, no network.
type spaceNeedResult struct {
	Distro     string           `json:"distro"`
	FreeMB     int64            `json:"free_mb"` // -1 when it can't be read
	HaveRootfs bool             `json:"have_rootfs"`
	Desktops   []spaceNeedEntry `json:"desktops"`
}

type spaceNeedEntry struct {
	DE     string `json:"de"`
	Name   string `json:"name"`
	NeedMB int64  `json:"need_mb"`
	Fits   bool   `json:"fits"` // true when free can't be read (don't block)
}

// spaceNeedReport computes the space picture for d: free space now,
// whether the rootfs is already there, and the need for each desktop (all,
// or just deFilter when set). The need and free numbers are exactly what
// install's "Checking your phone" uses (spaceNeed, FreeMB(Home)).
func spaceNeedReport(d *conf.Distro, deFilter string) spaceNeedResult {
	in := Open(d)
	haveRootfs := false
	switch in.Get("STAGE") {
	case "configured", "desktop", "done":
		if _, err := os.Stat(in.Rootfs); err == nil {
			haveRootfs = true
		}
	}
	free := sys.FreeMB(sys.Home())

	des := spaceNeedDEs
	if deFilter != "" {
		des = []string{deFilter}
	}
	res := spaceNeedResult{Distro: d.ID, FreeMB: free, HaveRootfs: haveRootfs}
	for _, deID := range des {
		de, err := conf.ResolveDesktop(deID)
		if err != nil {
			continue
		}
		need := spaceNeed(d, de, haveRootfs)
		res.Desktops = append(res.Desktops, spaceNeedEntry{
			DE: de.ID, Name: de.Name, NeedMB: need, Fits: free < 0 || free >= need})
	}
	return res
}

// SpaceNeedCmd answers `andronix space-need <distro> [--de <de>] [--json]`:
// how much free space an install of <distro> needs for each desktop, the
// free space now, and whether the rootfs is already there (adding a
// desktop to an installed distro needs only the desktop's part). The app
// calls this when the picker opens. deFilter limits to one desktop ("" =
// all); jsonOut prints one JSON object instead of the table.
func SpaceNeedCmd(id, deFilter string, jsonOut bool) error {
	if id == "" {
		return ui.Errorf("which_distro", "Which distro?", "space-need needs a distro id.", "e.g. andronix space-need debian")
	}
	d, err := conf.LoadDistro(id)
	if err != nil {
		return ui.Errorf("unknown_distro", "Unknown distro", id+" isn't a distro id.", "List them with: andronix list")
	}
	if deFilter != "" {
		if _, err := conf.ResolveDesktop(deFilter); err != nil {
			return ui.Errorf("unknown_desktop", "Unknown desktop", deFilter+" isn't a desktop id.", "e.g. xfce, lxqt, mate, kde, none")
		}
	}
	res := spaceNeedReport(d, deFilter)

	if jsonOut {
		b, err := json.Marshal(res)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Print(ui.Banner(""))
	ui.Section(d.Label())
	if res.FreeMB < 0 {
		ui.Row("Free", "unknown", 10)
	} else {
		ui.Row("Free", fmt.Sprintf("%d MB", res.FreeMB), 10)
	}
	if res.HaveRootfs {
		ui.Note("The rootfs is already installed; these are the desktop's part only.")
	}
	for _, e := range res.Desktops {
		mark := ui.GOK
		if !e.Fits {
			mark = ui.GWarn
		}
		ui.Row(mark+" "+e.DE, fmt.Sprintf("%s %s needs %d MB", e.Name, ui.GSep, e.NeedMB), 10)
	}
	fmt.Println()
	return nil
}
