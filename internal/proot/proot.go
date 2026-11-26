// Package proot builds the proot command line for an installed distro and
// runs commands inside it. Flags follow proot-distro's lessons (see
// DESIGN.md section 6); the code is our own.
package proot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/termux"
)

// Target is an installed distro.
type Target struct {
	ID       string // distro id, exported as ANDRONIX_DISTRO
	Dir      string // ~/.andronix/distros/<id>
	Rootfs   string
	BindsDir string // legacy ~/<distro>-binds
	// Shell is the distro's login shell (exported as SHELL).
	Shell string
	// Lang is LANG inside (default C.UTF-8).
	Lang string
	// Login user; empty means root. UID/GID/Home come from the guest passwd.
	User           string
	UID, GID, Home string
	// Display is DISPLAY inside, e.g. ":0" for a Termux:X11 session.
	// Empty leaves it to the login profile (the VNC display, :1).
	Display string
	// Local runs commands directly, for andronix running inside the
	// distro (Rootfs is then "/").
	Local bool
	// ExtraEnv sets variables for this session only (secrets for
	// setup-user go here, never on a command line).
	ExtraEnv map[string]string
	// XDGDefault is the desktop session's own default XDG_CONFIG_DIRS
	// (DE_XDG_CONFIG_DIRS; "" means /etc/xdg), after the standalone layers.
	XDGDefault string
}

var (
	helpOnce sync.Once
	helpText string
)

// PulseServer is where the distro's apps play sound: Termux's PulseAudio
// over TCP, or in the standalone app the distro's own (andronix sound).
func PulseServer() string {
	if os.Getenv("ANDRONIX_HOST") == "standalone" {
		return "unix:/tmp/.andronix-pulse/native"
	}
	return "127.0.0.1"
}

// Binary is the proot to run: PROOT_BIN (or ANDRONIX_PROOT) in the
// standalone app, where it's libproot.so in nativeLibraryDir; else proot
// from PATH (Termux's).
func Binary() string {
	for _, k := range []string{"PROOT_BIN", "ANDRONIX_PROOT"} {
		if p := os.Getenv(k); p != "" {
			return p
		}
	}
	return "proot"
}

// Available reports whether there's a proot to run.
func Available() bool {
	_, err := sys.LookPath(Binary())
	return err == nil
}

// Has reports whether this proot supports flag.
func Has(flag string) bool {
	helpOnce.Do(func() {
		c := sys.Command(Binary(), "--help")
		c.Env = hostEnv()
		out, _ := c.CombinedOutput()
		helpText = string(out)
	})
	if flag == "-L" {
		for _, l := range strings.Split(helpText, "\n") {
			if strings.TrimSpace(l) == "-L" || strings.HasPrefix(strings.TrimSpace(l), "-L,") {
				return true
			}
		}
		return false
	}
	return strings.Contains(helpText, flag)
}

// alwaysStandIn are the /proc stand-ins used on Android without probing
// the real file first.
var alwaysStandIn = map[string]bool{"max_user_watches": true, "overflowuid": true, "overflowgid": true, "filesystems": true}

func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 1)
	_, err = f.Read(b)
	return err == nil || err == io.EOF
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// Minimal is set after proot refused an option ("see proot --help"):
// only the options every proot has, plus link2symlink (hard links fail
// on Android without it).
var Minimal bool

// Describe is the proot in use, for logs: its first help line.
func Describe() string {
	Has("") // read the help once
	for _, l := range strings.Split(helpText, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "proot (no --help output)"
}

// reVersion is a proot version in its --help first line ("5.1.107.96",
// "5.1.107-96").
var reVersion = regexp.MustCompile(`(?:^|[^0-9.])(\d+\.\d+(?:\.\d+)*(?:-\d+)?)`)

// Version is proot's version from its --help, or "" if there is none.
func Version() string {
	m := reVersion.FindStringSubmatch(Describe())
	if m == nil || len(m[1]) > 32 {
		return ""
	}
	return m[1]
}

// Args returns proot's argument list (without the command to run).
func (t *Target) Args() []string {
	a := []string{Binary()}
	optional := []string{"--kill-on-exit", "--link2symlink", "--sysvipc", "-L"}
	if Minimal {
		optional = []string{"--link2symlink"}
	}
	// Experiment (#319): drop --link2symlink to test whether its hardlink
	// emulation is what SIGSEGVs proot during dpkg unpack on GKI 6.6/6.12
	// "android15/android16" kernels. Off by default; without it dpkg's
	// hardlinks go straight through and may fail, so this is only ever a
	// last-resort retry (NoLink2Symlink, set by pkgOps) or a hand-set A/B
	// lever (ANDRONIX_NO_L2S=1), never the shipped default.
	if NoLink2Symlink || os.Getenv("ANDRONIX_NO_L2S") == "1" {
		kept := optional[:0:0]
		for _, f := range optional {
			if f != "--link2symlink" {
				kept = append(kept, f)
			}
		}
		optional = kept
	}
	for _, f := range optional {
		if Has(f) {
			a = append(a, f)
		}
	}
	if Has("--kernel-release") && !Minimal && spoofKernelRelease() {
		a = append(a, "--kernel-release="+SpoofedRelease(sys.KernelRelease(), sys.DetectArch()))
	}
	if t.User != "" && t.UID != "" {
		a = append(a, "--change-id="+t.UID+":"+t.GID, "-w", t.Home)
	} else {
		a = append(a, "-0", "-w", "/root")
	}
	a = append(a, "-r", t.Rootfs, "-b", "/dev", "-b", "/proc", "-b", "/sys", "-b", "/dev/urandom:/dev/random")
	if !exists("/dev/fd") {
		a = append(a, "-b", "/proc/self/fd:/dev/fd")
	}
	for i, n := range []string{"stdin", "stdout", "stderr"} {
		if !exists("/dev/" + n) {
			a = append(a, "-b", "/proc/self/fd/"+string(rune('0'+i))+":/dev/"+n)
		}
	}
	if st, err := os.Stat(filepath.Join(t.Dir, "shm")); err == nil && st.IsDir() {
		a = append(a, "-b", filepath.Join(t.Dir, "shm")+":/dev/shm")
	}
	// Stand-ins for /proc entries newer Android hides from apps.
	sd := filepath.Join(t.Dir, "sysdata")
	for _, e := range [][2]string{
		{"/proc/loadavg", "loadavg"}, {"/proc/stat", "stat"}, {"/proc/uptime", "uptime"},
		{"/proc/version", "version"}, {"/proc/vmstat", "vmstat"},
		{"/proc/sys/kernel/cap_last_cap", "cap_last_cap"},
		{"/proc/sys/fs/inotify/max_user_watches", "max_user_watches"},
		{"/proc/sys/kernel/overflowuid", "overflowuid"}, {"/proc/sys/kernel/overflowgid", "overflowgid"},
		{"/proc/filesystems", "filesystems"},
	} {
		// These Android 11+ denies to apps outright; even looking (the
		// readable probe) logs an SELinux AVC, so on Android the stand-in
		// is used without asking.
		always := runtime.GOOS == "android" && alwaysStandIn[e[1]]
		if (always || !readable(e[0])) && exists(filepath.Join(sd, e[1])) {
			a = append(a, "-b", filepath.Join(sd, e[1])+":"+e[0])
		}
	}
	// apk 3 needs this stand-in always (see pkgmgr "apk").
	if exists(filepath.Join(sd, "memfd_noexec")) {
		a = append(a, "-b", filepath.Join(sd, "memfd_noexec")+":/proc/sys/vm/memfd_noexec")
	}
	// SVE cpuinfo mask (#192): on Qualcomm chips whose cores LLVM thinks have
	// SVE but that ship with SVE off, Mesa's llvmpipe JIT SIGILLs on the first
	// GL draw. When present (written only for such phones, see
	// rootfs.writeCpuinfoStandIn), bind the corrected cpuinfo over the real
	// one. Gated on existence, not readability: /proc/cpuinfo is readable; we
	// override it deliberately, and this bind (after -b /proc) shadows it.
	if exists(filepath.Join(sd, "cpuinfo")) {
		a = append(a, "-b", filepath.Join(sd, "cpuinfo")+":/proc/cpuinfo")
	}
	if exists("/sys/fs/selinux") && exists(filepath.Join(sd, "empty")) {
		a = append(a, "-b", filepath.Join(sd, "empty")+":/sys/fs/selinux")
	}
	// Shared storage at /sdcard (after termux-setup-storage). Not in the
	// standalone app: there the app passes the folders it may share
	// (ANDRONIX_BINDS, below), and a half-readable /sdcard only confuses.
	standalone := os.Getenv("ANDRONIX_HOST") == "standalone"
	if standalone {
		// nothing
	} else if readable("/storage/emulated/0") || dirReadable("/storage/emulated/0") {
		a = append(a, "-b", "/storage/emulated/0:/sdcard")
		if dirReadable("/storage") {
			a = append(a, "-b", "/storage")
		}
	} else if dirReadable("/sdcard") {
		a = append(a, "-b", "/sdcard")
	}
	// Termux:X11's display socket, while its server runs, so GUI apps in
	// any session of the distro can reach DISPLAY=:0. Only the one socket:
	// VNC's own sockets stay in the distro's /tmp.
	if st, err := os.Stat(termux.X11Socket(termux.X11Display)); err == nil && st.Mode()&os.ModeSocket != 0 {
		a = append(a, "-b", fmt.Sprintf("%s:/tmp/.X11-unix/X%d", termux.X11Socket(termux.X11Display), termux.X11Display))
	}
	a = append(a, legacyBinds(t.BindsDir)...)
	for _, b := range strings.Fields(os.Getenv("ANDRONIX_BINDS")) {
		if arg, ok := t.bindArg(b, standalone); ok {
			a = append(a, "-b", arg)
		}
	}
	return a
}

// sharedStorage matches the phone folders the standalone app may share
// into a distro: Andronix's own folders in Download and Documents, for any
// Android user (/storage/emulated/<N>).
var sharedStorage = regexp.MustCompile(`^/storage/emulated/[0-9]+/(Download/Andronix|Documents/Andronix)(/.*)?$`)

// bindArg turns an ANDRONIX_BINDS entry "<src>:<dst>[:ro]" into proot's
// "-b src:dst". In the standalone app a leading ~ in dst is the session
// user's home, a shared phone folder gets its mount point made (owned by
// that user) and ~/Android/README.txt, and only shared folders or the
// app's own files are allowed. ":ro" is a label: proot can't make a bind
// read-only, Android's permissions do. Termux passes entries unchanged.
func (t *Target) bindArg(entry string, standalone bool) (string, bool) {
	if !standalone {
		return entry, true
	}
	parts := strings.Split(entry, ":")
	if len(parts) == 3 && parts[2] == "ro" {
		parts = parts[:2]
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		termux.Logf("binds: %q isn't <src>:<dst>[:ro]; skipped", entry)
		return "", false
	}
	src, dst := filepath.Clean(parts[0]), parts[1]
	home := t.Home
	if home == "" {
		home = "/root"
	}
	if dst == "~" || strings.HasPrefix(dst, "~/") {
		dst = home + strings.TrimPrefix(dst, "~")
	}
	dst = path.Clean(dst)
	appHome := os.Getenv("ANDRONIX_HOME")
	if appHome == "" {
		appHome = filepath.Join(sys.Home(), ".andronix")
	}
	appOwn := strings.HasPrefix(src+"/", filepath.Clean(appHome)+"/")
	shared := sharedStorage.MatchString(src)
	// A Unix socket (the app's GPU server) shares no files.
	sock := false
	if st, err := os.Stat(src); err == nil && st.Mode()&os.ModeSocket != 0 {
		sock = true
	}
	if !appOwn && !shared && !sock {
		termux.Logf("binds: %s isn't a folder the app may share; skipped", src)
		return "", false
	}
	if shared && t.Rootfs != "" && strings.HasPrefix(dst, "/") {
		t.makeMountPoint(dst, home)
	}
	return src + ":" + dst, true
}

// makeMountPoint creates dst in the rootfs (and the README in
// ~/Android), owned by the session user, so the shared folder shows up as
// a real folder in their home.
func (t *Target) makeMountPoint(dst, home string) {
	uid, _ := strconv.Atoi(t.UID)
	gid, _ := strconv.Atoi(t.GID)
	chown := func(p string) {
		if t.User != "" {
			os.Lchown(p, uid, gid)
		}
	}
	// Each missing folder from the home down gets the user as owner.
	rel := strings.TrimPrefix(dst, "/")
	cur := t.Rootfs
	for _, c := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, c)
		if _, err := os.Stat(cur); err != nil {
			if os.Mkdir(cur, 0o755) == nil {
				chown(cur)
			}
		}
	}
	if path.Dir(dst) == path.Join(home, "Android") {
		readme := filepath.Join(t.Rootfs, home, "Android", "README.txt")
		if _, err := os.Stat(readme); err != nil {
			os.WriteFile(readme, []byte("These folders are on your phone:\n"+
				"  Download and Documents are the Andronix folders in the phone's Download and Documents.\n"+
				"  Files you put here show up in both places.\n"), 0o644)
			chown(readme)
		}
	}
}

func dirReadable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// legacyBinds sources the old installers' ~/<distro>-binds/* snippets
// (`command+=" -b a:b"`) with bash and returns their arguments.
func legacyBinds(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	script := `command=""; for f in "$1"/*; do [ -f "$f" ] && . "$f"; done; printf '%s\n' $command`
	out, err := sys.Command("bash", "-c", script, "binds", dir).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// Env is the clean environment inside the distro.
func (t *Target) Env() []string {
	home, user := "/root", "root"
	if t.User != "" {
		home, user = t.Home, t.User
	}
	shell := t.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	lang := t.Lang
	if lang == "" {
		lang = "C.UTF-8"
	}
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	env := []string{"/usr/bin/env", "-i",
		"HOME=" + home, "USER=" + user, "LOGNAME=" + user, "SHELL=" + shell, "TERM=" + term, "LANG=" + lang,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/games:/usr/games",
		"PULSE_SERVER=" + PulseServer(), "TMPDIR=/tmp", "ANDRONIX_DISTRO=" + t.ID}
	if t.Display != "" {
		env = append(env, "DISPLAY="+t.Display)
	}
	// Mesa over Termux:X11 takes the DRI3 path (zink, with no Vulkan
	// driver) and deadlocks: GTK apps hang and XFCE stays black. Every
	// session gets it, so apps opened on :0 from a second session work too;
	// Xvnc has no DRI3, so VNC is unchanged. ANDRONIX_DRI3=1 keeps DRI3
	// for GPU setups (turnip, zink with a Vulkan driver).
	if os.Getenv("ANDRONIX_DRI3") != "1" {
		env = append(env, "LIBGL_DRI3_DISABLE=1")
	}
	for _, k := range []string{"COLORTERM", "NO_COLOR", "COLUMNS", "ANDRONIX_ASCII", "ANDRONIX_PLAIN", "ANDRONIX_HOST", "ANDRONIX_TRACE", "ANDRONIX_FOLD_FIX", "ANDRONIX_CLIENT_CURSOR", "ANDRONIX_BLOCKED_DESKTOPS"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if x := t.xdgConfigDirs(); x != "" {
		env = append(env, "XDG_CONFIG_DIRS="+x)
	}
	for _, k := range sortedKeys(t.ExtraEnv) {
		env = append(env, k+"="+t.ExtraEnv[k])
	}
	return env
}

// xdgConfigDirs is, in the standalone app, the same XDG_CONFIG_DIRS the
// desktop session gets (xstartup): the standalone layer, then the light
// profile's, then /etc/xdg. Without it an xfce program started from a
// shell runs its own xfconfd that sees only /etc/xdg, and its first
// settings write saves the stock channel (compositing on) into the
// user's home, where it wins over the layers for every later session.
// Empty in Termux (unchanged there).
func (t *Target) xdgConfigDirs() string {
	if os.Getenv("ANDRONIX_HOST") != "standalone" || t.Rootfs == "" {
		return ""
	}
	isDir := func(rel string) bool {
		st, err := os.Stat(filepath.Join(t.Rootfs, rel))
		return err == nil && st.IsDir()
	}
	var dirs []string
	if isDir("etc/xdg/andronix-standalone") {
		dirs = append(dirs, "/etc/xdg/andronix-standalone")
	}
	if b, err := os.ReadFile(filepath.Join(t.Rootfs, "etc/andronix/profile")); err == nil &&
		strings.TrimSpace(string(b)) == "light" && isDir("etc/xdg/andronix-light") {
		dirs = append(dirs, "/etc/xdg/andronix-light")
	}
	if len(dirs) == 0 {
		return ""
	}
	tail := t.XDGDefault
	if tail == "" {
		tail = "/etc/xdg"
	}
	return strings.Join(append(dirs, tail), ":")
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Session, when set, readies a session's name lookups (the app's DNS
// forwarder, card #141) before proot runs; pid is the process whose end
// ends the session.
var Session func(rootfs string, pid int)

func (t *Target) session(pid int) {
	if Session != nil && !t.Local {
		Session(t.Rootfs, pid)
	}
}

// Login replaces this process with a login shell (or `shell -c cmd`,
// which is how the old start scripts passed arguments).
func (t *Target) Login(shell string, cmd []string) error {
	os.Unsetenv("LD_PRELOAD")
	argv := append(t.Args(), t.Env()...)
	argv = append(argv, shell, "-l")
	switch {
	case len(cmd) == 1:
		// One argument is a shell command line, like the old start
		// scripts: ./start-debian.sh "apt update && apt upgrade".
		argv = append(argv, "-c", cmd[0])
	case len(cmd) > 1:
		// Several arguments reach the distro unchanged (after the login
		// profile has run): ./start-debian.sh sh -c "a && b".
		argv = append(argv, "-c", `exec "$@"`, "andronix")
		argv = append(argv, cmd...)
	}
	bin, err := sys.LookPath(Binary())
	if err != nil {
		return err
	}
	t.session(os.Getpid()) // exec keeps the pid: the forwarder ends with proot
	return sys.Exec(bin, argv, hostEnv())
}

// Run runs a shell command inside the distro as root, streaming each
// output line to onLine. stdin may be nil.
//
// When ctx is cancelled (Ctrl-C, the app's Stop) the command's processes
// are ended first and proot last (stopSession), and Run returns ctx's
// error. Once the command has ended, or was cancelled, Run reads what is
// left of its output but doesn't wait for a process that got away with the
// pipe open (outPipe).
func (t *Target) Run(ctx context.Context, cmd string, stdin io.Reader, onLine func(string)) error {
	root := *t
	root.User = ""
	var c *exec.Cmd
	if t.Local {
		c = sys.CommandContext(ctx, "/bin/sh", "-c", cmd)
	} else {
		t.reapOrphans()
		argv := append(root.Args(), root.Env()...)
		argv = append(argv, "/bin/sh", "-c", cmd)
		c = sys.CommandContext(ctx, argv[0], argv[1:]...)
		c.Env = hostEnv()
		if c.Err == nil {
			// By pid (sys.Signal), never c.Process.Signal: that goes through
			// a pidfd, which Android 11 answers with SIGSYS (card #395).
			c.Cancel = func() error {
				return stopSession(c.Process.Pid, func(sig syscall.Signal) error { return sys.Signal(c.Process, sig) })
			}
		}
	}
	// For stdin's copy (the output pipe is ours): a process that kept
	// that pipe doesn't hold up Wait. With WaitDelay, os/exec itself calls
	// Process.Kill (a pidfd again, see Cancel above) when Wait hasn't got
	// the command's end waitDelay after Cancel returned: only a proot (or
	// the local shell) that isn't gone that long after its SIGKILL, which
	// a killed process is.
	c.WaitDelay = waitDelay // pidfd-ok: exec's Kill needs the command unreaped 5 s after our SIGKILL
	c.Stdin = stdin
	t.session(os.Getpid()) // this process waits for proot
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		pw.Close()
		return err
	}
	pw.Close() // the command's processes have the only writing ends now
	out := &outPipe{f: pr}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if onLine != nil {
				onLine(sc.Text())
			}
		}
		io.Copy(io.Discard, out)
		close(done)
	}()
	err = c.Wait()
	// proot killed (by our cancel, by Android, by a crash): end what it
	// left without a tracer now, not in the next andronix. A retry in this
	// process would run beside it, and the pipe would wait for it first.
	swept := false
	if !t.Local && (signaled(err) || errors.Is(err, exec.ErrWaitDelay)) {
		t.sweepOrphans("this run left")
		swept = true
	}
	out.ended()
	<-done
	if out.keptOpen() {
		termux.Logf("proot: a process kept the output pipe open after the command ended: %s", firstLine(cmd))
		if !t.Local && !swept {
			t.sweepOrphans("this run left")
		}
	}
	switch {
	case err != nil && errors.Is(ctx.Err(), context.Canceled):
		// Not "signal: killed": the caller asked for this.
		return ctx.Err()
	case errors.Is(err, exec.ErrWaitDelay):
		// The command itself ended well; something it started kept stdin's
		// pipe.
		return nil
	}
	return err
}

// signaled reports whether err is a command ended by a signal.
func signaled(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ProcessState == nil {
		return false
	}
	ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}

// outPipe is the reading end of a command's output pipe. While the command
// runs, a read waits as long as it takes. Once it has ended (ended), a read
// waits waitDelay at most for more, all reads together wait drainMax at
// most, and drainBytes at most are read: output that is already in the pipe
// is read however slowly the caller takes it, and a process that kept the
// pipe open (a daemon the command started, a process that outlived a killed
// proot) is not waited for, whether it is silent, writes now and then, or
// writes faster than we read. After that the pipe reads as ended.
type outPipe struct {
	f      *os.File
	mu     sync.Mutex
	over   bool          // the command has ended
	waited time.Duration // spent waiting in reads since then
	read   int           // bytes read since then
	kept   bool          // gave up on a pipe that was still open
}

func (o *outPipe) Read(p []byte) (int, error) {
	o.mu.Lock()
	if o.over && !o.kept && o.read >= drainBytes {
		o.kept = true
	}
	if o.kept {
		o.mu.Unlock()
		return 0, io.EOF
	}
	over := o.over
	if over {
		o.bound()
	}
	o.mu.Unlock()
	start := time.Now()
	n, err := o.f.Read(p)
	o.mu.Lock()
	defer o.mu.Unlock()
	if over {
		o.waited += time.Since(start)
		o.read += n
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		o.kept = true
		return n, io.EOF
	}
	return n, err
}

// ended says the command has ended; it also bounds a read that is waiting.
func (o *outPipe) ended() {
	o.mu.Lock()
	o.over = true
	o.bound()
	o.mu.Unlock()
}

// bound sets the next read's deadline (o.mu held). os.Pipe's ends take
// deadlines on every system andronix runs on.
func (o *outPipe) bound() {
	o.f.SetReadDeadline(time.Now().Add(min(waitDelay, max(drainMax-o.waited, 0))))
}

func (o *outPipe) keptOpen() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.kept
}

// firstLine is the start of a command, for a log line.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// Interactive runs a command inside the distro on this terminal (for
// prompts like vncpasswd or the first-boot setup).
func (t *Target) Interactive(cmd string, asUser bool) error {
	tt := *t
	if !asUser {
		tt.User = ""
	}
	argv := append(tt.Args(), tt.Env()...)
	argv = append(argv, "/bin/sh", "-c", cmd)
	c := sys.Command(argv[0], argv[1:]...)
	c.Env = hostEnv()
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	tt.session(os.Getpid())
	return c.Run()
}

// Watch readies name lookups for a Command started by the caller, ending
// with that process (a desktop session outlives this one).
func (t *Target) Watch(pid int) { t.session(pid) }

// Command is a command inside the distro (as the login user unless the
// target is root), ready to start. The caller sets stdio.
func (t *Target) Command(argv ...string) *exec.Cmd {
	a := append(t.Args(), t.Env()...)
	a = append(a, argv...)
	c := sys.Command(a[0], a[1:]...)
	c.Env = hostEnv()
	return c
}

// hostEnv is the environment for proot itself: no LD_PRELOAD (termux-exec
// breaks proot) and our own PROOT_TMP_DIR, since Termux clears
// $PREFIX/tmp on its own and proot fails when its files vanish.
func hostEnv() []string {
	out := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "LD_PRELOAD=") && !strings.HasPrefix(e, "PROOT_TMP_DIR=") {
			out = append(out, e)
		}
	}
	out = append(out, "PROOT_TMP_DIR="+TmpDir())
	// The compat rules' env (e.g. PROOT_NO_SECCOMP on old kernels); the
	// user's own settings win.
	for _, e := range compatEnv() {
		if k, _, _ := strings.Cut(e, "="); os.Getenv(k) == "" {
			out = append(out, e)
		}
	}
	// Fallback when the rules couldn't load, or set for the rest of this
	// run after proot's fake root failed (NoSeccomp).
	if os.Getenv("PROOT_NO_SECCOMP") == "" && !hasKey(compatEnv(), "PROOT_NO_SECCOMP") && (NoSeccomp || oldSyscallOrder(sys.KernelRelease())) {
		out = append(out, "PROOT_NO_SECCOMP=1")
	}
	return out
}

// NoSeccomp makes every later proot run of this process use ptrace only
// (PROOT_NO_SECCOMP=1): set when a program in the distro wasn't root under
// proot -0 (support #312).
var NoSeccomp bool

// NoLink2Symlink drops proot's --link2symlink for the rest of this run
// (experiment #319): set after a package op SIGSEGV'd proot, to test whether
// link2symlink's hardlink emulation is the cause. Also forced from the start
// by ANDRONIX_NO_L2S=1 (a hand-set A/B lever). Default off: without it dpkg's
// hardlinks go straight through and may fail, so it is never the shipped
// default.
var NoLink2Symlink bool

// EnvHook returns environment for every proot run (the compat rules, set
// by the app). Called once, lazily.
var (
	EnvHook func() []string
	envMu   sync.Mutex
	envSet  bool
	extra   []string
)

// compatEnv is the compat env: set by SetEnv, else from EnvHook once. The
// hook runs without the lock held, since it may call SetEnv itself.
func compatEnv() []string {
	envMu.Lock()
	set, env := envSet, extra
	envMu.Unlock()
	if set || EnvHook == nil {
		return env
	}
	env = EnvHook()
	envMu.Lock()
	if !envSet {
		envSet, extra = true, env
	}
	env = extra
	envMu.Unlock()
	return env
}

// SetEnv replaces the compat env (the installer, once it has probed).
func SetEnv(env []string) {
	envMu.Lock()
	envSet, extra = true, env
	envMu.Unlock()
}

func hasKey(env []string, key string) bool {
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k == key {
			return true
		}
	}
	return false
}

// OldKernel reports whether this phone's kernel is older than 4.8, where
// proot runs without seccomp acceleration (see oldSyscallOrder). Desktops
// there can hang at start (xfwm4 on Android 9, kernel 4.4).
func OldKernel() bool { return oldSyscallOrder(sys.KernelRelease()) }

// oldSyscallOrder reports a kernel before 4.8, where seccomp stops come
// before ptrace's syscall-enter stop. proot's seccomp acceleration then
// aborts ("assertion IS_IN_SYSENTER(tracee) failed") on the first apt run:
// Android 9 on kernel 4.4, in the app and under run-as alike. Plain
// ptrace is slower but works.
func oldSyscallOrder(release string) bool {
	var major, minor int
	if n, _ := fmt.Sscanf(release, "%d.%d", &major, &minor); n < 2 {
		return false
	}
	return major < 4 || major == 4 && minor < 8
}

// KernelSpoofFloor is the kernel (major.minor) at or above which Andronix
// stops passing --kernel-release. The 6.12 spoof exists only so a modern
// distro's minimum-kernel check passes on OLD phones; the highest floor
// among the distros we ship is Arch/Manjaro's glibc (--enable-kernel 4.4),
// Debian/Ubuntu/Kali glibc is 3.2, systemd is ~4.15, and musl (Alpine) has
// no check — so a real kernel this new clears every one by years. On such a
// phone the guest sees its real, modern release and proot's kompat
// extension (which --kernel-release turns on, and whose stock Termux
// handlers SIGSEGV on GKI 6.6/6.12 "android15/16" kernels, card #319) never
// runs. 6.0 covers the whole crash cluster (all 6.6/6.12) with margin.
const (
	KernelSpoofFloorMajor = 6
	KernelSpoofFloorMinor = 0
)

// SpoofKernelRelease reports whether a phone on this kernel release still
// needs the --kernel-release spoof: true when the release is older than the
// floor, or can't be parsed (the safe default — keep the old behaviour).
// Pure; the env override and the real kernel are applied by the caller.
func SpoofKernelRelease(release string) bool {
	var major, minor int
	if n, _ := fmt.Sscanf(release, "%d.%d", &major, &minor); n < 2 {
		return true
	}
	if major != KernelSpoofFloorMajor {
		return major < KernelSpoofFloorMajor
	}
	return minor < KernelSpoofFloorMinor
}

// The releases --kernel-release reports. Both end in -andronix, which is
// how a report tells proot's stand-in from the phone's kernel (guestKernel).
const (
	spoofRelease = "6.12.0-andronix"
	// On a 32-bit userland whose real kernel is older than 5.1, 6.12 is a
	// lie programs act on: Go 1.26's runtime on 32-bit Linux picks
	// futex_time64 and timer_settime64 from uname alone (5.1 or newer, no
	// fallback), the kernel answers ENOSYS, and every Go program in the
	// distro dies at start ("futexwakeup ... returned -38", SIGSEGV at
	// 0x1006) — our own /usr/local/bin/andronix first: the compat probe,
	// setup-user, vnc, packs (support #5402, #3812). 5.0 is the newest
	// release that's still true about those syscalls, and clears every
	// minimum-kernel check of the distros with a 32-bit build.
	spoofRelease32Old = "5.0.0-andronix"
)

// SpoofedRelease is the release --kernel-release reports on a phone with
// this real kernel release and userland (see spoofRelease32Old). Pure.
func SpoofedRelease(release string, arch sys.Arch) string {
	if arch != "arm" && arch != "i686" {
		return spoofRelease
	}
	var major, minor int
	if n, _ := fmt.Sscanf(release, "%d.%d", &major, &minor); n < 2 {
		return spoofRelease // unparseable: the old behaviour
	}
	if major < 5 || major == 5 && minor < 1 {
		return spoofRelease32Old
	}
	return spoofRelease
}

// spoofKernelRelease decides it for this run. ANDRONIX_KERNEL_RELEASE_SPOOF
// forces it either way for an A/B: "1"/"always" keeps the spoof (the old
// behaviour, the control), "0"/"never" drops it.
func spoofKernelRelease() bool {
	switch os.Getenv("ANDRONIX_KERNEL_RELEASE_SPOOF") {
	case "1", "always":
		return true
	case "0", "never":
		return false
	}
	return SpoofKernelRelease(sys.KernelRelease())
}

var tmpOnce sync.Once

// TmpDir is ~/.andronix/tmp (or $ANDRONIX_HOME/tmp). The first call
// creates it and removes files left by proot processes that are gone;
// live sessions' files are kept.
func TmpDir() string {
	base := os.Getenv("ANDRONIX_HOME")
	if base == "" {
		base = filepath.Join(sys.Home(), ".andronix")
	}
	dir := filepath.Join(base, "tmp")
	tmpOnce.Do(func() {
		os.MkdirAll(dir, 0o700)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			// proot names them <kind>-<pid>-<random>: proot- (glue),
			// prooted- (its loader, when not in libexec) and prootshm-
			// (--sysvipc). Removing a live session's loader makes every
			// exec in it fail with ENOENT.
			parts := strings.SplitN(e.Name(), "-", 3)
			if len(parts) == 3 && strings.HasPrefix(parts[0], "proot") {
				if _, err := os.Stat("/proc/" + parts[1]); err == nil {
					continue
				}
			}
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	})
	return dir
}
