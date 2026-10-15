package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	andronix "github.com/AndronixApp/andronix-distros"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/termux"
)

// Name lookups inside a distro through Android's resolver (card #141).
// Networks that drop DNS to public servers break the distro's resolv.conf
// (8.8.8.8, 1.1.1.1) while Android's resolver works. Every session runs a
// forwarder (`andronix __dnsfwd <rootfs> <pid>`, detached) on a free
// 127.0.0.1 port, announced as <rootfs>/run/andronix/dns/<port>; glibc
// distros ask it through libnss_andronix.so.2 (guest/preload/nss.c):
//
//	hosts: files andronix [NOTFOUND=return] dns
//
// A name Android says doesn't exist returns at once; with no forwarder
// answering (none running, a stale file) the module is UNAVAIL and dns runs
// as before. The forwarder exits when the session's process (pid) is gone.
// musl distros (Alpine) have no NSS and keep resolv.conf only.

const nssLine = "hosts: files andronix [NOTFOUND=return] dns"

func init() { proot.Session = startSession }

var forwarders sync.Map // rootfs -> started, for this process

// startSession readies a session's name lookups: the NSS module and the
// nsswitch line (again on every start: package upgrades can rewrite
// nsswitch.conf), and a forwarder watching pid. Best effort, silent.
func startSession(rootfs string, pid int) {
	if os.Getenv("ANDRONIX_DNS") == "go" || !sys.IsTermux() {
		return
	}
	if err := ensureNSS(rootfs, sys.DetectArch()); err != nil {
		termux.Logf("dns: nss setup in %s: %v", rootfs, err)
	}
	// One forwarder per distro for this process (an install runs proot
	// dozens of times); one per desktop session, which outlives it.
	if pid == os.Getpid() {
		if _, started := forwarders.LoadOrStore(rootfs, true); started {
			return
		}
	}
	if err := spawnForwarder(rootfs, pid); err != nil {
		termux.Logf("dns: forwarder for %s: %v", rootfs, err)
	}
}

// spawnForwarder starts the forwarder through a short-lived parent
// (`__dnsfwd`, which starts the server and exits once its port is
// announced), so the forwarder is never a child of this process: Login
// execs proot next, and proot waiting on a child it didn't start hung the
// session on an Android 9 emulator. Bounded: 3 s at most, and the session
// goes on without a forwarder (the module is UNAVAIL, dns as before).
func spawnForwarder(rootfs string, pid int) error {
	self, err := sys.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := sys.CommandContext(ctx, self, "__dnsfwd", rootfs, strconv.Itoa(pid))
	c.Stdin, c.Stderr = nil, nil
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	c.Env = append(env, "LD_PRELOAD=", serveEnv+"=")
	out, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	if port := strings.TrimSpace(string(out)); port == "" {
		return errors.New("no port announced")
	}
	return nil
}

// serveEnv marks the forwarder itself, as opposed to the parent starting it.
const serveEnv = "ANDRONIX_DNSFWD_SERVE"

// ForwarderCommand is `andronix __dnsfwd <rootfs> <pid>`: with serveEnv set
// it serves (DNSForward); otherwise it starts the server detached (its own
// session, no output) and prints the announced port, then exits.
func ForwarderCommand(rootfs string, pid int) error {
	if os.Getenv(serveEnv) == "1" {
		return DNSForward(rootfs, pid, os.NewFile(3, "ready"))
	}
	self, err := sys.Executable()
	if err != nil {
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	c := sys.Command(self, "__dnsfwd", rootfs, strconv.Itoa(pid))
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	c.Env = append(env, serveEnv+"=1")
	c.ExtraFiles = []*os.File{w}
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = c.Start()
	w.Close()
	if err != nil {
		return err
	}
	c.Process.Release()
	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		return fmt.Errorf("%s: %w", self, err)
	}
	fmt.Print(line)
	return nil
}

// nssLibDir is where the distro's glibc keeps libnss_files.so.2 ("" for
// musl and other libcs without NSS).
func nssLibDir(rootfs string) string {
	for _, pat := range []string{"usr/lib/*-linux-gnu*/libnss_files.so.2", "usr/lib64/libnss_files.so.2", "usr/lib/libnss_files.so.2",
		"lib/*-linux-gnu*/libnss_files.so.2", "lib64/libnss_files.so.2", "lib/libnss_files.so.2"} {
		if m, _ := filepath.Glob(filepath.Join(rootfs, pat)); len(m) > 0 {
			return filepath.Dir(m[0])
		}
	}
	return ""
}

// ensureNSS installs the module next to libnss_files and points the hosts
// line at it. Idempotent; a musl distro is left alone.
func ensureNSS(rootfs string, arch sys.Arch) error {
	dir := nssLibDir(rootfs)
	if dir == "" {
		return nil
	}
	b, err := andronix.Preload.ReadFile("guest/preload/" + string(arch) + "/libnss_andronix.so.2")
	if err != nil {
		return err
	}
	lib := filepath.Join(dir, "libnss_andronix.so.2")
	if cur, err := os.ReadFile(lib); err != nil || string(cur) != string(b) {
		if err := sys.WriteFileAtomic(lib, b, 0o644); err != nil {
			return err
		}
	}
	return setNSSLine(filepath.Join(rootfs, "etc/nsswitch.conf"))
}

// setNSSLine makes the hosts line ours, keeping everything else.
func setNSSLine(path string) error {
	cur, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var out []string
	found := false
	for _, l := range strings.Split(strings.TrimRight(string(cur), "\n"), "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == "hosts:" {
			if !found {
				out = append(out, nssLine)
			}
			found = true
			continue
		}
		if l != "" || len(out) > 0 {
			out = append(out, l)
		}
	}
	if !found {
		out = append(out, nssLine)
	}
	next := strings.Join(out, "\n") + "\n"
	if next == string(cur) {
		return nil
	}
	return sys.WriteFileAtomic(path, []byte(next), 0o644)
}

// DNSForward serves lookups until pid is gone, then removes the port
// file. The port goes to ready (if not nil) once announced.
func DNSForward(rootfs string, pid int, ready *os.File) error {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	dir := filepath.Join(rootfs, "run/andronix/dns")
	os.MkdirAll(dir, 0o755)
	cleanStale(dir)
	port := l.Addr().(*net.TCPAddr).Port
	file := filepath.Join(dir, strconv.Itoa(port))
	if err := os.WriteFile(file, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		l.Close()
		return err
	}
	defer os.Remove(file)
	if ready != nil {
		fmt.Fprintln(ready, port)
		ready.Close()
	}
	born := startTime(pid)
	go func() {
		for alive(pid) && startTime(pid) == born {
			time.Sleep(time.Second)
		}
		l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			return nil // closed: the session ended
		}
		go serveLookup(c)
	}
}

// cleanStale removes port files whose forwarder process is gone (a crash);
// a live session's file stays.
func cleanStale(dir string) {
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && !forwarderAlive(p) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// forwarderAlive: pid is alive and is a forwarder. A file left by a killed
// forwarder (force-stop, Android's kill) names a pid that after a reboot is
// often some other process: alive, but not ours.
func forwarderAlive(pid int) bool {
	if !alive(pid) {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		_, e := os.Stat("/proc/self/cmdline")
		return e != nil // no /proc to tell: trust alive
	}
	return strings.Contains(string(b), "__dnsfwd")
}

// alive: pid exists and isn't a zombie (a session whose parent hasn't
// reaped it yet is over).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if st, ok := procStat(pid); ok {
		return len(st) > 0 && st[0] != "Z" && st[0] != "X"
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// startTime is pid's start time from /proc ("" without /proc), so a
// reused pid doesn't keep a forwarder running.
func startTime(pid int) string {
	if st, ok := procStat(pid); ok && len(st) > 19 {
		return st[19]
	}
	return ""
}

// procStat is /proc/<pid>/stat after the command name: state, ppid, ...
// ok is false when there is no /proc at all; a gone pid gives ok, no fields.
func procStat(pid int) ([]string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if _, e := os.Stat("/proc/self/stat"); e != nil {
			return nil, false
		}
		return nil, true
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return nil, true
	}
	return strings.Fields(string(b[i+1:])), true
}

// serveLookup answers one request: "4|6|0 name\n" -> "OK addr ...\n",
// "NOTFOUND\n" or "ERR\n".
func serveLookup(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || len(line) < 3 || line[1] != ' ' {
		return
	}
	fam, name := line[0], strings.TrimSpace(line[2:])
	fmt.Fprint(c, answerLookup(fam, name))
}

var lookupForGuest = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return sys.LookupAndroid(ctx, host)
}

// The forwarder answers within fwdWait (the module gives up after 900 ms
// and falls back to dns): a lookup Android hasn't finished by then goes
// on in the background and is cached for the next asker (apt and
// browsers retry).
const fwdWait = 700 * time.Millisecond

type lookupEntry struct {
	done chan struct{}
	ips  []net.IPAddr
	err  error
	at   time.Time
}

var lookups sync.Map // name -> *lookupEntry

func cachedLookup(name string) (ips []net.IPAddr, err error, ready bool) {
	fresh := &lookupEntry{done: make(chan struct{})}
	v, loaded := lookups.LoadOrStore(name, fresh)
	e := v.(*lookupEntry)
	if loaded {
		select {
		case <-e.done:
			ttl := time.Minute
			if e.err != nil {
				ttl = 5 * time.Second
			}
			if time.Since(e.at) > ttl {
				lookups.CompareAndDelete(name, e)
				return cachedLookup(name)
			}
		default:
		}
	}
	if !loaded {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			e.ips, e.err = lookupForGuest(ctx, name)
			cancel()
			e.at = time.Now()
			close(e.done)
		}()
	}
	select {
	case <-e.done:
		return e.ips, e.err, true
	case <-time.After(fwdWait):
		return nil, nil, false
	}
}

func answerLookup(fam byte, name string) string {
	if name == "" || len(name) > 253 || strings.ContainsAny(name, " \t/") {
		return "NOTFOUND\n"
	}
	ips, err, ready := cachedLookup(name)
	if !ready {
		return "ERR\n" // still asking Android: dns now, the cache next time
	}
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return "NOTFOUND\n"
		}
		return "ERR\n"
	}
	var out []string
	for _, ip := range ips {
		v4 := ip.IP.To4() != nil
		if fam == '4' && !v4 || fam == '6' && v4 {
			continue
		}
		out = append(out, ip.IP.String())
	}
	if len(out) == 0 {
		return "NOTFOUND\n"
	}
	return "OK " + strings.Join(out, " ") + "\n"
}
