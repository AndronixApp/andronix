/*
 * libnss_andronix.so.2: glibc's "hosts" lookups through Android's resolver
 * (card #141). Some networks drop DNS to public servers; the distro's
 * resolv.conf (8.8.8.8, 1.1.1.1) then fails while Android's own resolver
 * (netd: the network's DNS, or Private DNS) works. Each andronix session
 * runs a forwarder on 127.0.0.1:<port> (andronix __dnsfwd) and leaves the
 * file /run/andronix/dns/<port>; this module asks whichever one answers.
 *
 * nsswitch.conf: hosts: files andronix [NOTFOUND=return] dns
 *   - no forwarder answers (none running, a stale file): UNAVAIL, so dns
 *     runs as before; a stale file costs one refused connect.
 *   - Android says the name doesn't exist: NOTFOUND, returned at once.
 *
 * Protocol (TCP, one line each way):  "4 name\n", "6 name\n" or "0 name\n"
 *   -> "OK addr addr ...\n" | "NOTFOUND\n" | "ERR\n"
 *
 * Only old, plain libc calls (no strtol/sscanf, no fortify), so the module
 * loads in every glibc distro Andronix ships.
 */
#include <arpa/inet.h>
#include <dirent.h>
#include <errno.h>
#include <netdb.h>
#include <netinet/in.h>
#include <nss.h>
#include <poll.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define DIR_PATH "/run/andronix/dns"
#define MAX_ADDRS 16
#define CONNECT_MS 250 /* localhost: refused or accepted at once */
#define REPLY_MS 900   /* the forwarder replies within 700 ms (FWD_WAIT_MS) */
#define MAX_ASK 2      /* forwarders asked per lookup, at most */
#define MAX_FILES 32   /* port files looked at per lookup, at most */


struct answer {
	int n;
	int family[MAX_ADDRS];
	unsigned char addr[MAX_ADDRS][16];
};

static int port_of(const char *s)
{
	int p = 0;
	if (!*s)
		return -1;
	for (; *s; s++) {
		if (*s < '0' || *s > '9' || p > 65535)
			return -1;
		p = p * 10 + (*s - '0');
	}
	return p > 0 && p <= 65535 ? p : -1;
}

/* io_all: write or read with a deadline (ms); returns bytes or -1. */
static int wait_fd(int fd, short ev, int ms)
{
	struct pollfd p = { fd, ev, 0 };
	int r;
	do
		r = poll(&p, 1, ms);
	while (r < 0 && errno == EINTR);
	return r > 0 ? 0 : -1;
}

/* ask one forwarder: 1 answered (ok or not found), 0 failed or too slow,
 * -1 nothing listens there (a stale file: refused at once, doesn't count) */
static int ask_port(int port, const char *req, size_t reqlen, char *resp, size_t resplen)
{
	struct sockaddr_in sa;
	size_t off = 0;
	int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
	if (fd < 0)
		return 0;
	memset(&sa, 0, sizeof sa);
	sa.sin_family = AF_INET;
	sa.sin_port = htons((unsigned short)port);
	sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	/* Never slower than a moment: a dead forwarder is refused at once, and
	 * one that doesn't accept or answer in time is skipped (UNAVAIL: dns
	 * runs as before). The forwarder answers from its cache, or within
	 * FWD_WAIT_MS, and finishes slow lookups for the next asker. */
	if (connect(fd, (struct sockaddr *)&sa, sizeof sa) < 0) {
		int err = 0;
		socklen_t l = sizeof err;
		if (errno == ECONNREFUSED)
			goto refused;
		if (errno != EINPROGRESS || wait_fd(fd, POLLOUT, CONNECT_MS) < 0 ||
		    getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &l) < 0)
			goto fail;
		if (err == ECONNREFUSED)
			goto refused;
		if (err != 0)
			goto fail;
	}
	while (off < reqlen) {
		ssize_t w;
		if (wait_fd(fd, POLLOUT, CONNECT_MS) < 0)
			goto fail;
		w = write(fd, req + off, reqlen - off);
		if (w < 0 && errno == EINTR)
			continue;
		if (w <= 0)
			goto fail;
		off += (size_t)w;
	}
	off = 0;
	while (off + 1 < resplen) {
		ssize_t r;
		if (wait_fd(fd, POLLIN, REPLY_MS) < 0)
			goto fail;
		r = read(fd, resp + off, resplen - 1 - off);
		if (r < 0 && (errno == EINTR || errno == EAGAIN))
			continue;
		if (r <= 0)
			break;
		off += (size_t)r;
		if (memchr(resp, '\n', off))
			break;
	}
	close(fd);
	resp[off] = 0;
	return off > 0 && memchr(resp, '\n', off) != NULL;
fail:
	close(fd);
	return 0;
refused:
	close(fd);
	return -1;
}

/* lookup: NSS_STATUS_SUCCESS (a filled), NOTFOUND or UNAVAIL. */
static enum nss_status lookup(const char *name, int family, struct answer *a)
{
	char req[300], resp[2048];
	size_t nl = strlen(name);
	DIR *d;
	struct dirent *e;
	enum nss_status st = NSS_STATUS_UNAVAIL;

	a->n = 0;
	if (nl == 0 || nl > 253)
		return NSS_STATUS_NOTFOUND;
	req[0] = family == AF_INET ? '4' : family == AF_INET6 ? '6' : '0';
	req[1] = ' ';
	memcpy(req + 2, name, nl);
	req[2 + nl] = '\n';

	d = opendir(DIR_PATH);
	if (!d)
		return NSS_STATUS_UNAVAIL;
	int asked = 0, files = 0, got;
	/* Stale files (refused at once) don't count against MAX_ASK, so they
	 * never hide a live forwarder; slow ones do. */
	while ((e = readdir(d)) != NULL && asked < MAX_ASK && files < MAX_FILES) {
		int port = port_of(e->d_name);
		char *p, *tok;
		if (port < 0)
			continue;
		files++;
		got = ask_port(port, req, nl + 3, resp, sizeof resp);
		if (got < 0)
			continue; /* nothing listens: a stale file */
		asked++;
		if (!got)
			continue; /* failed or too slow: try the next */
		if (strncmp(resp, "NOTFOUND", 8) == 0) {
			st = NSS_STATUS_NOTFOUND;
			break;
		}
		if (strncmp(resp, "OK ", 3) != 0)
			continue; /* ERR: Android couldn't answer; maybe another can't either */
		p = resp + 3;
		while ((tok = strsep(&p, " \n")) != NULL && a->n < MAX_ADDRS) {
			if (!*tok)
				continue;
			if (inet_pton(AF_INET, tok, a->addr[a->n]) == 1)
				a->family[a->n++] = AF_INET;
			else if (inet_pton(AF_INET6, tok, a->addr[a->n]) == 1)
				a->family[a->n++] = AF_INET6;
		}
		st = a->n > 0 ? NSS_STATUS_SUCCESS : NSS_STATUS_NOTFOUND;
		break;
	}
	closedir(d);
	return st;
}

static char *take(char **buf, size_t *len, size_t n, size_t align)
{
	size_t pad = (align - ((size_t)*buf % align)) % align;
	char *r;
	if (*len < pad + n)
		return NULL;
	r = *buf + pad;
	*buf += pad + n;
	*len -= pad + n;
	return r;
}

static enum nss_status fail_with(enum nss_status st, int *errnop, int *herrnop)
{
	if (st == NSS_STATUS_NOTFOUND) {
		*errnop = ENOENT;
		*herrnop = HOST_NOT_FOUND;
	} else {
		*errnop = EAGAIN;
		*herrnop = NO_RECOVERY;
	}
	return st;
}

/* getaddrinfo's entry point */
enum nss_status _nss_andronix_gethostbyname4_r(const char *name, struct gaih_addrtuple **pat,
	char *buffer, size_t buflen, int *errnop, int *herrnop, int *ttlp)
{
	struct answer a;
	struct gaih_addrtuple *prev = NULL;
	char *hname;
	size_t nl = strlen(name) + 1;
	int i;
	enum nss_status st = lookup(name, AF_UNSPEC, &a);

	if (st != NSS_STATUS_SUCCESS)
		return fail_with(st, errnop, herrnop);
	if (!(hname = take(&buffer, &buflen, nl, 1)))
		goto range;
	memcpy(hname, name, nl);
	for (i = 0; i < a.n; i++) {
		struct gaih_addrtuple *t = (struct gaih_addrtuple *)take(&buffer, &buflen, sizeof *t, sizeof(void *));
		if (!t)
			goto range;
		memset(t, 0, sizeof *t);
		t->name = i == 0 ? hname : NULL;
		t->family = a.family[i];
		memcpy(t->addr, a.addr[i], a.family[i] == AF_INET ? 4 : 16);
		if (prev)
			prev->next = t;
		else
			*pat = t;
		prev = t;
	}
	if (ttlp)
		*ttlp = 60;
	return NSS_STATUS_SUCCESS;
range:
	*errnop = ERANGE;
	*herrnop = NETDB_INTERNAL;
	return NSS_STATUS_TRYAGAIN;
}

enum nss_status _nss_andronix_gethostbyname3_r(const char *name, int af, struct hostent *h,
	char *buffer, size_t buflen, int *errnop, int *herrnop, int *ttlp, char **canonp)
{
	struct answer a;
	int i, n = 0, alen = af == AF_INET6 ? 16 : 4;
	char *hname, **addrs, **aliases;
	size_t nl = strlen(name) + 1;
	enum nss_status st;

	if (af != AF_INET && af != AF_INET6) {
		*errnop = EAFNOSUPPORT;
		*herrnop = NO_DATA;
		return NSS_STATUS_UNAVAIL;
	}
	st = lookup(name, af, &a);
	if (st != NSS_STATUS_SUCCESS)
		return fail_with(st, errnop, herrnop);
	for (i = 0; i < a.n; i++)
		if (a.family[i] == af)
			n++;
	if (n == 0)
		return fail_with(NSS_STATUS_NOTFOUND, errnop, herrnop);
	if (!(hname = take(&buffer, &buflen, nl, 1)))
		goto range;
	memcpy(hname, name, nl);
	if (!(aliases = (char **)take(&buffer, &buflen, sizeof(char *), sizeof(char *))))
		goto range;
	aliases[0] = NULL;
	if (!(addrs = (char **)take(&buffer, &buflen, (size_t)(n + 1) * sizeof(char *), sizeof(char *))))
		goto range;
	n = 0;
	for (i = 0; i < a.n; i++) {
		char *p;
		if (a.family[i] != af)
			continue;
		if (!(p = take(&buffer, &buflen, (size_t)alen, 4)))
			goto range;
		memcpy(p, a.addr[i], (size_t)alen);
		addrs[n++] = p;
	}
	addrs[n] = NULL;
	h->h_name = hname;
	h->h_aliases = aliases;
	h->h_addrtype = af;
	h->h_length = alen;
	h->h_addr_list = addrs;
	if (ttlp)
		*ttlp = 60;
	if (canonp)
		*canonp = hname;
	return NSS_STATUS_SUCCESS;
range:
	*errnop = ERANGE;
	*herrnop = NETDB_INTERNAL;
	return NSS_STATUS_TRYAGAIN;
}

enum nss_status _nss_andronix_gethostbyname2_r(const char *name, int af, struct hostent *h,
	char *buffer, size_t buflen, int *errnop, int *herrnop)
{
	return _nss_andronix_gethostbyname3_r(name, af, h, buffer, buflen, errnop, herrnop, NULL, NULL);
}

enum nss_status _nss_andronix_gethostbyname_r(const char *name, struct hostent *h,
	char *buffer, size_t buflen, int *errnop, int *herrnop)
{
	return _nss_andronix_gethostbyname3_r(name, AF_INET, h, buffer, buflen, errnop, herrnop, NULL, NULL);
}
