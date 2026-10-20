package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// Starting a distro's app in its running desktop, for the Andronix app's
// home-screen shortcuts:
//
//	andronix launch <distro> <id>
//
// A new proot session can't do it (its --kill-on-exit ends whatever it
// started when it returns), so the desktop session runs a small agent
// (andronix session-agent, started by session-prep inside the session's
// D-Bus, so it has DISPLAY, DBUS and XDG): it listens on
// /tmp/.andronix-d/run.sock, which is $ANDRONIX_HOME/d/run.sock outside,
// and starts the app as the session's user. launch exits 0 once the app
// is started; otherwise one stderr line "LAUNCH error <class>" and exit 3
// no_desktop, 4 unknown_app, 5 timeout.

const agentSocket = "/tmp/.andronix-d/run.sock"

type launchReq struct {
	App    string `json:"app,omitempty"`
	Action string `json:"action,omitempty"` // desktop-reload, desktop-restart (from the display helper)
}

type launchResp struct {
	OK     bool   `json:"ok"`
	Distro string `json:"distro"`
	Error  string `json:"error,omitempty"` // unknown_app, failed
	PID    int    `json:"pid,omitempty"`
}

func launchFail(class string, code int) error {
	fmt.Fprintf(os.Stderr, "LAUNCH error %s\n", class)
	return &ExitError{Code: code}
}

// Launch is `andronix launch <distro> <id>` on the host.
func Launch(name, id string) error {
	if !appID.MatchString(id) {
		return launchFail("unknown_app", 4)
	}
	d, err := conf.LoadDistro(name)
	if err != nil || !Open(d).Installed() {
		return launchFail("no_desktop", 3)
	}
	sock := filepath.Join(GetPaths().Home, "d", "run.sock")
	// Right after the ready line the session may still be starting:
	// wait up to 20 s for its agent.
	var c net.Conn
	end := time.Now().Add(20 * time.Second)
	for {
		c, err = net.DialTimeout("unix", sock, time.Second)
		if err == nil {
			break
		}
		if !desktopRunning() { // nothing runs (a stale socket file doesn't count)
			return launchFail("no_desktop", 3)
		}
		if time.Now().After(end) {
			return launchFail("timeout", 5)
		}
		time.Sleep(300 * time.Millisecond)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	b, _ := json.Marshal(launchReq{App: id})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return launchFail("timeout", 5)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return launchFail("timeout", 5)
	}
	var r launchResp
	if json.Unmarshal(line, &r) != nil {
		return launchFail("timeout", 5)
	}
	switch {
	case r.Distro != d.ID:
		return launchFail("no_desktop", 3) // the desktop that runs is another distro's
	case r.Error == "unknown_app":
		return launchFail("unknown_app", 4)
	case !r.OK:
		return launchFail("failed", 6)
	}
	return nil
}

// desktopRunning: a display or VNC session is up (its pid files).
func desktopRunning() bool {
	for _, p := range []string{displayState(), vncHostState()} {
		if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return true
		}
	}
	return false
}

// sessionEnvFile holds the desktop session's environment (DISPLAY, its
// D-Bus, XDG), NUL-separated, for the display helper's agent.
var sessionEnvFile = "/tmp/.andronix-d/session.env" // a var for tests

// startAgent is called by session-prep inside the session, in the app's
// display path only (where /tmp/.andronix-d is the app's folder). It
// writes the session's environment for the agent in the display helper
// (serveAgent). The agent used to be a process of its own (andronix
// session-agent): one more for Android's phantom process killer, which
// counts every app's child processes together (lead, dev21).
func startAgent() {
	if !standaloneHost() {
		return
	}
	if st, err := os.Stat(filepath.Dir(agentSocket)); err != nil || !st.IsDir() {
		return
	}
	tmp := sessionEnvFile + ".tmp"
	if os.WriteFile(tmp, []byte(strings.Join(os.Environ(), "\x00")), 0o600) == nil {
		os.Rename(tmp, sessionEnvFile)
	}
}

// sessionEnv is the session's environment from sessionEnvFile (nil
// before session-prep wrote it).
func sessionEnv() []string {
	b, err := os.ReadFile(sessionEnvFile)
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(string(b), "\x00")
}

// serveAgentInHelper runs the agent inside the display helper: it waits
// for the session's environment (the session is up), then serves the
// socket with it.
func serveAgentInHelper() {
	os.Remove(sessionEnvFile) // an earlier session's
	for sessionEnv() == nil {
		time.Sleep(200 * time.Millisecond)
	}
	agentInHelper = true
	if err := serveAgent(sessionEnv); err != nil {
		fmt.Fprintf(os.Stderr, "display: launch agent: %v\n", err)
	}
}

// agentInHelper: this process (the display helper) serves the agent, so
// askDesktop acts directly.
var agentInHelper bool

// SessionAgent is `andronix session-agent`: the agent as a process of its
// own, with its own environment (kept for older sessions and tests).
func SessionAgent() error { return serveAgent(func() []string { return nil }) }

// serveAgent serves agentSocket; env gives the session's environment for
// what it starts (nil: this process's own).
func serveAgent(env func() []string) error {
	if c, err := net.DialTimeout("unix", agentSocket, time.Second); err == nil {
		c.Close()
		return nil // one is already running
	}
	os.Remove(agentSocket)
	ln, err := net.Listen("unix", agentSocket)
	if err != nil {
		return err
	}
	defer ln.Close()
	os.Chmod(agentSocket, 0o600)
	distro := os.Getenv("ANDRONIX_DISTRO")
	for _, kv := range env() {
		if v, ok := strings.CutPrefix(kv, "ANDRONIX_DISTRO="); ok {
			distro = v
		}
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetDeadline(time.Now().Add(10 * time.Second))
			line, err := bufio.NewReader(c).ReadBytes('\n')
			if err != nil {
				return
			}
			var req launchReq
			resp := launchResp{Distro: distro}
			if json.Unmarshal(line, &req) == nil && req.Action == "desktop-reload" {
				resp.OK = reloadDesktop(env()) == nil
			} else if req.Action == "desktop-restart" {
				resp.OK = restartDesktop(env()) == nil
			} else if json.Unmarshal(line, &req) != nil || !appID.MatchString(req.App) {
				resp.Error = "unknown_app"
			} else if pid, err := spawnApp(req.App, env()); errors.Is(err, os.ErrNotExist) {
				resp.Error = "unknown_app"
			} else if err != nil {
				resp.Error = "failed"
			} else {
				resp.OK, resp.PID = true, pid
			}
			b, _ := json.Marshal(resp)
			c.Write(append(b, '\n'))
		}(c)
	}
}

// spawnApp starts a .desktop file's Exec, detached, with the session's
// environment (env; nil: this process's).
func spawnApp(id string, env []string) (int, error) {
	var file string
	var kv map[string]string
	for _, dir := range []string{"/usr/local/share/applications", "/usr/share/applications"} {
		p := filepath.Join(dir, id+".desktop")
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		kv = desktopEntry(f)
		f.Close()
		file = p
		break
	}
	if file == "" || kv["Type"] != "Application" || kv["Exec"] == "" {
		return 0, os.ErrNotExist
	}
	argv, err := execArgs(kv["Exec"], kv["Name"], file)
	if err != nil || len(argv) == 0 {
		return 0, fmt.Errorf("bad Exec: %v", err)
	}
	if kv["Terminal"] == "true" {
		for _, t := range [][]string{{"xfce4-terminal", "-x"}, {"x-terminal-emulator", "-e"}, {"xterm", "-e"}} {
			if _, err := sys.LookPath(t[0]); err == nil {
				argv = append(t, argv...)
				break
			}
		}
	}
	c := sys.Command(argv[0], argv[1:]...)
	c.Env = env
	if dir := kv["Path"]; dir != "" {
		c.Dir = dir
	} else {
		c.Dir = envHome(env)
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return 0, err
	}
	pid := c.Process.Pid
	go c.Wait() // reap it
	return pid, nil
}

// execArgs splits a Desktop Entry Exec value into argv: string escapes
// first (\s \n \t \r \\), then the quoting rules (double quotes, with \"
// \` \$ \\ inside), and field codes: %% is %, %c the name, %k the file,
// the others (%f %F %u %U %i …) are dropped since nothing is passed.
func execArgs(exec, name, file string) ([]string, error) {
	r := strings.NewReplacer(`\s`, " ", `\n`, "\n", `\t`, "\t", `\r`, "\r", `\\`, `\`)
	s := r.Replace(exec)
	var args []string
	var cur strings.Builder
	inQ, have := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQ && ch == '\\' && i+1 < len(s) && strings.ContainsRune("\"`$\\", rune(s[i+1])):
			cur.WriteByte(s[i+1])
			i++
		case ch == '"':
			inQ, have = !inQ, true
		case !inQ && (ch == ' ' || ch == '\t'):
			if have || cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		case ch == '%' && i+1 < len(s):
			code := s[i+1]
			i++
			switch code {
			case '%':
				cur.WriteByte('%')
			case 'c':
				cur.WriteString(name)
			case 'k':
				cur.WriteString(file)
			}
			// a lone field code leaves no empty argument (below)
		default:
			cur.WriteByte(ch)
		}
	}
	if inQ {
		return nil, fmt.Errorf("unbalanced quotes")
	}
	if have || cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args, nil
}

// reloadDesktop makes the desktop redraw its backdrop for the screen it has
// now: xfdesktop --reload, through the session's D-Bus (the agent has it).
// After a resize early in a session xfdesktop can keep the old monitor
// size and leave the new area black (android, fold right after start).
func reloadDesktop(env []string) error {
	if _, err := sys.LookPath("xfdesktop"); err != nil {
		return nil // another desktop
	}
	c := sys.Command("xfdesktop", "--reload")
	c.Env = env
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return nil // still running; it does its work anyway
	}
}

// restartDesktop starts xfdesktop and xfce4-panel afresh. A resize that
// lands while a GTK program is still opening the display can leave its
// GDK with the old monitor size for good (it read the geometry before it
// listened for RandR changes): a reload keeps that, a new process doesn't
// (android, 5640: fold right at READY, rows below 720 never drawn).
func restartDesktop(env []string) error {
	if _, err := sys.LookPath("xfdesktop"); err != nil {
		return nil
	}
	quiet := func(name string, args ...string) {
		c := sys.Command(name, args...)
		c.Env = env
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if c.Start() == nil {
			go c.Wait()
		}
	}
	// The panel restarts itself (-r); xfdesktop quits, and is started
	// again unless the session already did.
	if _, err := sys.LookPath("xfce4-panel"); err == nil {
		quiet("xfce4-panel", "-r")
	}
	q := sys.Command("xfdesktop", "--quit")
	q.Env = env
	q.Run()
	time.Sleep(1500 * time.Millisecond)
	if out, _ := sys.Command("pgrep", "-x", "xfdesktop").Output(); len(strings.TrimSpace(string(out))) == 0 {
		quiet("xfdesktop")
	}
	return nil
}

// envHome is HOME in env (nil: this process's).
func envHome(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok && v != "" {
			return v
		}
	}
	return home()
}

// askDesktop is the display helper asking the session's agent for an
// action (best effort: no agent, nothing happens). With the agent in this
// process, it acts directly.
func askDesktop(action string) {
	if agentInHelper {
		if action == "desktop-restart" {
			restartDesktop(sessionEnv())
		} else {
			reloadDesktop(sessionEnv())
		}
		return
	}
	c, err := net.DialTimeout("unix", agentSocket, time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	c.Write([]byte(`{"action":"` + action + `"}` + "\n"))
	bufio.NewReader(c).ReadBytes('\n')
}
