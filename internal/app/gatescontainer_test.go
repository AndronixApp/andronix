//go:build container

package app

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// The 2.1.6-rc2 gate bugs 3 to 5 against a real apt and a real apk, without
// a phone: these tests run as root inside a throwaway container, whose own
// "/" is the distro (they change its sources, /etc/hosts and resolv.conf).
// Never on a machine you keep.
//
//	CGO_ENABLED=0 GOOS=linux go test -c -tags container -o /tmp/gates/app.test ./internal/app
//	docker run --rm --pull=never -v /tmp/gates:/g:ro debian:stable-slim /g/app.test -test.v -test.run TestContainerDebianBreakMirror
//	docker run --rm --pull=never -v /tmp/gates:/g:ro --network none --add-host deb.debian.org:192.0.2.5 debian:stable-slim /g/app.test -test.v -test.run TestContainerAptOffline
//	docker run --rm --pull=never -v /tmp/gates:/g:ro --privileged alpine:3.24 /g/app.test -test.v -test.run TestContainerAlpinePack
//
// Any Debian 13 image with apt 3 and any Alpine image with apk-tools 3 will
// do (run on Nov 13 2026 with debian:stable-slim, apt 3.0.3, and Alpine
// 3.24). The pauses between tries are cut to a second.

func containerOnly(t *testing.T, tool string) {
	t.Helper()
	if _, err := os.Stat("/.dockerenv"); err != nil || os.Geteuid() != 0 {
		t.Skip("only as root inside a container")
	}
	if _, err := os.Stat(tool); err != nil {
		t.Skipf("no %s here", tool)
	}
}

func shortSleep(d time.Duration) { time.Sleep(min(d, time.Second)) }

// Bug 3: Debian 13's sources as debian:stable-slim has them (two servers),
// the main one pointed at a dead server. The refresh moves it to the
// distro's own server and a package from the main archive can be installed.
func TestContainerDebianBreakMirror(t *testing.T) {
	containerOnly(t, "/usr/bin/apt-get")
	realProbe(t)
	t.Setenv("ANDRONIX_TEST_BREAK_MIRROR", "1")
	d, fam := loadDistroFam(t, "debian")
	lg, logged := bufLog()
	tg := &proot.Target{Rootfs: "/", Local: true}
	p := newPkgOps(tg, fam, d, lg)
	p.sleep = shortSleep
	err := p.update(context.Background(), nopRep{})
	t.Logf("log:\n%s", logged)
	if err != nil {
		t.Fatalf("the refresh didn't recover: %v", err)
	}
	src, _ := os.ReadFile("/etc/apt/sources.list.d/debian.sources")
	if strings.Contains(string(src), "andronix-broken") || !strings.Contains(string(src), "URIs: http://deb.debian.org/debian\n") || !strings.Contains(string(src), "URIs: http://deb.debian.org/debian-security\n") {
		t.Errorf("sources:\n%s", src)
	}
	if !strings.Contains(logged.String(), "switched the sources from http://127.0.0.1:9/andronix-broken/ to http://deb.debian.org/debian") {
		t.Error("no mirror switch in the log")
	}
	if err := tg.Run(context.Background(), strings.ReplaceAll(fam.Has, "%s", "adduser"), nil, nil); err != nil {
		t.Errorf("adduser has no installation candidate after the refresh: %v", err)
	}
}

// Bug 4: no route to the package server (docker --network none, the name in
// /etc/hosts). apt 3 ends with "does not have a Release file"; it is the
// network, with the tries a network failure gets.
func TestContainerAptOffline(t *testing.T) {
	containerOnly(t, "/usr/bin/apt-get")
	realProbe(t)
	d, fam := loadDistroFam(t, "debian")
	lg, logged := bufLog()
	p := newPkgOps(&proot.Target{Rootfs: "/", Local: true}, fam, d, lg)
	p.sleep = shortSleep
	err := p.update(context.Background(), nopRep{})
	t.Logf("log:\n%s", logged)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || !f.Kind.Network() {
		t.Fatalf("want a network failure, got %v", err)
	}
	if n := strings.Count(logged.String(), "attempt "); n != 4 {
		t.Errorf("%d tries, want 4", n)
	}
	if e := pkgErr(refreshTitle, err).(*ui.UserError); e.Title != "Couldn't reach the package servers" {
		t.Errorf("user error: %q %q", e.Title, e.What)
	}
}

// Bug 5: Alpine with resolv.conf's servers dead (as on the Redmi: 192.0.2.1
// and 192.0.2.2), while "Android's resolver" (here 1.1.1.1, asked by this
// process) answers. pack add's refresh gets the package servers' addresses
// and works; they are gone when it ends.
func TestContainerAlpinePack(t *testing.T) {
	containerOnly(t, "/sbin/apk")
	// Docker binds both files; /etc/hosts must be a plain file to rewrite.
	for _, f := range []string{"/etc/hosts", "/etc/resolv.conf"} {
		b, _ := os.ReadFile(f)
		if err := (&proot.Target{Rootfs: "/", Local: true}).Run(context.Background(), "umount "+f, nil, nil); err != nil {
			t.Skipf("can't unmount %s (run the container with --privileged): %v", f, err)
		}
		os.WriteFile(f, b, 0o644)
	}
	os.WriteFile("/etc/resolv.conf", []byte("nameserver 192.0.2.1\nnameserver 192.0.2.2\n"), 0o644)
	before, _ := os.ReadFile("/etc/hosts")
	pkgHostsWanted = func(*proot.Target) bool { return true }
	lookupForGuest = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "1.1.1.1:53")
		}}
		return r.LookupIPAddr(ctx, host)
	}
	fam, _ := pkgmgr.Get("apk")
	lg, logged := bufLog()
	pp := &packPlace{id: "alpine", t: &proot.Target{Rootfs: "/", Local: true}, fam: fam, label: "Alpine"}
	pp.ops = packOps(pp, lg)
	pp.ops.sleep = shortSleep
	err := packRefresh(context.Background(), pp, nopRep{})
	during, _ := os.ReadFile("/etc/hosts")
	pp.ops.close()
	after, _ := os.ReadFile("/etc/hosts")
	t.Logf("log:\n%s\nhosts during:\n%s", logged, during)
	if err != nil {
		t.Fatalf("the refresh didn't recover: %v", err)
	}
	if !strings.Contains(string(during), pkgHostsMark) || string(after) != string(before) {
		t.Errorf("hosts after:\n%s", after)
	}
}
