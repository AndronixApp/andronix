package rootfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstallSelfSkipsTheSameBuild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the linux build installs itself; others need the embedded copy")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "usr/local/bin"), 0o755) // as in every rootfs
	if err := InstallSelf(root); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "usr/local/bin/andronix")
	st1, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, selfStampFile)); selfStamp() == "" || string(b) != installedStamp(selfStamp(), dst) {
		t.Fatalf("stamp %q, want %q", b, installedStamp(selfStamp(), dst))
	}
	time.Sleep(20 * time.Millisecond)
	if err := InstallSelf(root); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(dst); !st2.ModTime().Equal(st1.ModTime()) {
		t.Fatal("the same build was written again")
	}
	// A different stamp (another build) writes it again.
	os.WriteFile(filepath.Join(root, selfStampFile), []byte("gz old"), 0o644)
	time.Sleep(20 * time.Millisecond)
	if err := InstallSelf(root); err != nil {
		t.Fatal(err)
	}
	st3, _ := os.Stat(dst)
	if st3.ModTime().Equal(st1.ModTime()) {
		t.Fatal("another build's stamp didn't rewrite it")
	}
	// Something else rewrote the binary (an older installer): written again.
	os.WriteFile(dst, []byte("old andronix"), 0o755)
	time.Sleep(20 * time.Millisecond)
	if err := InstallSelf(root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) == "old andronix" {
		t.Fatal("a copy written by another installer was kept")
	}
}

func TestEnsureSysdata(t *testing.T) {
	d := t.TempDir()
	EnsureSysdata(d)
	if b, err := os.ReadFile(filepath.Join(d, "filesystems")); err != nil || !strings.Contains(string(b), "\text4") {
		t.Fatalf("filesystems stand-in: %q %v", b, err)
	}
	os.WriteFile(filepath.Join(d, "filesystems"), []byte("custom"), 0o644)
	EnsureSysdata(d)
	if b, _ := os.ReadFile(filepath.Join(d, "filesystems")); string(b) != "custom" {
		t.Error("an existing stand-in was overwritten")
	}
	EnsureSysdata(filepath.Join(d, "missing")) // no sysdata dir: nothing made
	if _, err := os.Stat(filepath.Join(d, "missing")); err == nil {
		t.Error("made a sysdata dir")
	}
}
