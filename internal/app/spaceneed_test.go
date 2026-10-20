package app

import (
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

func TestSpaceNeedReport(t *testing.T) {
	d, err := conf.LoadDistro("kali")
	if err != nil {
		t.Fatal(err)
	}
	r := spaceNeedReport(d, "")
	if r.Distro != "kali" {
		t.Errorf("distro %q", r.Distro)
	}
	// Every desktop in the picker's order, each with a positive need that
	// matches spaceNeed (one source of truth).
	if len(r.Desktops) != len(spaceNeedDEs) {
		t.Fatalf("got %d desktops, want %d", len(r.Desktops), len(spaceNeedDEs))
	}
	for i, e := range r.Desktops {
		if e.DE != spaceNeedDEs[i] {
			t.Errorf("desktop %d is %q, want %q", i, e.DE, spaceNeedDEs[i])
		}
		de, _ := conf.ResolveDesktop(e.DE)
		if e.NeedMB != spaceNeed(d, de, r.HaveRootfs) {
			t.Errorf("%s need %d != spaceNeed", e.DE, e.NeedMB)
		}
		if e.NeedMB <= 0 {
			t.Errorf("%s need %d", e.DE, e.NeedMB)
		}
	}
	// A desktop needs more than the command line.
	var none, kde int64
	for _, e := range r.Desktops {
		switch e.DE {
		case "none":
			none = e.NeedMB
		case "kde":
			kde = e.NeedMB
		}
	}
	if kde <= none {
		t.Errorf("kde %d not above none %d", kde, none)
	}
	// Filter to one desktop.
	if one := spaceNeedReport(d, "xfce"); len(one.Desktops) != 1 || one.Desktops[0].DE != "xfce" {
		t.Errorf("filter xfce: %+v", one.Desktops)
	}
}
