package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// SelfCheck is `andronix selfcheck [--json]`: can this phone run Linux
// from the standalone app? It checks what the spike's probe app checked,
// in the app's own process context and in about 100-200 ms: no network,
// and no writes outside $ANDRONIX_HOME/tmp. Exit status 0 only when every
// check passes. The app runs it once per app version and caches it.
type selfCheckResult struct {
	V      int                   `json:"v"`
	OK     bool                  `json:"ok"`
	SDK    int                   `json:"sdk"`
	Page   int                   `json:"page_size"`
	Checks map[string]checkState `json:"checks"`
	// What the compat engine knows without a distro (the host and a
	// cached probe) and the rules that match it, for problem reports.
	Probe *selfCheckProbe `json:"probe,omitempty"`
	Rules []string        `json:"rules"`
}

type selfCheckProbe struct {
	Summary string `json:"summary"` // "not-probed", "all-ok" or what isn't ok
	Kernel  string `json:"kernel"`
	SDK     int    `json:"sdk"`
	ROM     string `json:"rom"`
	RAM     string `json:"ram"`
	Termux  string `json:"termux,omitempty"`
}

type checkState struct {
	OK  bool   `json:"ok"`
	MS  int64  `json:"ms"`
	Err string `json:"err,omitempty"`
}

func SelfCheck(asJSON bool) error {
	// Its own folder per run: two checks at once mustn't share files.
	base := filepath.Join(GetPaths().Home, "tmp")
	os.MkdirAll(base, 0o700)
	tmp, terr := os.MkdirTemp(base, "selfcheck-")
	if terr != nil {
		tmp = filepath.Join(base, fmt.Sprintf("selfcheck-%d", os.Getpid()))
	}
	defer os.RemoveAll(tmp)
	res := selfCheckResult{V: 1, OK: true, Page: os.Getpagesize(), Checks: map[string]checkState{}}
	res.SDK, _ = strconv.Atoi(strings.TrimSpace(sysProp("ro.build.version.sdk")))
	run := func(name string, f func() error) {
		t := time.Now()
		err := f()
		c := checkState{OK: err == nil, MS: time.Since(t).Milliseconds()}
		if err != nil {
			c.Err = err.Error()
			res.OK = false
		}
		res.Checks[name] = c
	}
	run("api26", func() error {
		if res.SDK > 0 && res.SDK < 26 {
			return fmt.Errorf("Android API %d; standalone needs 26 (Android 8)", res.SDK)
		}
		return nil
	})
	run("tmp_dir", func() error { return os.MkdirAll(tmp, 0o700) })
	run("exec_libdir", func() error {
		out, err := sys.Command(proot.Binary(), "--version").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s --version: %v", proot.Binary(), err)
		}
		if !strings.Contains(strings.ToLower(string(out)), "proot") {
			return fmt.Errorf("unexpected output")
		}
		return nil
	})
	run("mmap_exec_data", func() error {
		p := filepath.Join(tmp, "page")
		if err := os.WriteFile(p, make([]byte, os.Getpagesize()), 0o600); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := syscall.Mmap(int(f.Fd()), 0, os.Getpagesize(), syscall.PROT_READ|syscall.PROT_EXEC, syscall.MAP_PRIVATE)
		if err != nil {
			return fmt.Errorf("mmap PROT_EXEC of an app data file: %w", err)
		}
		return syscall.Munmap(b)
	})
	run("anon_rw_to_rx", func() error {
		b, err := syscall.Mmap(-1, 0, os.Getpagesize(), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
		if err != nil {
			return err
		}
		defer syscall.Munmap(b)
		if err := syscall.Mprotect(b, syscall.PROT_READ|syscall.PROT_EXEC); err != nil {
			return fmt.Errorf("mprotect to RX: %w", err)
		}
		return nil
	})
	run("ptrace_child", func() error {
		runtime.LockOSThread() // ptrace belongs to the thread that started the child
		defer runtime.UnlockOSThread()
		c := sys.Command("/system/bin/true")
		c.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
		if err := c.Start(); err != nil {
			return fmt.Errorf("start a traced child: %w", err)
		}
		// It stops at its exec (SIGTRAP): wait for that, then let it go.
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(c.Process.Pid, &ws, 0, nil); err != nil || !ws.Stopped() {
			c.Process.Kill()
			c.Wait()
			return fmt.Errorf("the traced child didn't stop (%v, %v)", err, ws)
		}
		if err := syscall.PtraceDetach(c.Process.Pid); err != nil {
			c.Process.Kill()
			c.Wait()
			return fmt.Errorf("ptrace detach: %w", err)
		}
		return c.Wait()
	})
	// The whole chain: proot from nativeLibraryDir runs a program that
	// sits in app data, through the loader (exec + mmap PROT_EXEC + ptrace).
	// The test program is Android's toybox; where the system won't let the
	// app read it (Samsung One UI 8, Android 16: open /system/bin/toybox
	// gave EPERM, ANDROID-N), it's a copy of andronix itself.
	run("proot_run", func() error {
		bin, args, want := filepath.Join(tmp, "toybox"), []string{"echo", "andronix-selfcheck"}, "andronix-selfcheck"
		toybox := "/system/bin/toybox"
		if os.Getenv("ANDRONIX_TEST_NO_TOYBOX") == "1" { // tests: the One UI 8 path
			toybox = "/nonexistent/toybox"
		}
		err := copyFile(toybox, bin, 0o700)
		if err != nil {
			self, serr := sys.Executable()
			if serr != nil {
				return fmt.Errorf("copy a test program: %w (and no own binary: %v)", err, serr)
			}
			bin, args, want = filepath.Join(tmp, "andronix"), []string{"version"}, "andronix "
			if err2 := copyFile(self, bin, 0o700); err2 != nil {
				return fmt.Errorf("copy a test program: %v; then %w", err, err2)
			}
		}
		c := sys.Command(proot.Binary(), append([]string{"-r", "/", bin}, args...)...)
		c.Env = append(os.Environ(), "PROOT_TMP_DIR="+tmp, "ANDRONIX_NO_TELEMETRY=1")
		out, err := c.CombinedOutput()
		if err != nil || !strings.Contains(string(out), want) {
			return fmt.Errorf("proot ran nothing: %v: %s", err, lastNonEmpty(string(out), 2))
		}
		return nil
	})
	if asJSON {
		c := newCompat(nil, "")
		p := c.facts.Probe
		res.Probe = &selfCheckProbe{Summary: p.Summary(), Kernel: p.KernelMajorMinor(), SDK: p.SDK, ROM: p.ROM, RAM: p.RAMBucket(), Termux: p.Termux}
		res.Rules = append([]string{}, c.plan.IDs...)
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
	} else {
		for _, n := range []string{"api26", "tmp_dir", "exec_libdir", "mmap_exec_data", "anon_rw_to_rx", "ptrace_child", "proot_run"} {
			c := res.Checks[n]
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Printf("%s %-15s %4d ms %s\n", mark, n, c.MS, c.Err)
		}
	}
	if !res.OK {
		return errSelfCheck
	}
	return nil
}

// errSelfCheck makes the exit status non-zero without another message.
var errSelfCheck = &ExitError{Code: 1}

// ExitError ends a command with Code and no message: what it had to say
// is already out (JSON on stdout, or a machine-readable line).
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

// copyFile copies from to to with mode; its errors say which side failed
// (ANDROID-N: the report had only "open <path>: operation not permitted").
// A create refused with mode is tried once more as 0600, then chmodded.
func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("read %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil && mode != 0o600 {
		if out, err = os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			defer os.Chmod(to, mode)
		}
	}
	if err != nil {
		return fmt.Errorf("write %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy: %w", err)
	}
	return out.Close()
}

func sysProp(k string) string {
	out, err := sys.Command("getprop", k).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
