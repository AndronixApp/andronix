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
