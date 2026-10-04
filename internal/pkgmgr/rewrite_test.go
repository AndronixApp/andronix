package pkgmgr

import (
	"strings"
	"testing"
)

func TestReplaceBase(t *testing.T) {
	src := "URIs: http://deb.debian.org/debian\nSuites: trixie\n\nURIs: http://deb.debian.org/debian-security\nSuites: trixie-security\n"
	out, n := replaceBase(src, "http://deb.debian.org/debian", "http://ftp.debian.org/debian")
	if n != 1 || !strings.Contains(out, "http://ftp.debian.org/debian\n") || !strings.Contains(out, "http://deb.debian.org/debian-security") {
		t.Errorf("%d:\n%s", n, out)
	}
}
