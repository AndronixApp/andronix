package app

import (
	"bufio"
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Package servers through Android's resolver, for a distro the session's
// DNS forwarder can't serve (card #398).
//
// glibc distros ask Android's resolver through the forwarder and its NSS
// module (dnsfwd.go). Alpine's musl has no NSS, and its resolv.conf takes
// addresses on port 53 only, which an Android app can't listen on, so apk
// asks just the public servers in resolv.conf (8.8.8.8, 1.1.1.1). On a
// network that doesn't answer those it gives up after 5 s per source ("DNS:
// transient error (try again later)", exit status 2 with both sources)
// while the installer and the app, on Android's resolver, work.
//
// When the package manager fails that way, pinPkgHosts looks the sources'
// hosts up through Android's resolver and writes the addresses to the
// distro's /etc/hosts (musl reads it before asking DNS), each line marked.
// They are for this run only: a CDN's addresses change. pkgOps.close
// removes them when the run ends; what a killed run left goes with the next
// run's first package command and with the next proot start (startSession)
// that finds no install or update of the distro running.
//
// pack add from Termux or the app gets the same (packOps). It holds no
// distro lock, so its first package command leaves lines it finds (an
// install's, maybe), and so does its end while an install or update holds
// the lock. A proot start in another andronix may remove pack's own in the
// middle of it: its next package command that fails the lookup writes them
// again.
//
// Only the package servers: other names inside the distro still go to
// resolv.conf's servers.

const pkgHostsMark = "# andronix: package server, from Android's DNS, for this run only"

// pkgHostsPinned holds the rootfs of every distro this process wrote the
// lines for, so its own proot runs (startSession) leave them.
var pkgHostsPinned sync.Map // rootfs -> true

// pkgHostsWanted reports whether t's distro can only use resolv.conf.
var pkgHostsWanted = func(t *proot.Target) bool {
	return pkgHostsGate(sys.OnAndroid(), sys.SystemDNS(), t.Local, nssLibDir(t.Rootfs))
}

// pkgHostsGate: on Android, with Android's own resolver linked in and in
// use (not a NO_NDK build, not ANDRONIX_DNS=go), a real distro under proot,
// and no NSS in it to ask the forwarder.
func pkgHostsGate(onAndroid, systemDNS, local bool, nssDir string) bool {
	return onAndroid && systemDNS && !local && nssDir == ""
}

// isPkgHostsLine: one of pinPkgHosts' lines (the mark ends it).
func isPkgHostsLine(l string) bool {
	return strings.HasSuffix(strings.TrimRight(l, " \t\r"), pkgHostsMark)
}

// readHosts reads the distro's hosts file for rewriting: its content, its
// permissions, and ok=false when it isn't ours to rewrite (a link out of
// the distro, a device, unreadable). A missing file is ok and empty.
func readHosts(path string) (cur string, mode os.FileMode, ok bool) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", 0o644, true
	}
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false
	}
	return string(b), st.Mode().Perm(), true
}

// hasPkgHostsLines reports whether any line of s is one of ours.
func hasPkgHostsLines(s string) bool {
	if !strings.Contains(s, pkgHostsMark) {
		return false
	}
	for _, l := range strings.Split(s, "\n") {
		if isPkgHostsLine(l) {
			return true
		}
	}
	return false
}

// apkRepoHosts lists the hosts of the distro's apk sources.
func apkRepoHosts(rootfs string) []string {
	f, err := os.Open(filepath.Join(rootfs, "etc/apk/repositories"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var hosts []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, w := range strings.Fields(sc.Text()) {
			if strings.HasPrefix(w, "#") {
				break
			}
			// "@tag URL" and plain "URL"; a local path has no host.
			u, err := url.Parse(w)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				continue
			}
			if h := strings.ToLower(u.Hostname()); validHostName(h) && !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// validHostName: a DNS name that is safe to write as a hosts-file field
// (not an IP address, no spaces or other surprises).
func validHostName(h string) bool {
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// pkgHostAddrs picks what to write for a host: its IPv4 addresses when it
// has any (a phone's IPv6 can be a black hole, see sys.DialContext), else
// its IPv6 ones; at most 4.
func pkgHostAddrs(ips []net.IPAddr) []string {
	var v4, v6 []string
	for _, ip := range ips {
		if ip.IP == nil || ip.IP.IsUnspecified() || ip.IP.IsLoopback() || ip.Zone != "" {
			continue
		}
		if ip.IP.To4() != nil {
			v4 = append(v4, ip.IP.String())
		} else {
			v6 = append(v6, ip.IP.String())
		}
	}
	out := v4
	if len(out) == 0 {
		out = v6
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

// pinPkgHosts writes the package servers' addresses, from Android's
// resolver, to the distro's /etc/hosts. It reports whether it wrote any,
// i.e. whether trying the package manager again can work: false when the
// distro has the forwarder, the lines are there already (they didn't help),
// or Android's resolver has no answer either (no network).
func (p *pkgOps) pinPkgHosts(ctx context.Context, r ui.Reporter) bool {
	if !pkgHostsWanted(p.t) {
		return false
	}
	path := filepath.Join(p.t.Rootfs, "etc/hosts")
	cur, mode, ok := readHosts(path)
	if !ok {
		return false
	}
	if hasPkgHostsLines(cur) {
		p.lg.Printf("package manager: the package servers' addresses from Android's DNS are in /etc/hosts already; the lookup failed all the same")
		return false
	}
	var lines, named []string
	for _, h := range apkRepoHosts(p.t.Rootfs) {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ips, err := lookupForGuest(lctx, h)
		cancel()
		addrs := pkgHostAddrs(ips)
		if err != nil || len(addrs) == 0 {
			p.lg.Printf("package manager: Android's DNS has no answer for %s either: %v", h, err)
			continue
		}
		for _, a := range addrs {
			lines = append(lines, a+" "+h+" "+pkgHostsMark)
		}
		named = append(named, h)
	}
	if len(lines) == 0 || ctx.Err() != nil {
		return false
	}
	next := cur
	if next != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	next += strings.Join(lines, "\n") + "\n"
	// Registered before the write: a proot run of this process must never
	// take the lines for a killed run's.
	pkgHostsPinned.Store(p.t.Rootfs, true)
	if err := sys.WriteFileAtomic(path, []byte(next), mode); err != nil {
		pkgHostsPinned.Delete(p.t.Rootfs)
		p.lg.Printf("package manager: writing the package servers' addresses to /etc/hosts: %v", err)
		return false
	}
	p.androidDNSRetry = true
	p.lg.Printf("package manager: the distro's DNS servers (resolv.conf) didn't answer and Android's did; using Android's addresses for %s during this run (/etc/hosts)", strings.Join(named, ", "))
	r.Line("The distro's DNS servers didn't answer; using Android's for the package servers.")
	return true
}

// close ends the run: the package servers' addresses leave /etc/hosts.
// Safe to call twice, and while a cancelled step is still winding down.
func (p *pkgOps) close() {
	if _, mine := pkgHostsPinned.LoadAndDelete(p.t.Rootfs); !mine {
		return
	}
	// pack add, with an install or update of the distro running (it holds
	// the lock, pack never does): that run may be using these lines, so they
	// stay. Its own end, or the next proot start after it, removes them.
	if p.shared && distroLockHeld(p.t.Rootfs) {
		p.lg.Printf("package manager: an install or update of this distro is running; leaving the package servers' addresses in /etc/hosts to it")
		return
	}
	if err := unpinPkgHosts(p.t.Rootfs); err != nil {
		p.lg.Printf("package manager: removing the package servers' addresses from /etc/hosts: %v", err)
	}
}

// dropStalePkgHosts removes the lines a killed run left in rootfs, unless
// this process is the run that wrote them. Every proot start calls it, so
// also a login or a desktop start in another session while an install or
// update of this distro is running: the lines may be that run's, and taking
// them away fails its next download. A live install or update holds the
// distro's lock (distrolock.go), so with the lock held the lines stay; the
// run that holds it removes them when it ends, and its first package command
// removes what a killed run before it left (pkgOps.prepare). The lock is
// only looked at when there are lines to remove.
func dropStalePkgHosts(rootfs string) {
	if _, mine := pkgHostsPinned.Load(rootfs); mine {
		return
	}
	if cur, _, ok := readHosts(filepath.Join(rootfs, "etc/hosts")); !ok || !hasPkgHostsLines(cur) {
		return
	}
	if distroLockHeld(rootfs) {
		return
	}
	unpinPkgHosts(rootfs)
}

// unpinPkgHosts removes pinPkgHosts' lines, keeping everything else (and
// the file's permissions). Nothing is written when there are none.
func unpinPkgHosts(rootfs string) error {
	path := filepath.Join(rootfs, "etc/hosts")
	cur, mode, ok := readHosts(path)
	if !ok || !hasPkgHostsLines(cur) {
		return nil
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(cur, "\n"), "\n") {
		if !isPkgHostsLine(l) {
			keep = append(keep, l)
		}
	}
	next := ""
	if len(keep) > 0 {
		next = strings.Join(keep, "\n") + "\n"
	}
	return sys.WriteFileAtomic(path, []byte(next), mode)
}
