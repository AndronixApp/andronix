package sys

import (
	"os"
	"syscall"
)

// Signal and Kill stand in for os.Process's Signal and Kill. From Go 1.23
// those send through a pidfd (the pidfd_send_signal system call) when the
// kernel gives one, as Android 11's 5.4 kernels do. Android's seccomp
// policy before Android 12 answers that system call with SIGSYS, and the
// check Go runs first doesn't notice there, so the installer died with
// "SIGSYS: bad system call" the moment it stopped a child: on Stop in the
// app, on Ctrl-C, or when a step's time ran out (card #395). kill(2) by
// pid is allowed on every Android.
//
// A process that is gone gives os.ErrProcessDone; a nil p or one that
// never started is done too. Unlike os.Process, these don't know whether
// the caller has waited for p already: don't signal a process after
// reaping it, since its pid may be another process's by then.
func Signal(p *os.Process, sig syscall.Signal) error {
	if p == nil || p.Pid <= 0 {
		return os.ErrProcessDone
	}
	return SignalPid(p.Pid, sig)
}

// Kill is Signal with SIGKILL.
func Kill(p *os.Process) error { return Signal(p, syscall.SIGKILL) }

// SignalPid is Signal for a pid read from a file or from /proc: never
// os.FindProcess(pid).Signal, which goes through a pidfd as well.
func SignalPid(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(pid, sig); err != nil {
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
