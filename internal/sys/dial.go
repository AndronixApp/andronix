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

// DialContext is net.Dialer.DialContext with LookupIP's resolution, for
// every HTTP client: IPv4 addresses first (a phone's IPv6 can be a black
// hole), each tried in turn.
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
	sort.SliceStable(ips, func(i, j int) bool { return ips[i].IP.To4() != nil && ips[j].IP.To4() == nil })
	var lastErr error
	for _, ip := range ips {
		if network == "tcp4" && ip.IP.To4() == nil || network == "tcp6" && ip.IP.To4() != nil {
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable address for %s", host)
	}
	return nil, lastErr
}
