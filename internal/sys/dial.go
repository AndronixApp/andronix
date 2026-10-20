package sys

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Lookups on Android try Android's resolver first, then Go's (#141). Go's
// resolver (FixDNS) asks public servers (Termux's resolv.conf, 8.8.8.8,
// 1.1.1.1) over UDP 53 itself; networks that block outbound DNS to public
// servers then fail every lookup, while curl and the Andronix app work
// through Android's resolver (netd: the network's DNS, or Private DNS).
// Android's resolver needs no resolv.conf, so the problem FixDNS fixed
// (static builds asked 127.0.0.1:53) can't come back through it.

// DNSLog, when set, gets one line per host: which resolver answered.
var DNSLog func(format string, args ...any)

var (
	systemResolver = &net.Resolver{} // cgo: getaddrinfo, not the Go resolver
	goResolver     *net.Resolver     // FixDNS's
	logged         sync.Map
	// for tests
	lookupSystem = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return systemResolver.LookupIPAddr(ctx, host)
	}
	lookupGo = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		r := goResolver
		if r == nil { // before FixDNS (tests)
			r = net.DefaultResolver
		}
		return r.LookupIPAddr(ctx, host)
	}
	// ANDRONIX_DNS=go skips Android's resolver (tests and comparisons:
	// the pre-2.0.9 behaviour).
	useSystemDNS = haveSystemDNS && os.Getenv("ANDRONIX_DNS") != "go"
)

// LookupIP resolves host: Android's resolver (10 s), then Go's. how is
// "android", "go" or "go after android failed: <err>".
func LookupIP(ctx context.Context, host string) (ips []net.IPAddr, how string, err error) {
	if useSystemDNS {
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ips, err = lookupSystem(sctx, host)
		cancel()
		if err == nil && len(ips) > 0 {
			return ips, "android", nil
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		serr := err
		if serr == nil {
			serr = fmt.Errorf("no addresses")
		}
		ips, err = lookupGo(ctx, host)
		if err != nil {
			return nil, "", fmt.Errorf("lookup %s: Android's resolver: %v; public DNS: %w", host, serr, err)
		}
		return ips, "go after android failed: " + shortErr(serr), nil
	}
	ips, err = lookupGo(ctx, host)
	return ips, "go", err
}

// LookupAndroid is Android's resolver only (Go's where it isn't linked
// in), for the distro's forwarder: the guest's own resolv.conf is the
// public-DNS fallback already.
func LookupAndroid(ctx context.Context, host string) ([]net.IPAddr, error) {
	if haveSystemDNS {
		return lookupSystem(ctx, host)
	}
	return lookupGo(ctx, host)
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// dialFallbackDelay staggers the start of each address's dial so a
// black-holed address (a phone's dead IPv6) does not burn the whole timeout
// before the next is tried (happy-eyeballs, RFC 8305).
const dialFallbackDelay = 300 * time.Millisecond

// DialContext is net.Dialer.DialContext with LookupIP's resolution, for
// every HTTP client: IPv4 addresses first (a phone's IPv6 can be a black
// hole), raced with a short fallback delay. When Android's resolver alone
// answered and every address failed, Go's resolver (public DNS) is asked
// too: on a v6-only carrier Android can hand back AAAA-only, leaving no A
// record to escape an IPv6 black hole (#243, report AX-G6WNB0: the tarball
// HEAD failed "dial tcp [IPv6]:443: network is unreachable" with no IPv4
// tried, then the install fell back to the registry and hit a 401).
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || net.ParseIP(host) != nil || !strings.HasPrefix(network, "tcp") {
		return d.DialContext(ctx, network, addr)
	}
	ips, how, err := LookupIP(ctx, host)
	if _, seen := logged.LoadOrStore(host, true); !seen && DNSLog != nil {
		if err != nil {
			DNSLog("dns %s: %v", host, err)
		} else {
			DNSLog("dns %s: %s (%d addresses)", host, how, len(ips))
		}
	}
	if err != nil {
		return nil, err
	}
	c, lastErr := dialAddrs(ctx, d, network, host, port, ips)
	if c != nil {
		return c, nil
	}
	// Every resolved address failed to connect. If only Android's resolver
	// answered (its addresses can be IPv6-only on a v6 network), ask Go's
	// resolver and dial any address it adds — usually the IPv4 that reaches
	// the host when IPv6 is a black hole.
	if how == "android" && ctx.Err() == nil {
		if gips, gerr := lookupGo(ctx, host); gerr == nil {
			if extra := newAddrs(gips, ips); len(extra) > 0 {
				if DNSLog != nil {
					DNSLog("dns %s: go fallback added %d address(es) after android-only dial failed", host, len(extra))
				}
				if c, e := dialAddrs(ctx, d, network, host, port, extra); c != nil {
					return c, nil
				} else if e != nil {
					lastErr = e
				}
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable address for %s", host)
	}
	return nil, lastErr
}

// dialAddrs dials ips (filtered to network's family, IPv4 first), staggering
// each start by dialFallbackDelay. The first connection wins and the rest
// are cancelled. Returns (nil, lastErr) if none connected, or (nil, nil)
// when ips held no address for this family (the caller decides).
func dialAddrs(ctx context.Context, d *net.Dialer, network, host, port string, ips []net.IPAddr) (net.Conn, error) {
	addrs := make([]string, 0, len(ips))
	v4first := make([]net.IPAddr, len(ips))
	copy(v4first, ips)
	sort.SliceStable(v4first, func(i, j int) bool { return v4first[i].IP.To4() != nil && v4first[j].IP.To4() == nil })
	for _, ip := range v4first {
		if network == "tcp4" && ip.IP.To4() == nil || network == "tcp6" && ip.IP.To4() != nil {
			continue
		}
		addrs = append(addrs, net.JoinHostPort(ip.IP.String(), port))
	}
	if len(addrs) == 0 {
		return nil, nil
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		c   net.Conn
		err error
	}
	res := make(chan result, len(addrs)) // buffered: losers never block on send
	timer := time.NewTimer(0)
	defer timer.Stop()
	var launched, finished int
	var lastErr error
	for finished < len(addrs) {
		if launched < len(addrs) {
			select {
			case <-cctx.Done():
				if lastErr == nil {
					lastErr = cctx.Err()
				}
				return nil, lastErr
			case <-timer.C:
				a := addrs[launched]
				launched++
				go func(a string) { c, err := d.DialContext(cctx, network, a); res <- result{c, err} }(a)
				timer.Reset(dialFallbackDelay)
				continue
			case r := <-res:
				finished++
				if r.c != nil {
					return r.c, nil
				}
				lastErr = r.err
			}
		} else {
			select {
			case <-cctx.Done():
				if lastErr == nil {
					lastErr = cctx.Err()
				}
				return nil, lastErr
			case r := <-res:
				finished++
				if r.c != nil {
					return r.c, nil
				}
				lastErr = r.err
			}
		}
	}
	return nil, lastErr
}

// newAddrs returns the addresses in b that are not already in a (by IP).
func newAddrs(b, a []net.IPAddr) []net.IPAddr {
	seen := make(map[string]bool, len(a))
	for _, ip := range a {
		seen[ip.IP.String()] = true
	}
	var out []net.IPAddr
	for _, ip := range b {
		if !seen[ip.IP.String()] {
			out = append(out, ip)
		}
	}
	return out
}
