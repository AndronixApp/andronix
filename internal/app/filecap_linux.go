package app

import "golang.org/x/sys/unix"

// capFileSize sets pid's RLIMIT_FSIZE: past n bytes in any one file, a
// write fails and the process gets SIGXFSZ.
func capFileSize(pid int, n int64) error {
	lim := unix.Rlimit{Cur: uint64(n), Max: uint64(n)}
	return unix.Prlimit(pid, unix.RLIMIT_FSIZE, &lim, nil)
}
