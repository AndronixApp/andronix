package conf

import (
	"fmt"
	"sort"
	"strings"
)

// Typed names are forgiven (2.0.3): installs failed on "--de xfc", "xf",
// "xfcexport" and misspelt distros. A name that isn't a distro or desktop
// becomes one when it's a unique prefix ("xf", "deb") or one typo away
// ("debain", "xcfe"; two for longer names), and the caller says so.

// NotOffered are desktops people ask for that Andronix doesn't install.
var NotOffered = map[string]string{"gnome": "GNOME", "gnome-shell": "GNOME", "cinnamon": "Cinnamon", "budgie": "Budgie",
	"unity": "Unity", "deepin": "Deepin", "dde": "Deepin", "pantheon": "Pantheon", "cosmic": "COSMIC", "sway": "Sway",
	"hyprland": "Hyprland", "lxde": "LXDE", "kde4": "KDE 4", "fluxbox": "Fluxbox", "icewm": "IceWM"}

// ForgiveDistro resolves a distro name, forgiving a prefix or a typo.
// guess reports that the name was corrected.
func ForgiveDistro(name string) (d *Distro, guess bool, err error) {
	if d, err = ResolveDistro(name); err == nil {
		return d, false, nil
	}
	names := map[string]string{}
	for _, id := range DistroIDs() {
		if dd, derr := LoadDistro(id); derr == nil {
			names[id] = id
			for _, a := range dd.Aliases {
				names[strings.ToLower(a)] = id
			}
		}
	}
	if id, ok := closest(name, names); ok {
		d, err = LoadDistro(id)
		return d, err == nil, err
	}
	return nil, false, fmt.Errorf("unknown distro %q", name)
}

// ForgiveDesktop is ResolveDesktopCompat, forgiving a prefix or a typo.
// notice explains a replacement or a correction; a desktop Andronix
// doesn't offer is an error that says so.
func ForgiveDesktop(name string) (de *Desktop, notice string, err error) {
	if de, notice, err = ResolveDesktopCompat(name); err == nil {
		return de, notice, nil
	}
	want := normName(name)
	if n, ok := NotOffered[want]; ok {
		return nil, "", fmt.Errorf("%s isn't offered: it doesn't run well under proot on a phone", n)
	}
	names := map[string]string{}
	for _, id := range DesktopIDs() {
		if id == "none" {
			continue
		}
		v, verr := read("desktops", id)
		if verr != nil {
			continue
		}
		names[id] = id
		for _, a := range v.List("DE_ALIASES") {
			names[a] = id
		}
	}
	if id, ok := closest(name, names); ok {
		if de, err = ResolveDesktop(id); err == nil {
			return de, fmt.Sprintf("'%s' isn't a desktop name; installing %s.", strings.TrimSpace(name), de.Name), nil
		}
	}
	return nil, "", fmt.Errorf("unknown desktop %q", name)
}

func normName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// closest is the id whose name the input is a unique prefix of, or the
// unique nearest name within a typo or two.
func closest(input string, names map[string]string) (string, bool) {
	want := normName(input)
	if len(want) < 2 {
		return "", false
	}
	if id, ok := names[want]; ok {
		return id, true
	}
	// A prefix: one id, or one id that prefixes all the others ("ubu":
	// ubuntu, not ubuntu24).
	ids := map[string]bool{}
	for n, id := range names {
		if strings.HasPrefix(n, want) {
			ids[id] = true
		}
	}
	if id, ok := onePrefix(ids); ok {
		return id, true
	}
	// A typo.
	limit := 1
	if len(want) >= 6 {
		limit = 2
	}
	best, bestIDs := limit+1, map[string]bool{}
	for n, id := range names {
		dist := editDistance(want, n)
		if dist < best {
			best, bestIDs = dist, map[string]bool{id: true}
		} else if dist == best {
			bestIDs[id] = true
		}
	}
	if best <= limit && len(bestIDs) == 1 {
		for id := range bestIDs {
			return id, true
		}
	}
	return "", false
}

func onePrefix(ids map[string]bool) (string, bool) {
	if len(ids) == 0 {
		return "", false
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) < len(list[j]) })
	for _, id := range list[1:] {
		if !strings.HasPrefix(id, list[0]) {
			return "", false
		}
	}
	return list[0], true
}

// editDistance is the optimal string alignment distance (insertions,
// deletions, substitutions and swaps of neighbours each cost 1).
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}
