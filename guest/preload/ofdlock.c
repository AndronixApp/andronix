/*
 * libandronix-ofdlock.so: preloaded (/etc/ld.so.preload) on kernels before
 * 3.15, which have no open file description locks: fcntl(F_OFD_SETLKW)
 * fails with EINVAL. systemd 256+ takes /etc/.pwd.lock that way
 * (take_etc_passwd_lock -> unposix_lock), so sysusers and systemd's
 * postinst stopped with "Failed to take /etc/passwd lock: Invalid
 * argument" on kernel 3.10 phones (support #436 Kali, #438 Ubuntu KDE).
 *
 * The F_OFD_* commands become the classic per-process POSIX locks, which
 * these kernels have. The difference (a classic lock goes when the process
 * closes any fd of the file) doesn't matter for a lock file held by one
 * process for one job. Every other command passes through unchanged.
 *
 * No <fcntl.h>: with 64-bit time (Debian 13 and Ubuntu 26.04 on 32-bit
 * arm) it renames fcntl and fcntl64 to __fcntl_time64, and each exported
 * name here takes its own struct flock, so the numbers are spelled out.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdarg.h>

#define F_OFD_GETLK 36
#define F_OFD_SETLK 37
#define F_OFD_SETLKW 38

/* Classic lock commands for struct flock (32-bit offsets on 32-bit CPUs)
 * and struct flock64; on 64-bit CPUs both are 5, 6, 7. */
#define F_GETLK 5
#define F_SETLK 6
#define F_SETLKW 7
#if defined(__LP64__)
#define F_GETLK64 5
#define F_SETLK64 6
#define F_SETLKW64 7
#else
#define F_GETLK64 12
#define F_SETLK64 13
#define F_SETLKW64 14
#endif

typedef int (*fcntl_fn)(int, int, ...);

static int call(const char *name, fcntl_fn *real, int fd, int cmd, void *arg, int lk64)
{
	switch (cmd) {
	case F_OFD_GETLK: cmd = lk64 ? F_GETLK64 : F_GETLK; break;
	case F_OFD_SETLK: cmd = lk64 ? F_SETLK64 : F_SETLK; break;
	case F_OFD_SETLKW: cmd = lk64 ? F_SETLKW64 : F_SETLKW; break;
	}
	if (!*real)
		*real = (fcntl_fn)dlsym(RTLD_NEXT, name);
	if (!*real) /* a libc without this name never calls it */
		*real = (fcntl_fn)dlsym(RTLD_NEXT, "fcntl");
	return (*real)(fd, cmd, arg);
}

#define WRAP(sym, cname, lk64)                                     \
	int cname(int fd, int cmd, ...) __asm__(#sym);                 \
	int cname(int fd, int cmd, ...)                                \
	{                                                              \
		static fcntl_fn real;                                      \
		va_list ap;                                                \
		va_start(ap, cmd);                                         \
		void *arg = va_arg(ap, void *);                            \
		va_end(ap);                                                \
		return call(#sym, &real, fd, cmd, arg, lk64);              \
	}

/* fcntl: struct flock as the caller's ABI has it (on 32-bit CPUs, old
 * 32-bit offsets). */
WRAP(fcntl, andronix_fcntl, 0)
/* fcntl64 (_FILE_OFFSET_BITS=64): struct flock64. */
WRAP(fcntl64, andronix_fcntl64, 1)
#if !defined(__LP64__)
/* __fcntl_time64 (_TIME_BITS=64, which implies 64-bit offsets). */
WRAP(__fcntl_time64, andronix_fcntl_time64, 1)
#endif
