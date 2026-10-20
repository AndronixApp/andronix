package rootfs

import (
	"strings"
	"testing"
)

// No GIO volume monitors or gvfsd-fuse in a login shell (#172).
func TestProfileShGvfs(t *testing.T) {
	if !strings.Contains(profileSh, "export GIO_USE_VOLUME_MONITOR=unix GVFS_DISABLE_FUSE=1\n") {
		t.Error("profile.d: gvfs settings missing")
	}
}
