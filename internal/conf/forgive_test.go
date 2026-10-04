package conf

import "testing"

func TestForgiveDesktop(t *testing.T) {
	for in, want := range map[string]string{"xfce": "xfce", "XFCE4": "xfce", "xfc": "xfce", "xf": "xfce", "xcfe": "xfce", "xfec": "xfce",
		"plasma": "kde", "kd": "kde", "kdee": "kde", "lxq": "lxqt", "lxqtt": "lxqt", "mat": "mate", "mte": "mate", "lxde": "lxqt", "none": "none", "cli": "none"} {
		de, _, err := ForgiveDesktop(in)
		if err != nil || de.ID != want {
			t.Errorf("%q: %v %v, want %s", in, de, err, want)
		}
	}
	for _, in := range []string{"gnome", "cinnamon", "x", "qwerty", "nosuch", ""} {
		if de, _, err := ForgiveDesktop(in); err == nil && in != "" {
			t.Errorf("%q: got %s", in, de.ID)
		}
	}
	if _, _, err := ForgiveDesktop("gnome"); err == nil || err.Error() != "GNOME isn't offered: it doesn't run well under proot on a phone" {
		t.Errorf("gnome: %v", err)
	}
}

func TestForgiveDistro(t *testing.T) {
	for in, want := range map[string]string{"debian": "debian", "debia": "debian", "debain": "debian", "deb": "debian", "ubu": "ubuntu",
		"ubunto": "ubuntu", "ubuntu24": "ubuntu24", "kalli": "kali", "fedra": "fedora", "arc": "arch", "manjar": "manjaro", "alpin": "alpine", "vod": "void"} {
		d, _, err := ForgiveDistro(in)
		if err != nil || d.ID != want {
			t.Errorf("%q: %v %v, want %s", in, d, err, want)
		}
	}
	for _, in := range []string{"windows", "x", "centos"} {
		if d, _, err := ForgiveDistro(in); err == nil {
			t.Errorf("%q: got %s", in, d.ID)
		}
	}
}
