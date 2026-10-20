# Andronix's changes to proot 5.1.107.96 for the app's jniLibs build
# (inside.sh runs this from proot's source folder). See SHA256SUMS: a
# change here means new sums (PROOT_UPDATE=1).
def rep(p, old, new):
    s = open(p).read()
    assert s.count(old) == 1, (p, old[:60])
    open(p, 'w').write(s.replace(old, new, 1))

# 1. Guests start with no signals blocked. The app starts proot with the
#    mask Android's runtime gave it (SIGPIPE and SIGUSR1 blocked, SigBlk
#    0x1200), and every guest inherited it: a writer to a closed pipe got
#    EPIPE instead of dying, SIGUSR1 never arrived (sv2-gpu, Redmi, Oct 7).
rep('src/cli/cli.c', '''	/* Configure the memory allocator.  */
	talloc_enable_leak_report();''', '''	/* Andronix: start (and so start every guest) with no signals
	 * blocked; Android's app runtime leaves SIGPIPE and SIGUSR1
	 * blocked in every process an app starts.  */
	{
		sigset_t none;
		sigemptyset(&none);
		sigprocmask(SIG_SETMASK, &none, NULL);
	}

	/* Configure the memory allocator.  */
	talloc_enable_leak_report();''')
rep('src/cli/cli.c', '#include <string.h>        /* str*(3), basename(3),  */', '#include <string.h>        /* str*(3), basename(3),  */\n#include <signal.h>        /* sigprocmask(2), (Andronix) */')

# 2. kompat (on because of --kernel-release) traps only what it does work
#    for on Android: uname (the faked release), execve, sethostname and
#    setdomainname. Every other handler emulates a syscall missing before
#    Linux 2.6.30 and does nothing on any Android kernel (3.18+), but the
#    traps cost: futex and epoll_pwait alone were 28% of the tracer's
#    stops on a Redmi desktop, and openat/socket each paid an exit stop
#    (sv2-gpu's counting build, #69 c, Oct 7).
rep('src/extension/kompat/kompat.c', '''static FilteredSysnum filtered_sysnums[] = {
	{ PR_accept4,		FILTER_SYSEXIT },''', '''static FilteredSysnum filtered_sysnums[] = {
	{ PR_execve, 		FILTER_SYSEXIT },
	{ PR_setdomainname,	FILTER_SYSEXIT },
	{ PR_sethostname,	FILTER_SYSEXIT },
	{ PR_uname, 		FILTER_SYSEXIT },
	FILTERED_SYSNUM_END,
};

/* The full list (kernels before 2.6.30), unused on Android.  */
static FilteredSysnum filtered_sysnums_old_kernels[] UNUSED = {
	{ PR_accept4,		FILTER_SYSEXIT },''')

# 3. The SysV-IPC shm helper re-execs proot ("proot --shm-helper") through
#    /proc/self/exe. Under Android's linker (Play Store Termux's W^X exec
#    path, and the app when native libraries unpack without the exec bit,
#    support #229), /proc/self/exe is the linker, not proot, so this became
#    `linker64 --shm-helper`, which the linker rejects ("expected absolute
#    path: --shm-helper") and the desktop started without shared-memory
#    frames. andronix leaves proot's real path in TERMUX_EXEC__PROC_SELF_EXE
#    when it starts proot that way, so re-exec through the linker with it.
rep('src/extension/sysvipc/sysvipc_shm.c', '''#include <string.h> /* memset */''', '''#include <string.h> /* memset, str*(3) (Andronix) */
#include <stdlib.h> /* getenv (Andronix) */
#include <limits.h> /* PATH_MAX (Andronix) */''')
rep('src/extension/sysvipc/sysvipc_shm.c', '''			execl("/proc/self/exe", "proot", "--shm-helper", NULL);
				perror("proot-shm-helper: execl");
				_exit(1);''', '''			/* Andronix (#229): started through Android's linker,
				 * /proc/self/exe is the linker and proot's real path is
				 * in TERMUX_EXEC__PROC_SELF_EXE; re-exec through the
				 * linker so the helper is proot, not "--shm-helper".  */
				const char *self = getenv("TERMUX_EXEC__PROC_SELF_EXE");
				if (self != NULL && self[0] != '\\0') {
					char linker[PATH_MAX] = "/system/bin/linker64";
					char exe[PATH_MAX];
					ssize_t n = readlink("/proc/self/exe", exe, sizeof(exe) - 1);
					if (n > 0) {
						exe[n] = '\\0';
						const char *b = strrchr(exe, '/');
						if (strncmp(b != NULL ? b + 1 : exe, "linker", 6) == 0)
							strcpy(linker, exe);
					}
					execl(linker, linker, self, "--shm-helper", (char *)NULL);
					perror("proot-shm-helper: execl linker");
					_exit(1);
				}
				execl("/proc/self/exe", "proot", "--shm-helper", (char *)NULL);
				perror("proot-shm-helper: execl");
				_exit(1);''')
