/*
 * libandronix-getcwd.so: preloaded (/etc/ld.so.preload) on devices where
 * proot answers getcwd(2) with ENOSYS, so the C library's getcwd() returns
 * "Function not implemented" and sh/apt/dpkg fall over ("sh: 0: getcwd()
 * failed: Function not implemented", "cannot fetch initial working
 * directory" from dpkg-preconfigure). Seen on Blackview/rockchip kernel
 * 5.10 ROMs (support #229, report AX-N73QZG, SHARK 8); PROOT_NO_SECCOMP=1
 * does not help because it is proot's own getcwd path, not seccomp.
 *
 * getcwd(), get_current_dir_name() and getwd() call the real libc function
 * first; only when it fails with ENOSYS do they fall back to the current
 * directory read from /proc/self/cwd (and, failing that, $PWD). Every other
 * outcome is passed through unchanged, so the shim is inert everywhere the
 * syscall works.
 *
 * The compat rule "getcwd-shim" preloads this when the probe reports
 * getcwd=ENOSYS inside the distro.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#ifndef PATH_MAX
#define PATH_MAX 4096
#endif

/* cwd reads the current directory the way getcwd can't: /proc/self/cwd,
 * then $PWD. Returns the length (without the NUL) or 0. */
static size_t cwd(char *out, size_t cap)
{
	ssize_t n = readlink("/proc/self/cwd", out, cap - 1);
	if (n > 0) {
		out[n] = '\0';
		return (size_t)n;
	}
	const char *pwd = getenv("PWD");
	if (pwd != NULL && pwd[0] == '/') {
		size_t len = strlen(pwd);
		if (len < cap) {
			memcpy(out, pwd, len + 1);
			return len;
		}
	}
	return 0;
}

char *getcwd(char *buf, size_t size)
{
	static char *(*real)(char *, size_t);
	if (!real)
		real = (char *(*)(char *, size_t))dlsym(RTLD_NEXT, "getcwd");
	if (real) {
		char *r = real(buf, size);
		if (r != NULL || errno != ENOSYS)
			return r;
	}

	char path[PATH_MAX];
	size_t len = cwd(path, sizeof(path));
	if (len == 0) {
		errno = ENOSYS;
		return NULL;
	}
	if (buf == NULL) {
		/* glibc extension: allocate. size caps the allocation when set. */
		size_t cap = (size != 0 && size > len) ? size : len + 1;
		buf = malloc(cap);
		if (buf == NULL) {
			errno = ENOMEM;
			return NULL;
		}
	} else if (len + 1 > size) {
		errno = ERANGE;
		return NULL;
	}
	memcpy(buf, path, len + 1);
	return buf;
}

char *get_current_dir_name(void)
{
	static char *(*real)(void);
	if (!real)
		real = (char *(*)(void))dlsym(RTLD_NEXT, "get_current_dir_name");
	if (real) {
		errno = 0;
		char *r = real();
		if (r != NULL || errno != ENOSYS)
			return r;
	}
	char path[PATH_MAX];
	size_t len = cwd(path, sizeof(path));
	if (len == 0) {
		errno = ENOSYS;
		return NULL;
	}
	char *r = malloc(len + 1);
	if (r == NULL) {
		errno = ENOMEM;
		return NULL;
	}
	memcpy(r, path, len + 1);
	return r;
}

/* getwd(3): legacy, no size; the caller's buffer must be PATH_MAX. */
char *getwd(char *buf)
{
	static char *(*real)(char *);
	if (!real)
		real = (char *(*)(char *))dlsym(RTLD_NEXT, "getwd");
	if (real) {
		char *r = real(buf);
		if (r != NULL || errno != ENOSYS)
			return r;
	}
	if (cwd(buf, PATH_MAX) == 0) {
		errno = ENOSYS;
		return NULL;
	}
	return buf;
}
