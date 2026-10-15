package rootfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuietSudo(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("x"), 0o755)
	}
	ln := func(target, p string) {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.Remove(filepath.Join(root, p))
		os.Symlink(target, filepath.Join(root, p))
	}
	f := filepath.Join(root, quietSudoFile)

	QuietSudo(root) // no sudoers.d, no sudo
	if _, err := os.Stat(f); err == nil {
		t.Fatal("written without sudo")
	}
	os.MkdirAll(filepath.Join(root, "etc/sudoers.d"), 0o755)
	mk("usr/bin/sudo.ws")
	ln("/etc/alternatives/sudo", "usr/bin/sudo") // Ubuntu 26.04
	ln("/usr/bin/sudo.ws", "etc/alternatives/sudo")
	QuietSudo(root)
	if b, _ := os.ReadFile(f); string(b) != quietSudo {
		t.Fatalf("classic sudo via alternatives: %q", b)
	}
	mk("usr/lib/cargo/bin/sudo")
	ln("/usr/lib/cargo/bin/sudo", "etc/alternatives/sudo") // switched to sudo-rs
	QuietSudo(root)
	if _, err := os.Stat(f); err == nil {
		t.Fatal("kept with sudo-rs, which refuses log_allowed")
	}
	os.Remove(filepath.Join(root, "usr/bin/sudo"))
	mk("usr/bin/sudo") // Debian: a plain file
	QuietSudo(root)
	if _, err := os.Stat(f); err != nil {
		t.Fatal("not written for a plain sudo")
	}
}
