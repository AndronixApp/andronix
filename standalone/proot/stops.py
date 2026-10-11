# PROOT_STOPS=1 builds only (inside.sh, from proot's source folder): count
# tracer stops per syscall, written to <guest root>/tmp/proot-stops.<pid>
# on SIGUSR1. A measurement build for sv2-gpu (#69 c), never shipped.
def rep(p, old, new):
    s = open(p).read()
    assert s.count(old) == 1, (p, old[:50])
    open(p, 'w').write(s.replace(old, new, 1))

rep('src/syscall/syscall.c', '#include "cli/note.h"', '''#include "cli/note.h"
#include "path/binding.h"

/* Andronix (PROOT_STOPS builds only): tracer stops per syscall, written
 * to <guest root>/tmp/proot-stops.<pid> on SIGUSR1 (see event.c).  */
unsigned long andronix_stops_enter[PR_NB_SYSNUM];
unsigned long andronix_stops_exit;
char *andronix_stops_root;''')
rep('src/syscall/syscall.c', '''	if (is_enter_stage) {
		/* Never restore original register values at the end
		 * of this stage.  */''', '''	if (is_enter_stage) {
		{
			Sysnum n = get_sysnum(tracee, CURRENT);
			if (n < PR_NB_SYSNUM)
				andronix_stops_enter[n]++;
			if (andronix_stops_root == NULL && get_root(tracee) != NULL)
				andronix_stops_root = strdup(get_root(tracee));
		}
		/* Never restore original register values at the end
		 * of this stage.  */''')
rep('src/syscall/syscall.c', '''	else {
		/* By default, restore original register values at the
		 * end of this stage.  */
		tracee->restore_original_regs = true;''', '''	else {
		andronix_stops_exit++;
		/* By default, restore original register values at the
		 * end of this stage.  */
		tracee->restore_original_regs = true;''')
rep('src/tracee/event.c', '#include <unistd.h>     /* fork(2), chdir(2), getpid(2), */', '''#include <unistd.h>     /* fork(2), chdir(2), getpid(2), */
#include <stdio.h>      /* fopen(3), fprintf(3), (Andronix stop counts) */
#include <time.h>       /* clock_gettime(2), */
#include <limits.h>     /* PATH_MAX, */
#include "syscall/sysnum.h"''')
rep('src/tracee/event.c', '''/* Print on stderr the complete talloc hierarchy.  */
static void print_talloc_hierarchy(int signum, siginfo_t *siginfo UNUSED, void *ucontext UNUSED)
{
	switch (signum) {
	case SIGUSR1:
		talloc_report_depth_cb(NULL, 0, 100, print_talloc_chunk, NULL);
		break;
''', '''/* Andronix (PROOT_STOPS builds only): SIGUSR1 asks for the stop counts
 * (syscall.c); the event loop writes them, outside the handler.  */
extern unsigned long andronix_stops_enter[];
extern unsigned long andronix_stops_exit;
extern char *andronix_stops_root;
static volatile sig_atomic_t andronix_dump_stops;

static void andronix_write_stops(void)
{
	char path[PATH_MAX];
	struct timespec now;
	unsigned long total = 0;
	FILE *f;
	int i;

	andronix_dump_stops = 0;
	snprintf(path, sizeof(path), "%s/tmp/proot-stops.%d",
		 andronix_stops_root != NULL ? andronix_stops_root : "", (int) getpid());
	f = fopen(path, "w");
	if (f == NULL)
		return;
	clock_gettime(CLOCK_MONOTONIC, &now);
	for (i = 0; i < PR_NB_SYSNUM; i++)
		total += andronix_stops_enter[i];
	fprintf(f, "# proot %d monotonic_ms %lld enter %lu exit %lu\\n", (int) getpid(),
		(long long) now.tv_sec * 1000 + now.tv_nsec / 1000000, total, andronix_stops_exit);
	for (i = 0; i < PR_NB_SYSNUM; i++)
		if (andronix_stops_enter[i] != 0)
			fprintf(f, "%lu %s\\n", andronix_stops_enter[i], stringify_sysnum(i));
	fclose(f);
}

/* Print on stderr the complete talloc hierarchy.  */
static void print_talloc_hierarchy(int signum, siginfo_t *siginfo UNUSED, void *ucontext UNUSED)
{
	switch (signum) {
	case SIGUSR1:
		andronix_dump_stops = 1;
		break;
''')
rep('src/tracee/event.c', '''		pid = waitpid(-1, &tracee_status, __WALL);
		saved_errno = errno;''', '''		pid = waitpid(-1, &tracee_status, __WALL);
		saved_errno = errno;
		if (andronix_dump_stops)
			andronix_write_stops();''')

# The app starts proot with SIGUSR1 blocked (Android's runtime): unblock
# it, or the dump never comes (andronix.py unblocks everything anyway).
rep('src/cli/cli.c', '''	/* Configure the memory allocator.  */
	talloc_enable_leak_report();''', '''	{
		sigset_t usr1;
		sigemptyset(&usr1);
		sigaddset(&usr1, SIGUSR1);
		sigprocmask(SIG_UNBLOCK, &usr1, NULL);
	}

	/* Configure the memory allocator.  */
	talloc_enable_leak_report();''')
if '#include <signal.h>' not in open('src/cli/cli.c').read():
    rep('src/cli/cli.c', '#include <string.h>        /* str*(3), basename(3),  */', '#include <string.h>        /* str*(3), basename(3),  */\n#include <signal.h>        /* sigprocmask(2), (Andronix stop counts) */')
