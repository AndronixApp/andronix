/*
 * libandronix-creat.so: preloaded (/etc/ld.so.preload) on x86 phones and
 * Chromebooks whose Android sandbox doesn't allow the legacy creat syscall
 * (support #326: ChromeOS ARC answers ENOSYS). On x86 glibc's creat() is
 * that syscall; dnf's key import (librepo) uses creat(), so every Fedora
 * package install failed with "Failed to create file ...: Function not
 * implemented".
 *
 * creat(path, mode) is open(path, O_CREAT|O_WRONLY|O_TRUNC, mode), which
 * glibc does with openat. Other CPUs have no creat syscall; there the shim
 * is never applied (the compat rule keys on the probe).
 */
#define _GNU_SOURCE
#include <fcntl.h>
#include <sys/types.h>

int creat(const char *path, mode_t mode)
{
	return open(path, O_CREAT | O_WRONLY | O_TRUNC, mode);
}

int creat64(const char *path, mode_t mode)
{
	return open(path, O_CREAT | O_WRONLY | O_TRUNC | O_LARGEFILE, mode);
}
