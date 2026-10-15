//go:build linux && (amd64 || 386)

package compat

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// creatSyscall tries the legacy creat syscall, which only x86 has (glibc
// uses it for creat(); elsewhere creat is openat). Android's x86 app
// sandbox may not allow it (support #326: ChromeOS ARC, ENOSYS).
func creatSyscall() (syscall.Errno, bool) {
	f, err := os.CreateTemp("", "andronix-probe-")
	if err != nil {
		return 0, false
	}
	f.Close()
	defer os.Remove(f.Name())
	p, _ := unix.BytePtrFromString(f.Name())
	r1, _, e := unix.Syscall(unix.SYS_CREAT, uintptr(unsafe.Pointer(p)), 0o600, 0)
	closeFd(r1, e)
	return e, true
}
