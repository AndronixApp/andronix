package app

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// The XFCE wallpaper defaults must parse (xfconfd drops a broken channel)
// and cover Termux:X11's portrait "builtin" monitor without zooming.
func TestXFCEWallpaperDefaults(t *testing.T) {
	root := t.TempDir()
	if err := setWallpaper(context.Background(), root, "xfce", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-desktop.xml"))
	if err != nil {
		t.Fatal(err)
	}
	type prop struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
		Props []prop `xml:"property"`
	}
	var ch struct {
		Props []prop `xml:"property"`
	}
	if err := xml.Unmarshal(b, &ch); err != nil {
		t.Fatalf("not valid XML: %v", err)
	}
	styles := map[string]string{}
	for _, mon := range ch.Props[0].Props[0].Props { // backdrop/screen0/monitor*
		for _, p := range mon.Props[0].Props { // workspace0/*
			if p.Name == "image-style" {
				styles[mon.Name] = p.Value
			}
		}
	}
	if styles["monitorbuiltin"] != "4" || styles["monitorVNC-0"] != "5" {
		t.Fatalf("image-style per monitor: %v", styles)
	}
	if !strings.Contains(string(b), `name="rgba1"`) {
		t.Fatal("no background colour for the letterboxed monitor")
	}
}

// syncSkel fills in what useradd -m missed (nested files), without
// touching files the home already has.
func TestSyncSkel(t *testing.T) {
	skel, home := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(skel, ".config/tigervnc"), 0o700)
	os.WriteFile(filepath.Join(skel, ".config/tigervnc/xstartup"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(skel, ".bashrc"), []byte("skel\n"), 0o644)
	os.WriteFile(filepath.Join(home, ".bashrc"), []byte("mine\n"), 0o644)
	os.MkdirAll(filepath.Join(home, ".config"), 0o700) // what useradd left
	if n := syncSkel(skel, home, os.Getuid(), os.Getgid()); n != 1 {
		t.Fatalf("copied %d files, want 1", n)
	}
	st, err := os.Stat(filepath.Join(home, ".config/tigervnc/xstartup"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("xstartup: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".bashrc")); string(b) != "mine\n" {
		t.Fatal("overwrote an existing file")
	}
}

// pcmanfm-qt reads only the first pcmanfm-qt/lxqt dir in XDG_CONFIG_DIRS,
// so the light layer must carry LXQt's wallpaper defaults along with its
// own key (it held only ShowThumbnails, and the desktop came up black).
func TestLightLayerKeepsLXQtWallpaper(t *testing.T) {
	root := t.TempDir()
	de, _ := conf.ResolveDesktop("lxqt")
	if err := setWallpaper(context.Background(), root, "lxqt", nil); err != nil {
		t.Fatal(err)
	}
	if err := ApplyProfile(root, de, "light"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, lightDir, "pcmanfm-qt/lxqt/settings.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"Wallpaper=" + WallpaperPath, "BgColor=#15110e", "ShowThumbnails=false"} {
		if !strings.Contains(s, want) {
			t.Errorf("light settings.conf lacks %q:\n%s", want, s)
		}
	}
}

// andronix update repairs the settings pcmanfm-qt saved while the light
// layer hid the defaults (no wallpaper on black), and leaves a user's own
// wallpaper alone.
func TestRefreshWallpaperRepairsBlackLXQt(t *testing.T) {
	root := t.TempDir()
	de, _ := conf.ResolveDesktop("lxqt")
	os.MkdirAll(filepath.Join(root, filepath.Dir(WallpaperPath)), 0o755)
	os.WriteFile(filepath.Join(root, WallpaperPath), []byte("png"), 0o644)
	cases := map[string][2]string{ // home: before, want in after
		"home/black":   {"[Desktop]\nWallpaper=\nBgColor=#000000\nShowHidden=false\n", "Wallpaper=" + WallpaperPath},
		"home/missing": {"[Behavior]\nSingleClick=false\n", "Wallpaper=" + WallpaperPath},
		"home/own":     {"[Desktop]\nWallpaper=/home/own/cat.png\nBgColor=#000000\n", "Wallpaper=/home/own/cat.png"},
		"root":         {"[Desktop]\nWallpaper=\nBgColor=#336699\n", "BgColor=#336699"},
	}
	for h, c := range cases {
		p := filepath.Join(root, h, ".config/pcmanfm-qt/lxqt/settings.conf")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(c[0]), 0o644)
	}
	refreshWallpaper(context.Background(), root, de)
	for h, c := range cases {
		b, _ := os.ReadFile(filepath.Join(root, h, ".config/pcmanfm-qt/lxqt/settings.conf"))
		if !strings.Contains(string(b), c[1]+"\n") {
			t.Errorf("%s: want %q in\n%s", h, c[1], b)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "home/black/.config/pcmanfm-qt/lxqt/settings.conf")); !strings.Contains(string(b), "ShowHidden=false") {
		t.Errorf("other keys lost:\n%s", b)
	}
}

// Under the light profile xstartup sets XDG_CONFIG_DIRS, and startlxqt
// then no longer adds /usr/share (LXQt's default lxqt.conf and panel.conf
// on Debian; the panel drew solid black), so LXQt's own default dirs follow
// our layer.
func TestXstartupKeepsSessionConfigDirs(t *testing.T) {
	for id, want := range map[string]string{
		"lxqt": `"/etc/xdg/andronix-light:${XDG_CONFIG_DIRS:-/etc:/etc/xdg:/usr/share}"`,
		"xfce": `"/etc/xdg/andronix-light:${XDG_CONFIG_DIRS:-/etc/xdg}"`,
	} {
		de, _ := conf.ResolveDesktop(id)
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "root/.config/tigervnc"), 0o755)
		writeXstartup(root, de)
		b, err := os.ReadFile(filepath.Join(root, "root/.config/tigervnc/xstartup"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), want) || strings.Contains(string(b), "@XDGDIRS@") {
			t.Errorf("%s: want %s in xstartup", id, want)
		}
	}
}

// A profile file emptied by a kill mid-write counts as unset (the install
// picks light again on a small phone) and reads as balanced meanwhile.
func TestProfileEmptyFile(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc/andronix"), 0o755)
	os.WriteFile(filepath.Join(root, profileFile), nil, 0o600)
	if profileSet(root) || Profile(root) != "balanced" {
		t.Error("empty profile: want unset and balanced")
	}
	os.WriteFile(filepath.Join(root, profileFile), []byte("li\x00"), 0o600)
	if profileSet(root) {
		t.Error("garbled profile: want unset")
	}
	if err := ApplyProfile(root, nil, "light"); err != nil || !profileSet(root) || Profile(root) != "light" {
		t.Errorf("after ApplyProfile: %v", err)
	}
}
