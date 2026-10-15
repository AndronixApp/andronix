package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A desktop added to a command-line install: the user made earlier gets
// the session files from /etc/skel, and nothing of theirs is overwritten
// (support #284).
func TestUserSessionFiles(t *testing.T) {
	root := t.TempDir()
	w := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o755)
	}
	w("etc/passwd", "root:x:0:0:root:/root:/bin/sh\nalex:x:1000:1000::/home/alex:/bin/bash\n")
	w("etc/andronix/user", "alex\n")
	w("etc/skel/.config/tigervnc/xstartup", "new session")
	w("etc/skel/.bashrc", "skel bashrc")
	w("home/alex/.bashrc", "my bashrc")
	in := &Inst{Rootfs: root}
	if n := userSessionFiles(in); n != 1 {
		t.Errorf("copied %d, want 1 (xstartup only)", n)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "home/alex/.config/tigervnc/xstartup")); string(b) != "new session" {
		t.Errorf("xstartup: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "home/alex/.bashrc")); string(b) != "my bashrc" {
		t.Errorf(".bashrc overwritten: %q", b)
	}
	if n := userSessionFiles(in); n != 0 {
		t.Errorf("second run copied %d", n)
	}
	os.Remove(filepath.Join(root, "etc/andronix/user")) // root only: nothing to do
	if n := userSessionFiles(in); n != 0 {
		t.Errorf("no user: copied %d", n)
	}
}
