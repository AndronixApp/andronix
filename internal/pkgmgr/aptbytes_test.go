package pkgmgr

import "testing"

// Lines as apt prints them without a TTY (Ubuntu 26.04 and Debian 13 logs).
func TestAptBytes(t *testing.T) {
	for _, c := range []struct {
		line string
		need bool
		want int64
	}{
		{"Need to get 4169 kB of archives.", true, 4169000},
		{"Need to get 26.8 MB/30.1 MB of archives.", true, 26800000},
		{"Need to get 1,234 kB of archives.", true, 1234000},
		{"Get:1 http://ports.ubuntu.com/ubuntu-ports resolute-updates/main arm64 openssl-provider-legacy arm64 3.5.5-1ubuntu3.7 [38.4 kB]", false, 38400},
		{"Get:12 http://deb.debian.org/debian trixie/main arm64 libgtk-3-0t64 arm64 3.24.49-3 [2769 kB]", false, 2769000},
		{"Get:7 http://ports.ubuntu.com/ubuntu-ports resolute/universe arm64 Packages [19.9 MB]", false, 19900000},
		{"Get:3 http://ports.ubuntu.com/ubuntu-ports resolute-backports/universe arm64 Packages [3309 B]", false, 3309},
	} {
		f := AptGotBytes
		if c.need {
			f = AptNeedBytes
		}
		if got, ok := f(c.line); !ok || got != c.want {
			t.Errorf("%q: %d %v, want %d", c.line, got, ok, c.want)
		}
	}
	for _, l := range []string{"Get:1 http://x resolute InRelease", "Hit:1 http://x resolute InRelease", "Fetched 26.8 MB in 9s (3135 kB/s)", "Unpacking rust-coreutils (0.10.0) ..."} {
		if _, ok := AptGotBytes(l); ok {
			t.Errorf("%q counted as a download", l)
		}
		if _, ok := AptNeedBytes(l); ok {
			t.Errorf("%q counted as the total", l)
		}
	}
}
