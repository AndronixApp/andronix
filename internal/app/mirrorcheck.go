package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// Before the sources move to a fallback mirror, the mirror is asked for what
// apt will ask it for (card #399, support #5242). A mirror that stopped
// syncing still answers, with an index older than the packages the install
// already has from the distro's own server, and the next apt-get install
// ends in "unmet dependencies": mirrors.ocf.berkeley.edu/ubuntu-ports last
// synced on Nov 8 2026, 2.3 days before that report and 4.5 days before it
// was checked on Nov 12.

// mirrorMaxLag is how far behind the lists apt already has a mirror's
// Release file may be. A mirror that syncs twice a day reads as up to a day
// behind on a suite published once a day (the healthy ones in our lists
// were 0 to 23 hours behind on Nov 12 2026), so the limit is two days. A
// mirror inside it can still lack a package version the phone already has;
// unmet dependencies there send the sources back (backToPrimary).
const mirrorMaxLag = 48 * time.Hour

// mirrorProbeTimeout bounds the questions to one mirror. Its suites are
// asked about at the same time, so a slow link has the whole of it for each.
var mirrorProbeTimeout = 15 * time.Second

// mirrorProbe says why the sources under rootfs can't move from one base to
// another, "" when they can (a variable for tests).
var mirrorProbe = aptMirrorProblem

// debArch is dpkg's name for the CPU.
func debArch(a sys.Arch) string {
	switch a {
	case "aarch64":
		return "arm64"
	case "arm":
		return "armhf"
	case "i686":
		return "i386"
	case "x86_64":
		return "amd64"
	}
	return string(a)
}

// aptMirrorProblem checks, for every suite the sources take from `from`,
// that `to` has its Release file, that the file isn't older than the one apt
// has on disk by more than mirrorMaxLag, and that it carries this CPU's
// package index. With no suite to go by it has no objection (as before).
func aptMirrorProblem(ctx context.Context, rootfs, from, to string) string {
	suites := pkgmgr.AptSuites(rootfs, from)
	if len(suites) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, mirrorProbeTimeout)
	defer cancel()
	base := strings.TrimSuffix(to, "/") + "/dists/"
	arch := debArch(sys.DetectArch())
	why := make([]string, len(suites))
	var wg sync.WaitGroup
	for i, s := range suites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			why[i] = aptSuiteProblem(ctx, rootfs, from, base, arch, s)
		}()
	}
	wg.Wait()
	for _, w := range why {
		if w != "" {
			return w
		}
	}
	return ""
}

// aptSuiteProblem is aptMirrorProblem for one suite.
func aptSuiteProblem(ctx context.Context, rootfs, from, base, arch string, s pkgmgr.AptSuite) string {
	b, err := netx.Peek(ctx, base+s.Name+"/InRelease", 4096)
	if err != nil {
		return fmt.Sprintf("%s: no Release file (%s)", s.Name, pkgmgr.CleanDetail(err.Error()))
	}
	date, ok := pkgmgr.ReleaseDate(b)
	if !ok {
		// Not an archive's answer: a sign-in page, a filter.
		return s.Name + ": its Release file has no date"
	}
	if have := pkgmgr.AptListDate(rootfs, from, s.Name); !have.IsZero() && date.Before(have.Add(-mirrorMaxLag)) {
		return fmt.Sprintf("%s: out of date (its index is from %s, the one here from %s)", s.Name, date.Format("2 Jan 15:04"), have.Format("2 Jan 15:04"))
	}
	comp := s.Component
	if comp == "" {
		comp = "main"
	}
	code, _, err := netx.Head(ctx, base+s.Name+"/"+comp+"/binary-"+arch+"/Release")
	if err != nil {
		return fmt.Sprintf("%s: no %s package index (%s)", s.Name, arch, pkgmgr.CleanDetail(err.Error()))
	}
	if code != 200 {
		return fmt.Sprintf("%s: no %s package index (HTTP %d)", s.Name, arch, code)
	}
	return ""
}
