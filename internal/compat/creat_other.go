//go:build linux && !(amd64 || 386)

package compat

import "syscall"

// creatSyscall: no creat syscall on this CPU (creat() is openat).
func creatSyscall() (syscall.Errno, bool) { return 0, true }
