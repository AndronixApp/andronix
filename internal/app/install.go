package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	andronix "github.com/AndronixApp/andronix-distros"
	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/oci"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/rootfs"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/telemetry"
	"github.com/AndronixApp/andronix-distros/internal/termux"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// InstallOpts are the install command's flags.
type InstallOpts struct {
	Distro, Desktop    string
	NoBrowser, NoStart bool
	Reinstall, NoUser  bool
	// User sets up this user at the end, without questions (the
	// standalone app): its password and the optional VNC password, time
	// zone and keyboard come from ANDRONIX_USER_PASSWORD,
	// ANDRONIX_VNC_PASSWORD, ANDRONIX_TZ and ANDRONIX_KEYBOARD.
	User string
	// Modded installs a paid Modded edition: Token (from the app) signs a
	// products-api download of the edition's tarball.
	Modded bool
	Token  string
	// Edition is a Modded edition id (editions.conf), e.g. legacy-debian.
	// It implies Modded and picks the distro and desktop.
	Edition string
	// ImageBuild (ci/build-image.sh, on a Linux build host): run the
	// phone's package steps, add what the app installs lazily (Xvfb,
	// PulseAudio), and stop. The rootfs is then cleaned and exported as a
	// ready-made image (image.go). Not in help; refused on Android.
	ImageBuild bool
	report     *installReport
}

// DefaultMirror is the public URL of the Andronix R2 bucket (decided:
// R2 is the default source, the registry the fallback). The domain is a
// proposal until the bucket's custom domain is set up.
const DefaultMirror = "https://dl.andronix.app"

// Mirror is where CI publishes rootfs tarballs and binaries:
//
//	<mirror>/rootfs/<id>/<version>/<id>-<version>-<arch>.tar.xz (+ .sha256)
//	<mirror>/bin/latest/andronix-<arch>, <mirror>/bin/latest/SHA256SUMS
func Mirror() string {
	if m := os.Getenv("ANDRONIX_MIRROR"); m != "" {
		return strings.TrimRight(m, "/")
	}
	return DefaultMirror
}

type source struct {
	kind  string // local | tarball | registry
	file  string
	url   string
	size  int64
	sha   string
	image *oci.Image
	// unpacked is a ready-made image's unpacked size (its .meta), 0 if unknown.
	unpacked int64
	// sumURL is where a Modded download's sha256 comes from (the same
	// token check, <file>.sha256).
	sumURL string
	api    string // the products-api address in url and sumURL (Modded)
}

// Install is `andronix install`. It tells the Andronix app how the
// install went when the app asked (see report.go).
func Install(ctx context.Context, o InstallOpts) error {
	o.report = newInstallReport(o.Distro, o.Desktop, o.Modded || o.Edition != "")
	telemetry.Notice()
	err := install(ctx, o)
	if err != nil {
		o.report.err = err
		o.report.send("fail")
	}
	o.report.send("ok") // no-op if sent already (finish, or the failure above)
	return err
}

func install(ctx context.Context, o InstallOpts) error {
	rep := o.report
	var ed *conf.Edition
	if o.Edition != "" {
		var err error
		if ed, err = conf.ResolveEdition(o.Edition); err != nil {
			return ui.Errorf("unknown_edition", "Unknown edition '"+o.Edition+"'", "Andronix has no Modded edition called '"+o.Edition+"'.",
				"Copy the install command from the Andronix app again. Editions: "+strings.Join(conf.EditionIDs(), ", "))
		}
		o.Modded = true
		for _, f := range []struct {
			got        *string
			want, what string
		}{{&o.Distro, ed.Distro, "distro"}, {&o.Desktop, ed.Desktop, "desktop"}} {
			if *f.got == "" {
				*f.got = f.want
			} else if n := resolvedID(f.what, *f.got); n != f.want {
				return ui.Errorf("that_edition_doesn_t_match", "That edition doesn't match", "The "+ed.ID+" edition is "+ed.Distro+" with "+ed.Desktop+", not "+*f.got+".",
					"Copy the install command from the Andronix app again.")
			}
		}
	}
	// Pick a distro and desktop, asking when we can.
	if o.Distro == "" && ui.Interactive {
		var opts []ui.Option
		for _, id := range conf.DistroIDs() {
			d, _ := conf.LoadDistro(id)
			opts = append(opts, ui.Option{Label: d.Label(), Value: id})
		}
		v, err := ui.Choose("Which distro?", "You can install more later.", opts, "debian")
		if err != nil {
			return err
		}
		o.Distro = v
	}
	d, err := resolveDistro(o.Distro, "install")
	if err != nil {
		return err
	}
	rep.distro = d.ID
	// Reject a bad or reserved --user up front, before downloading anything:
	// a name like "shadow" or "audio" would otherwise fail useradd at the very
	// end of a finished install (#579). This is the static layer (no rootfs
	// yet); setupUserScripted re-checks against the unpacked passwd/group.
	if o.User != "" {
		if p := userNameProblem(o.User); p != "" {
			return &ui.UserError{Title: "Can't use that user name", Class: "bad_user",
				What: "'" + o.User + "': " + p + ".", Fix: "Install again with a different name, e.g. --user alex."}
		}
	}
	unlock, err := lockDistro(d, "install")
	if err != nil {
		return err
	}
	defer unlock()
	// Old app keys (LXDE, the window managers) map to a desktop we offer;
	// if that one isn't offered on this distro, XFCE is. A prefix or a
	// typo ("xfc") is forgiven, with a note.
	if de, notice, err := conf.ForgiveDesktop(o.Desktop); err == nil && notice != "" {
		if !de.Supports(d) && strings.Contains(notice, "any more") {
			de, _ = conf.ResolveDesktop("xfce")
			notice = o.Desktop + " isn't offered any more. Installing XFCE instead."
		}
		ui.WarnKey("corrected_desktop", notice)
		o.Desktop = de.ID
	}
	if o.Desktop == "" {
		o.Desktop = "xfce"
		if ui.Interactive {
			var opts []ui.Option
			for _, id := range conf.DesktopIDs() {
				de, _ := conf.ResolveDesktop(id)
				if !de.Supports(d) {
					continue
				}
				opts = append(opts, ui.Option{Label: de.Name, Value: id})
			}
			if o.Desktop, err = ui.Choose("Which desktop?", "XFCE is light and works well on phones.", opts, "xfce"); err != nil {
				return err
			}
		}
	}
	de, err := conf.ResolveDesktop(o.Desktop)
	if err != nil {
		what := "Andronix can't install '" + o.Desktop + "'."
		if _, _, ferr := conf.ForgiveDesktop(o.Desktop); ferr != nil && strings.Contains(ferr.Error(), "isn't offered") {
			what = ferr.Error() + "."
		}
		return &ui.UserError{Title: "Unknown desktop '" + o.Desktop + "'", What: what, Class: "unknown_desktop",
			Fix: "Choose one of: " + strings.Join(conf.DesktopIDs(), ", ") + ". XFCE is the lightest: --de xfce"}
	}
	rep.de = de.ID
	fam, err := pkgmgr.Get(d.Family)
	if err != nil {
		return err
	}
	if !de.Supports(d) {
		fix := "Try --de xfce, or --de none for the command line only."
		if on := de.SupportedOn(); len(on) > 0 {
			fix = de.Name + " is available on " + strings.Join(on, ", ") + ". On " + d.Label() + ", try --de xfce."
		}
		return &ui.UserError{Title: de.Name + " isn't available on " + d.Label(), What: d.Label() + " doesn't offer " + de.Name + " yet.",
			Fix: fix, Class: "desktop_not_offered"}
	}
	arch := sys.DetectArch()
	in := Open(d)
	deLabel := de.Name
	if o.ImageBuild {
		if sys.OnAndroid() {
			return ui.Errorf("not_on_a_phone", "Not on a phone", "--image-build makes ready-made images on a Linux build host.", "Run it in ci/build-image.sh.")
		}
		if de.None() {
			return ui.Errorf("no_desktop", "No desktop", "An image is for a desktop install.", "Pass --de xfce (or another desktop).")
		}
		// The browser is installed on the phone, never baked in (owner, 2026-10-05).
		o.NoBrowser, o.NoUser, o.NoStart = true, true, true
	}

	fmt.Print(ui.Banner("Linux on Android " + ui.GSep + " " + VersionLabel()))

	// The edition this install is: "" free, else a Modded edition id.
	edition := ""
	if ed != nil {
		edition = ed.ID
	} else if o.Modded {
		edition = d.ID + "-" + de.ID
	}
	have := installedEdition(in)
	if in.Installed() && !o.Reinstall && have != edition && (o.Modded || have != "") {
		// Another edition is there (free Kali, and the app's Modded Kali
		// command): not a failure. Ask what to do, or say both ways.
		name := func(e string) string {
			if e == "" {
				return "the free edition"
			}
			return editionName(e) + " (" + e + ")"
		}
		replace := false
		if ui.Interactive && !ui.Yes {
			v, err := ui.Choose(d.Label()+" is already installed: "+name(have)+".", "This command installs "+name(edition)+".",
				[]ui.Option{{Label: "Keep it and open it (./" + d.MainStart() + ")", Value: "keep"},
					{Label: "Replace it with " + name(edition) + " (deletes everything inside it)", Value: "replace"}}, "keep")
			if err != nil {
				return err
			}
			replace = v == "replace"
		}
		if !replace {
			rep.outcome = "already_installed"
			ui.SetDone("outcome", "already_installed")
			ui.SetDone("distro", d.ID)
			fmt.Print(ui.Box(ui.BoxOK, d.Label()+" is already installed",
				ui.KV("Installed", name(have)), ui.KV("Start", "./"+d.MainStart()), "",
				"To replace it with "+name(edition)+", run the same command with --reinstall. That deletes everything inside it; back it up first: andronix backup "+d.ID))
			fmt.Println()
			return nil
		}
		o.Reinstall = true
	}
	if in.Installed() && !o.Reinstall && (in.Get("DE") == de.ID || de.None()) {
		rep.outcome = "already_installed"
		ui.SetDone("outcome", "already_installed")
		ui.SetDone("distro", d.ID)
		WriteLaunchers(d)             // refresh in case andronix moved
		rootfs.InstallSelf(in.Rootfs) // and keep the in-distro helper current
		if cur, err := conf.ResolveDesktop(in.Get("DE")); err == nil && !cur.None() {
			writeXstartup(in.Rootfs, cur)
		}
		fmt.Print(ui.Box(ui.BoxOK, d.Label()+" is already installed",
			ui.KV("Desktop", in.Get("DE_NAME")), ui.KV("Start", "./"+d.MainStart()), "",
			"To start over, run: andronix install "+d.ID+" --reinstall"))
		fmt.Println()
		return nil
	}
	if o.Reinstall {
		// A Modded reinstall from the app's Reinstall button can carry a
		// token hours old: refuse an expired one before deleting anything.
		if edition != "" && tokenExpired(o.Token, time.Now()) {
			return &ui.UserError{Title: "Download link expired", Class: "token_refused",
				What: "The download token in this command has expired, so nothing was deleted: " + d.Label() + " is as it was.",
				Fix:  "Copy a fresh install command from the Andronix app and run it again."}
		}
		if _, err := os.Stat(in.Dir); err == nil {
			fmt.Print(ui.Box(ui.BoxWarn, "Reinstall "+d.Label()+"?",
				"This deletes everything inside "+d.Label()+", including your files in it. Files in /sdcard are safe."))
			ok, err := ui.Confirm("Delete it and install again?", "", false)
			if err != nil || !ok {
				ui.Note("Nothing changed.")
				return err
			}
			if err := rootfs.Remove(in.Dir); err != nil {
				return ui.Errorf("couldn_t_delete_the_old_files", "Couldn't delete the old files", err.Error(), "Exit every session of "+d.Name+", then try again.")
			}
		}
	}
	// 'Command line only' over an unfinished desktop install (owner, #177):
	// resuming would keep its desktop, so start over from the base system.
	restarted := ""
	if de.None() && edition == "" && !o.Reinstall && desktopLeft(in) {
		restarted = fmt.Sprintf("PREBUILT=%q DE=%q STAGE=%q", in.Get("PREBUILT"), in.Get("DE"), in.Get("STAGE"))
		if err := rootfs.Remove(in.Dir); err != nil {
			return ui.Errorf("couldn_t_delete_the_old_files", "Couldn't delete the old files", err.Error(), "Exit every session of "+d.Name+", then try again.")
		}
	}
	haveRootfs := false
	switch in.Get("STAGE") {
	case "configured", "desktop", "done":
		if _, err := os.Stat(in.Rootfs); err == nil {
			haveRootfs = true
		}
	}

	fmt.Print(ui.Box(ui.BoxBrand, d.Label()+" "+ui.GSep+" "+deLabel,
		ui.KV("CPU", arch.Label()), ui.KV("Desktop", deLabel), ui.KV("Location", ui.Tilde(in.Dir))))
	fmt.Println()
	if sys.Standalone() {
		ui.NoteKey("install_time", "This takes 5 to 30 minutes on a phone. Andronix keeps working while it runs in the background.")
	} else {
		ui.Note("Keep Termux open while this runs; it takes 5 to 30 minutes on a phone. Android may ask whether Termux can run in the background: tap Allow, so the install isn't stopped.")
	}
	// This phone's compatibility rules (compat.json): what's known before
	// the probe (kernel, Android, Termux, a cached probe).
	cr := newCompat(d, de.ID)
	rep.cr = cr
	shown := map[string]bool{}
	if sys.OnAndroid() {
		cr.warnings(shown)
		if err := cr.refuse(d); err != nil {
			return err
		}
		if err := appLibCheck(); err != nil {
			return err
		}
	}
	fmt.Println()

	lg := NewLog("install-" + d.ID)
	defer lg.Close()
	lg.Printf("andronix %s install %s de=%s arch=%s termux=%v", Version, d.ID, de.ID, arch, sys.IsTermux())
	if restarted != "" {
		lg.Printf("command line only over an unfinished desktop install (%s): started over", restarted)
	}
	if sys.IsTermux() {
		sys.Command("termux-wake-lock").Run()
		defer sys.Command("termux-wake-unlock").Run()
	}

	paths := GetPaths()
	os.MkdirAll(paths.Cache, 0o755)
	var src source
	var lowRAM int64
	// The image's fallback reason (image.go), "" when one was used or none tried.
	imgFallback := ""
	imageUnpacked := false
	t := in.Target(true)
	run := func(r ui.Reporter, cmd string) error {
		return t.Run(ctx, cmd, nil, func(l string) { r.Line(l) })
	}
	ops := newPkgOps(t, fam, d, lg) // retries, mirror fallback, repairs

	var steps []ui.Step
	// proot comes from Termux's packages: install it here (with pkg's lock
	// and its recovery for a half-upgraded Termux) rather than asking.
	if _, err := sys.LookPath("proot"); err != nil && sys.IsTermux() {
		steps = append(steps, ui.Step{Key: "proot", Label: "Installing proot", Run: func(ctx context.Context, r ui.Reporter) error {
			if err := termux.Install(ctx, r.Line, "proot"); err != nil {
				return termuxPkgErr("Couldn't install proot", "pkg upgrade -y && pkg install proot -y", err)
			}
			return nil
		}})
	}
	steps = append(steps, []ui.Step{
		{Key: "phone-check", Label: "Checking your phone", Run: func(ctx context.Context, r ui.Reporter) error {
			if !proot.Available() {
				if sys.Standalone() {
					return &ui.UserError{Title: "proot is missing", Class: "proot_is_missing",
						What: "The Andronix app didn't pass a usable proot (PROOT_BIN).", Fix: "Update or reinstall the Andronix app."}
				}
				if !sys.IsTermux() {
					return &ui.UserError{Title: "proot is missing", Class: "proot_is_missing",
						What: "This doesn't look like a normal Termux session ($PREFIX isn't set), so Andronix can't install proot itself.",
						Fix:  "Open the Termux app, then run the same command there."}
				}
				return &ui.UserError{Title: "proot is missing", Class: "proot_is_missing", What: "Andronix needs proot to run Linux inside Termux.",
					Fix: "Install it, then run the same command again: pkg install proot -y"}
			}
			if d.MaxKernel != "" && kernelNewer(sys.KernelRelease(), d.MaxKernel) {
				return ui.Errorf("kernel_too_new", d.Label()+" can't run on this phone yet",
					d.Name+" needs an Android kernel up to "+d.MaxKernel+"; this phone has "+sys.KernelRelease()+". A fix in proot is in progress.",
					"Pick another distro for now, e.g. Debian or Alpine.")
			}
			if !d.HasArch(string(arch)) {
				return ui.Errorf("no_build_for_your_cpu", d.Label()+" isn't available here", d.Label()+" has no build for "+arch.Label()+" phones.",
					"Pick another distro in the Andronix app, e.g. Debian.")
			}
			need := spaceNeed(d, de, haveRootfs)
			free := sys.FreeMB(sys.Home())
			lg.Printf("free=%dMB need=%dMB", free, need)
			if free >= 0 && free < need && sys.AndroidSDK() == 0 {
				// Termux's userland on a PC (termux-docker): no Android to
				// read, and its free space isn't a phone's (Oct 6: 21
				// 'not enough space' failures from one x86_64 box).
				return &ui.UserError{Title: "This doesn't look like an Android phone", Class: "not_android",
					What: fmt.Sprintf("Andronix runs on Android phones. Here Android's version can't be read, and the free space reads %d MB (%s with %s needs about %d MB).", free, d.Label(), deLabel, need),
					Fix:  "Use the Andronix app on an Android phone (or, on the command line, Termux on the phone). On a PC, run the distro there directly."}
			}
			if free >= 0 && free < need {
				return &ui.UserError{Title: "Not enough space", Class: "not_enough_space",
					What: fmt.Sprintf("Your phone has %d MB free; %s with %s needs about %d MB (with some to spare).", free, d.Label(), deLabel, need),
					Fix:  fmt.Sprintf("Free up about %d MB (old videos, app caches), then run the same command again.", need-free)}
			}
			if de.MinRAMMB > 0 {
				if ram := sys.TotalRAMMB(); ram > 0 && ram < int64(de.MinRAMMB) {
					lg.Printf("ram=%dMB below %s's %dMB", ram, de.Name, de.MinRAMMB)
					ui.DetailOf(r, "arch.low_ram", fmt.Sprintf("%s · only %d MB RAM", arch.Label(), ram), map[string]any{"arch": string(arch), "ram_mb": ram})
					lowRAM = ram
				}
			}
			if lowRAM == 0 {
				ui.DetailOf(r, "arch", arch.Label(), map[string]any{"arch": string(arch)})
			}
			os.MkdirAll(in.Dir, 0o755)
			in.Set("DISTRO", d.ID)
			in.Set("VERSION", d.Version)
			in.Set("ARCH", string(arch))
			in.Set("EDITION", edition)
			return nil
		}},
		{Key: "find-download", Label: "Finding the best download", Run: func(ctx context.Context, r ui.Reporter) error {
			if haveRootfs {
				return ui.Skip("already downloaded")
			}
			var s source
			var err error
			if o.Modded && os.Getenv("ANDRONIX_ROOTFS") == "" {
				if s, err = moddedSource(d, de, arch, paths, o.Token, edition); err == nil {
					if err = moddedChecksum(ctx, &s, lg, r.Line); err != nil {
						if e := earlyErr(err, ed); e != nil {
							err = e
						}
					}
				}
			} else {
				// A ready-made image first (image.go), else the base rootfs.
				if imageEligible(o, edition, de) {
					img, reason := findImage(ctx, d, de, arch, lg)
					if reason == "" {
						src = img
						ui.DetailOf(r, "source.image", "ready-made image, "+ui.Bytes(img.size), map[string]any{"bytes": img.size})
						return nil
					}
					imgFallback = reason
					lg.Printf("image: %s, using the package install", reason)
				}
				s, err = pickSource(ctx, d, arch, paths, lg, r.Line)
			}
			if err != nil {
				return err
			}
			src = s
			switch s.kind {
			case "local":
				ui.DetailOf(r, "source.local", "local file", nil)
			case "tarball":
				ui.DetailOf(r, "source.mirror", "Andronix mirror, "+ui.Bytes(s.size), map[string]any{"bytes": s.size})
			case "upstream":
				ui.DetailOf(r, "source.upstream", d.Name+"'s own download, "+ui.Bytes(s.size), map[string]any{"distro": d.Name, "distro_id": d.ID, "bytes": s.size})
			case "modded":
				ui.DetailOf(r, "source.edition", editionName(edition), map[string]any{"edition": edition})
			default:
				ui.DetailOf(r, "source.official", "official image, "+ui.Bytes(s.image.Total), map[string]any{"bytes": s.image.Total})
			}
			return nil
		}},
		{Key: "download", Label: downloadLabel(d, de, edition), Run: func(ctx context.Context, r ui.Reporter) error {
			if haveRootfs || src.kind == "local" {
				return ui.Skip("nothing to download")
			}
			// Before spending a long download, check the phone can hold the
			// compressed file and the unpacked base rootfs at the same time
			// (card #301: free space is ~23% of install failures; the
			// phone-check before this ran without the download size, yet a
			// stored tarball sits on disk while it unpacks). The image path
			// streams into the rootfs with no stored copy, so it is exempt.
			if (src.kind == "tarball" || src.kind == "upstream") && src.size > 0 {
				dlMB := (src.size + (1 << 20) - 1) >> 20
				peak := downloadPeakMB(d, src.size)
				if free := sys.FreeMB(sys.Home()); free >= 0 && free < peak {
					lg.Printf("pre-download space: free=%dMB < download %dMB + unpacked base %dMB", free, dlMB, peak-dlMB)
					return &ui.UserError{Title: "Not enough space", Class: "not_enough_space",
						What: fmt.Sprintf("Your phone has %d MB free; downloading and unpacking %s needs about %d MB at once (a %d MB download that stays on disk while the system unpacks).", free, d.Label(), peak, dlMB),
						Fix:  fmt.Sprintf("Free up about %d MB (old videos, app caches), then run the same command again.", peak-free)}
				}
			}
			if src.kind == "image" {
				// Downloaded straight into the rootfs (no copy on disk), hashed
				// on the way; on any failure, wipe it and fall back silently.
				rootfs.Remove(in.Rootfs)
				os.MkdirAll(in.Rootfs, 0o755)
				x := &rootfs.Extractor{Root: in.Rootfs}
				n, reason, err := unpackImage(ctx, src, x, func(c, t int64) { r.Progress(c, t, "bytes") })
				rep.downloadBytes += n // all bytes fetched, a failed image's included
				if reason == fbNoSpace {
					// Disk full mid-unpack: abort with the clear result, don't
					// fall back to a package install that would also run out (#1147).
					lg.Printf("image: out of space while unpacking: %v", err)
					return notEnoughSpace(d, de, haveRootfs)
				}
				if reason == "" {
					x.Finish()
					for _, w := range x.Warnings {
						lg.Printf("extract: %s", w)
					}
					// Checksum fine but no usable system: a bad build. Same silent
					// fallback as any other image failure (review).
					if _, serr := os.Lstat(filepath.Join(in.Rootfs, "etc/passwd")); serr != nil {
						reason, err = fbUnpack, errors.New("the image has no etc/passwd")
					}
				}
				if reason == "" {
					imageUnpacked = true
					ui.DetailOf(r, "verify.image", "ready-made image, sha256 checked", nil)
					return nil
				}
				lg.Printf("image: %s: %v; using the package install", reason, err)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				imgFallback = reason
				rootfs.Remove(in.Rootfs)
				base, err := pickSource(ctx, d, arch, paths, lg, r.Line)
				if err != nil {
					return err
				}
				src = base
				r.Label(downloadLabel(d, de, edition))
				if src.kind == "local" {
					return nil
				}
			}
			if src.kind == "tarball" || src.kind == "upstream" || src.kind == "modded" {
				part := src.file + ".part"
				sum, err := netx.Download(ctx, src.url, part, nil, src.size, func(c, t int64) { r.Progress(c, t, "bytes") })
				rep.downloadBytes += src.size
				if err != nil {
					if e := earlyErr(err, ed); e != nil {
						return e
					}
					return downloadErr(err, src.kind == "modded")
				}
				if src.sha != "" && sum != src.sha {
					os.Remove(part)
					return ui.Errorf("download_is_damaged", "Download is damaged", "The file's checksum doesn't match, so it was deleted.", "Run the same command again to download it fresh.")
				}
				ui.DetailOf(r, "verify.sha256", "sha256 checked", nil)
				return os.Rename(part, src.file)
			}
			// Registry layers, each verified against its digest.
			dir := filepath.Join(paths.Cache, "layers-"+d.ID)
			os.MkdirAll(dir, 0o755)
			var done int64
			for _, l := range src.image.Layers {
				dst := filepath.Join(dir, strings.TrimPrefix(l.Digest, "sha256:"))
				if sum, _ := netx.FileSHA256(dst); "sha256:"+sum == l.Digest {
					done += l.Size
					continue
				}
				base := done
				blob := func() (string, error) {
					return netx.Download(ctx, src.image.BlobURL(l), dst+".part", src.image.Headers(), l.Size,
						func(c, _ int64) { r.Progress(base+c, src.image.Total, "bytes") })
				}
				sum, err := blob()
				var se *netx.StatusError
				if errors.As(err, &se) && se.Code == 401 {
					// The registry (docker.io) bearer expired mid-pull, not
					// the user's token (#243/#658): refresh it and resume the
					// blob from the .part (netx.Download Ranges).
					lg.Printf("registry blob %s: 401, refreshing bearer", l.Digest)
					if rerr := src.image.RefreshToken(ctx); rerr == nil {
						sum, err = blob()
					}
				}
				if err != nil {
					// A 401 that survives the refresh is a plain download
					// failure here, never the user's "token refused".
					return downloadErr(err, false)
				}
				if "sha256:"+sum != l.Digest {
					os.Remove(dst + ".part")
					return ui.Errorf("download_is_damaged", "Download is damaged", "A layer's checksum doesn't match, so it was deleted.", "Run the same command again to download it fresh.")
				}
				os.Rename(dst+".part", dst)
				done += l.Size
			}
			ui.DetailOf(r, "verify.digests", "digests checked", nil)
			return nil
		}},
		{Key: "unpack", Label: "Unpacking files", Run: func(ctx context.Context, r ui.Reporter) error {
			if haveRootfs {
				return ui.Skip("already unpacked")
			}
			if imageUnpacked {
				// Unpacked while downloading. Without the desktop's session it
				// isn't the image it claims to be: the package steps fill it in.
				pb := "image"
				if !sessionPresent(in, de) {
					pb, imgFallback = "no", fbNoDesk
				}
				in.Set("PREBUILT", pb)
				if _, err := os.Lstat(filepath.Join(in.Rootfs, "etc/passwd")); err != nil {
					return ui.Errorf("unpacking_failed", "Unpacking failed", "The download doesn't contain a usable system.", "Run the same command again with --reinstall.")
				}
				in.Set("STAGE", "extracted")
				return ui.Skip("unpacked while downloading")
			}
			rootfs.Remove(in.Rootfs)
			os.MkdirAll(in.Rootfs, 0o755)
			x := &rootfs.Extractor{Root: in.Rootfs, Progress: func(c, t int64) { r.Progress(c, t, "bytes") }}
			var files []string
			if src.kind == "registry" {
				x.Layers = true
				x.Total = src.image.Total
				for _, l := range src.image.Layers {
					files = append(files, filepath.Join(paths.Cache, "layers-"+d.ID, strings.TrimPrefix(l.Digest, "sha256:")))
				}
			} else { // local, tarball or upstream: one archive
				st, _ := os.Stat(src.file)
				if st != nil {
					x.Total = st.Size()
				}
				files = []string{src.file}
			}
			for _, f := range files {
				if err := x.ExtractFile(f); err != nil {
					if errors.Is(err, syscall.ENOSPC) {
						// Stopped on the first out-of-space write, not left
						// half-unpacked to fail later at "Setting up" (#1147).
						lg.Printf("unpack: out of space: %v", err)
						return notEnoughSpace(d, de, haveRootfs)
					}
					return ui.Errorf("unpacking_failed", "Unpacking failed", "The files couldn't be unpacked: "+err.Error(), "Free up some space, then run the same command again.")
				}
			}
			x.Finish()
			for _, w := range x.Warnings {
				lg.Printf("extract: %s", w)
			}
			if _, err := os.Lstat(filepath.Join(in.Rootfs, "etc/passwd")); err != nil {
				return ui.Errorf("unpacking_failed", "Unpacking failed", "The download doesn't contain a usable system.", "Run the same command again with --reinstall.")
			}
			if src.kind == "upstream" {
				// Not cleaned by our CI: strip kernel/firmware packages, default
				// users and passwords, and identity, like ci/build-rootfs.sh.
				if rm := d.UpstreamRemove(); len(rm) > 0 && fam.ID == "pacman" {
					run(r, "pkgs=$(pacman -Qq "+strings.Join(rm, " ")+" 2>/dev/null); [ -z \"$pkgs\" ] || pacman -Rns --noconfirm $pkgs; rm -rf /boot/*")
				}
				if err := run(r, rootfs.CleanScript); err != nil {
					lg.Printf("upstream clean: %v", err)
				}
			}
			// Whether the image itself has the desktop, before any package
			// run: a desktop install that stopped part way leaves the
			// session binary too, and mustn't count as prebuilt on resume.
			pb := "no"
			if sessionPresent(in, de) {
				pb = "yes"
			}
			in.Set("PREBUILT", pb)
			return in.Set("STAGE", "extracted")
		}},
		{Key: "setup", Label: "Setting up " + d.Label(), Run: func(ctx context.Context, r ui.Reporter) error {
			// Measure this phone inside the rootfs, then apply the fixes
			// (idempotent: every install and resume runs it).
			compatFix := func(ctx context.Context) error {
				if !sys.OnAndroid() {
					return nil
				}
				if err := cr.probe(ctx, t, lg); err != nil {
					return err
				}
				if err := cr.refuse(d); err != nil {
					return err
				}
				cr.apply(in.Rootfs, arch, lg)
				return in.Set("COMPAT", strings.Join(cr.plan.IDs, " "))
			}
			if in.Get("STAGE") != "extracted" && haveRootfs {
				rootfs.InstallSelf(in.Rootfs) // keep the in-distro helper current
				// A resumed install (maybe begun by an older andronix) still
				// gets this phone's fixes before the package steps.
				if err := compatFix(ctx); err != nil {
					return err
				}
				return ui.Skip("already set up")
			}
			edition := "free"
			if moddedImage(in) || o.Modded {
				edition = "modded"
			}
			if err := rootfs.Configure(in.Dir, in.Rootfs, rootfs.Info{ID: d.ID, Label: d.Label(), Start: d.MainStart(),
				Version: Version, Family: d.Family, Edition: edition}); err != nil {
				return ui.Errorf("setup_failed", "Setup failed", "Couldn't write settings into "+d.Label()+": "+err.Error(), "Run the same command again.")
			}
			if fam.Configure != nil {
				if err := fam.Configure(in.Rootfs); err != nil {
					lg.Printf("family configure: %v", err)
				}
			}
			if d.NoSnap {
				pkgmgr.NoSnap(in.Rootfs)
			}
			pkgmgr.SetMirror(in.Rootfs, d.Family, d.MirrorFor(string(arch)))
			if err := compatFix(ctx); err != nil {
				return err
			}
			if fam.FchmodatShim {
				if err := installShim(in.Rootfs, arch); err != nil {
					lg.Printf("fchmodat shim: %v", err)
				}
			}
			if err := pkgmgr.RewriteSources(in.Rootfs, d.MirrorRewrite[0], d.MirrorRewrite[1]); err != nil {
				lg.Printf("mirror rewrite: %v", err)
			}
			return in.Set("STAGE", "configured")
		}},
		{Key: "refresh", Label: "Refreshing package lists", Run: func(ctx context.Context, r ui.Reporter) error {
			if err := ops.update(ctx, r); err != nil {
				return pkgErr(refreshTitle, err)
			}
			return cr.preScripts(ctx, t, r)
		}},
	}...)
	// Its own step, so a failure there names it (support #147: Ubuntu
	// 26.04's coreutils switch failed as 'Refreshing package lists').
	if d.PreUpgrade != "" {
		steps = append(steps, ui.Step{Key: "prepare", Label: "Preparing " + d.Label() + " for updates", Run: func(ctx context.Context, r ui.Reporter) error {
			return preUpgrade(ctx, ops, d, r)
		}})
	}
	steps = append(steps, []ui.Step{
		{Key: "update", Label: "Updating " + d.Label(), Run: func(ctx context.Context, r ui.Reporter) error {
			if prebuilt(in, de) {
				return ui.Skip("the image is up to date")
			}
			n := count(ctx, t, fam.SimUpgrade, fam)
			if n == 0 {
				return ui.Skip("already up to date")
			}
			r.Label(fmt.Sprintf("Updating %s", d.Label()))
			if err := ops.run(ctx, fam.Upgrade, n, r); err != nil {
				return pkgErr("Couldn't update "+d.Name, err)
			}
			run(r, fam.Clean) // the upgrade's downloads go before the desktop's come (support #158)
			ui.DetailOf(r, "packages", packages(n), map[string]any{"count": n})
			return nil
		}},
		{Key: installKey(de), Label: installLabel(de), Run: func(ctx context.Context, r ui.Reporter) error {
			// A prebuilt image (a Modded edition, or a rootfs exported with
			// its desktop) already has everything: skip the package run.
			if prebuilt(in, de) || imageInstalled(in, de) {
				return ui.Skip("already in the image")
			}
			pkgs := append([]string{}, d.BasePkgs...)
			if !de.None() {
				pkgs = append(pkgs, de.Packages(d.Family)...)
				// VNC packages: install only those the distro's archive
				// actually has. A renamed or dropped supplementary package
				// (Ubuntu 26.04 "resolute" dropped tigervnc-tools; the
				// server stayed tigervnc-standalone-server) must not fail
				// the whole desktop with "Unable to locate package" (#1625).
				// If none resolve, keep the original list so the install
				// errors by name; either way the Xvnc/vncpasswd check in
				// setupDesktop still fails loudly if no server installs.
				var vncHave []string
				t.Run(ctx, fam.FilterAvailable(fam.VNC), nil, func(l string) {
					if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, " ") {
						vncHave = append(vncHave, l)
					}
				})
				pkgs = append(pkgs, vncPackages(fam.VNC, vncHave)...)
				// Optional packages: only those this distro has.
				var opt []string
				for _, o := range de.OptionalPackages(d.Family) {
					if !cr.plan.SkipOptional[o] {
						opt = append(opt, o)
					}
				}
				if len(opt) > 0 {
					t.Run(ctx, fam.FilterAvailable(opt), nil, func(l string) {
						if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, " ") {
							pkgs = append(pkgs, l)
						}
					})
				}
			}
			// The app's display and sound (Xvfb, PulseAudio) in the same run, so the
			// first desktop start doesn't install them (about a minute there).
			extras := standaloneExtras(d, de, sys.Standalone())
			n := count(ctx, t, fam.SimInstall(append(pkgs, extras...)), fam)
			err := ops.run(ctx, fam.Install(append(pkgs, extras...)), n, r)
			if err != nil && len(extras) > 0 {
				// The desktop never fails because of them: without them, the first
				// display start installs them (ensureXvfb, ensurePulse) as before.
				lg.Printf("install with %s failed (%v); again without them", strings.Join(extras, " "), err)
				n = count(ctx, t, fam.SimInstall(pkgs), fam)
				err = ops.run(ctx, fam.Install(pkgs), n, r)
			}
			if err != nil {
				return pkgErr("Couldn't install "+strings.ToLower(installLabel(de)[len("Installing "):]), err)
			}
			if n > 0 {
				ui.DetailOf(r, "packages", packages(n), map[string]any{"count": n})
			}
			run(r, fam.Clean)
			return nil
		}},
		{Key: "browser", Label: "Installing a web browser", Run: func(ctx context.Context, r ui.Reporter) error {
			b := d.BrowserFor(string(arch))
			switch {
			case de.None() || o.NoBrowser:
				return ui.Skip("not requested")
			case b == "":
				return ui.Skip("none packaged for " + arch.Label())
			case prebuilt(in, de) && (moddedImage(in) || hasAny(in.Rootfs, "usr/bin/"+b, "usr/bin/firefox", "usr/lib/firefox")):
				return ui.Skip("already in the image")
			}
			r.Label("Installing " + strings.Title(strings.TrimSuffix(b, "-esr")))
			if err := browserRepo(ctx, d, string(arch), in.Rootfs, t, fam); err != nil {
				lg.Printf("mozilla repo: %v", err)
				return ui.Skip("couldn't reach Mozilla; install it later")
			}
			n := count(ctx, t, fam.SimInstall([]string{b}), fam)
			if err := ops.run(ctx, fam.Install([]string{b}), n, r); err != nil {
				lg.Printf("browser: %v", err)
				return ui.Skip("failed; try later: install " + b)
			}
			installCodecs(ctx, t, fam, d, r, lg)
			run(r, fam.Clean)
			return nil
		}},
		{Key: "desktop-setup", Label: "Setting up the desktop", Run: func(ctx context.Context, r ui.Reporter) error {
			if de.None() {
				return ui.Skip("command line only")
			}
			// Images ship without SSH host keys; make this phone's own if
			// an SSH server is present.
			run(r, "[ -d /etc/ssh ] && command -v ssh-keygen >/dev/null && ssh-keygen -A >/dev/null 2>&1; true")
			if err := setupDesktop(ctx, in, de, t, r); err != nil {
				return ui.Errorf("desktop_setup_failed", "Desktop setup failed", err.Error(), "Run the same command again.")
			}
			// A user from an earlier command-line install gets the session files too (#284).
			if n := userSessionFiles(in); n > 0 {
				lg.Printf("desktop: copied %d files from /etc/skel to the existing user's home", n)
			}
			in.Set("DE", de.ID)
			in.Set("DE_NAME", deLabel)
			rootfs.SetRelease(in.Rootfs, map[string]string{"ANDRONIX_DE": de.ID})
			return in.Set("STAGE", "desktop")
		}},
		{Key: "finish", Label: finishLabel(d), Run: func(ctx context.Context, r ui.Reporter) error {
			if de.None() {
				in.Set("DE", "none")
				in.Set("DE_NAME", "Command line")
				rootfs.SetRelease(in.Rootfs, map[string]string{"ANDRONIX_DE": "none"})
			}
			// Termux's start scripts (./start-debian.sh and the old names);
			// the standalone app starts distros itself, so none there.
			if !sys.Standalone() {
				renamed, err := WriteLaunchers(d)
				if err != nil {
					return ui.Errorf("couldn_t_write_the_start_script", "Couldn't write the start script", err.Error(), "Check that Termux can write to its home folder.")
				}
				in.Set("RENAMED", strings.Join(renamed, " "))
			}
			in.Set("SOURCE", src.kind)
			in.Set("FALLBACK", imgFallback)
			in.Set("STAGE", "done")
			os.RemoveAll(filepath.Join(paths.Cache, "layers-"+d.ID))
			// The image isn't needed any more (a Modded or Classic one
			// never stays on the phone longer than the install).
			if src.kind == "tarball" || src.kind == "modded" || src.kind == "upstream" {
				os.Remove(src.file)
			}
			return nil
		}},
	}...)
	if o.ImageBuild {
		// Up to the package steps, plus what the app installs lazily; the
		// rest (desktop setup, launchers, the user) runs on the phone.
		for i, st := range steps {
			if st.Key == "browser" {
				steps = steps[:i]
				break
			}
		}
		steps = append(steps, ui.Step{Key: "image-extras", Label: "Adding Xvfb and PulseAudio", Run: func(ctx context.Context, r ui.Reporter) error {
			var pkgs []string
			for _, p := range []string{d.Xvfb(), d.Pulse()} {
				if p != "" {
					pkgs = append(pkgs, p)
				}
			}
			if len(pkgs) == 0 {
				return ui.Skip("none for " + d.Label())
			}
			n := count(ctx, t, fam.SimInstall(pkgs), fam)
			if err := ops.run(ctx, fam.Install(pkgs), n, r); err != nil {
				return pkgErr("Couldn't install Xvfb and PulseAudio", err)
			}
			run(r, fam.Clean)
			// The package list, next to the rootfs (not in it), for the image's .manifest.
			if q := imageManifestCmd(d.Family); q != "" {
				var lines []string
				t.Run(ctx, q, nil, func(l string) {
					if l = strings.TrimSpace(l); l != "" {
						lines = append(lines, l)
					}
				})
				sort.Strings(lines)
				os.WriteFile(filepath.Join(in.Dir, "image-manifest.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
			}
			return nil
		}})
	}
	for i := range steps {
		label, key, run := steps[i].Label, steps[i].Key, steps[i].Run
		steps[i].Run = func(ctx context.Context, r ui.Reporter) error {
			rep.step, rep.stepKey = label, ui.StepKey(key, label) // the failed step, for the app
			t0 := time.Now()
			err := run(ctx, r)
			ms := time.Since(t0).Milliseconds()
			if len(rep.steps) < 20 {
				rep.steps = append(rep.steps, fmt.Sprintf("%s:%d", ui.StepKey(key, label), ms))
			}
			switch key {
			case "download":
				rep.downloadMS += ms
			case "unpack":
				rep.unpackMS += ms
			}
			return err
		}
	}
	telemetry.Send("install_started", rep.ids(map[string]any{"distro": d.ID, "de": de.ID, "modded": o.Modded, "edition": edition}))
	if err := ui.RunSteps(ctx, steps, lg); err != nil {
		if ue, ok := err.(*ui.UserError); ok && ue.Log == "" {
			ue.Log = lg.Path
		}
		return err
	}
	rep.step, rep.stepKey = "", ""
	if o.ImageBuild {
		ui.SetDone("distro", d.ID)
		return nil
	}
	if !de.None() {
		in.Set("FIRST_DESKTOP", rep.attemptID) // desktop_start says first, once
	}
	// products-api takes source image|packages only.
	rep.source, rep.fallback = "packages", imgFallback
	if src.kind == "image" {
		rep.source = "image"
	}

	ui.SetDone("distro", d.ID)
	if edition != "" {
		ui.SetDone("edition", edition)
	}
	// A normal user instead of root (asks here; otherwise at first login).
	user := in.Get("USER")
	noStart, userStopped := o.NoStart, false
	if o.User != "" {
		ui.Emit("step", map[string]any{"key": "user", "label": "Setting up " + o.User})
		t0 := time.Now()
		err := setupUserScripted(ctx, in, o.User, nil)
		rep.steps = append(rep.steps, fmt.Sprintf("user:%d", time.Since(t0).Milliseconds()))
		if err != nil {
			rep.step, rep.stepKey = "Setting up "+o.User, "user"
			ui.Emit("fail", map[string]any{"key": "user", "label": "Setting up " + o.User})
			return err
		}
		ui.Emit("ok", map[string]any{"key": "user", "label": "Setting up " + o.User})
		user = in.Get("USER")
	} else if user == "" && !o.NoUser {
		firstboot := filepath.Join(in.Rootfs, "etc/andronix/firstboot")
		if ui.Interactive {
			fmt.Println()
			t0 := time.Now()
			err := t.Interactive("/usr/local/bin/andronix setup-user", false)
			rep.userSetupMS = time.Since(t0).Milliseconds()
			if b, rerr := os.ReadFile(filepath.Join(in.Rootfs, "etc/andronix/user")); err == nil && rerr == nil {
				user = strings.TrimSpace(string(b))
				in.Set("USER", user)
			} else {
				// Stopped (Ctrl+C) or failed: the first login asks again
				// (Welcome) instead of leaving root without a user.
				sys.WriteFileAtomic(firstboot, []byte("1\n"), 0o644)
				if ctx.Err() != nil || exitCode(err) == 130 {
					// Ctrl+C: don't go into the distro as root now.
					noStart, userStopped = true, true
					fmt.Println()
					ui.Note(d.Label() + " is installed, but your user isn't set up yet. It asks again the next time you start it: ./" + d.MainStart())
				}
			}
		} else {
			sys.WriteFileAtomic(firstboot, []byte("1\n"), 0o644)
		}
	}
	if lowRAM > 0 {
		ui.WarnKey("low_ram", fmt.Sprintf("%s needs about %d MB of RAM; this phone has %d MB. It may be slow or close apps; XFCE is lighter.", de.Name, de.MinRAMMB, lowRAM))
	}
	finish(in, de, user, noStart, userStopped, rep)
	return nil
}

// exitCode is a finished command's exit code (0 without an error, -1 if
// it didn't run).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// installShim puts the fchmodat2 preload shim for this CPU into the
// distro and registers it in /etc/ld.so.preload, before any package run.
func installShim(root string, arch sys.Arch) error {
	b, err := andronix.Preload.ReadFile("guest/preload/" + string(arch) + "/libandronix-fchmodat.so")
	if err != nil {
		return err
	}
	lib := "/usr/local/lib/andronix/libandronix-fchmodat.so"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(lib)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, lib), b, 0o755); err != nil {
		return err
	}
	p := filepath.Join(root, "etc/ld.so.preload")
	cur, _ := os.ReadFile(p)
	if strings.Contains(string(cur), lib) {
		return nil
	}
	os.Remove(p)
	return os.WriteFile(p, append(cur, []byte(lib+"\n")...), 0o644)
}

// browserRepo adds the browser's own package repository (Mozilla's) and
// refreshes the package lists; nothing for distros without one.
func browserRepo(ctx context.Context, d *conf.Distro, arch, root string, t *proot.Target, fam *pkgmgr.Family) error {
	if d.BrowserRepoFor(arch) != "mozilla" {
		return nil
	}
	key, _, err := netx.Get(ctx, pkgmgr.MozillaKeyURL, nil)
	if err == nil {
		err = pkgmgr.MozillaRepo(root, key)
	}
	if err == nil {
		err = t.Run(ctx, fam.Update, nil, nil)
	}
	return err
}

// installCodecs installs the browser's video codecs (DISTRO_BROWSER_CODECS),
// if any are missing. Optional: if the set fails, each on its own (Fedora's
// openh264 comes from Cisco's repo, which may be off). It reports whether
// it installed anything.
func installCodecs(ctx context.Context, t *proot.Target, fam *pkgmgr.Family, d *conf.Distro, r ui.Reporter, lg *Logger) bool {
	cs := d.BrowserCodecs
	if len(cs) == 0 {
		return false
	}
	n := count(ctx, t, fam.SimInstall(cs), fam)
	if n == 0 {
		return false
	}
	r.Label("Installing video codecs")
	if err := pkgRun(ctx, t, fam, fam.Install(cs), n, r); err != nil {
		lg.Printf("browser codecs: %v", err)
		for _, c := range cs {
			if err := t.Run(ctx, fam.Install([]string{c}), nil, func(l string) { r.Line(l) }); err != nil {
				lg.Printf("browser codec %s: %v", c, err)
			}
		}
	}
	return true
}

// prebuilt reports whether the unpacked image already contains this
// desktop (its session command is installed): a Modded edition or an
// exported rootfs. Then the package steps are skipped.
func prebuilt(in *Inst, de *conf.Desktop) bool {
	if !sessionPresent(in, de) {
		return false
	}
	switch in.Get("PREBUILT") {
	case "yes":
		return true
	case "no":
		return false
	}
	// Unpacked by an andronix before PREBUILT (2.0.0): only a Modded or
	// Classic image is prebuilt; a free one has its desktop from apt,
	// maybe half-installed.
	return moddedImage(in) || in.Get("EDITION") != ""
}

func sessionPresent(in *Inst, de *conf.Desktop) bool {
	if de.None() || de.Session == "" {
		return false
	}
	cmd := strings.Fields(de.Session)[0]
	return hasAny(in.Rootfs, "usr/bin/"+cmd, "bin/"+cmd, "usr/local/bin/"+cmd)
}

// desktopLeft reports an unfinished install with a desktop in it: unpacked
// from a ready-made or Modded image, a desktop recorded, or any desktop's
// session installed (an interrupted package run).
func desktopLeft(in *Inst) bool {
	if _, err := os.Stat(in.Rootfs); err != nil || in.Installed() {
		return false
	}
	if p := in.Get("PREBUILT"); p != "" && p != "no" {
		return true
	}
	if de := in.Get("DE"); de != "" && de != "none" {
		return true
	}
	for _, id := range conf.DesktopIDs() {
		if de, err := conf.ResolveDesktop(id); err == nil && sessionPresent(in, de) {
			return true
		}
	}
	return false
}

// moddedImage reports a Modded edition (its layer's files are present).
func moddedImage(in *Inst) bool { return hasAny(in.Rootfs, "usr/share/andronix/modded") }

func hasAny(root string, rels ...string) bool {
	for _, r := range rels {
		if _, err := os.Stat(filepath.Join(root, r)); err == nil {
			return true
		}
	}
	return false
}

// API is products-api's first address (netx.APIs has the fallback).
func API() string { return netx.APIs()[0] }

// moddedSource turns the app's token into a products-api download:
// GET <api>/v1/modded/download/<file>?k&e&h answers with a redirect to a
// short-lived bucket URL (the same flow the Modded scripts use today).
// The token is "k=<key>&e=<expiry>&h=<hash>", optionally with "&f=<file>";
// the default file is <id>-<version>-<desktop>-modded-<arch>.tar.xz.
func moddedSource(d *conf.Distro, de *conf.Desktop, arch sys.Arch, p Paths, token, edition string) (source, error) {
	if token == "" {
		return source{}, ui.Errorf("missing_download_token", "Missing download token", "Modded editions need the token the Andronix app gives you after purchase.",
			"Copy the install command from the Andronix app again; it includes --token.")
	}
	q, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(token), "?"))
	if err != nil || q.Get("k") == "" || q.Get("e") == "" || q.Get("h") == "" {
		return source{}, ui.Errorf("that_token_doesn_t_look_right", "That token doesn't look right", "The --token value is incomplete.",
			"Copy the whole install command from the Andronix app again.")
	}
	file := q.Get("f")
	if file == "" {
		file = fmt.Sprintf("%s-%s-%s-modded-%s.tar.xz", d.ID, d.Version, de.ID, arch)
		if strings.HasPrefix(edition, "legacy-") {
			file = fmt.Sprintf("%s-%s-%s.tar.xz", edition, d.Version, arch)
		}
	}
	if strings.ContainsAny(file, "/\\") || strings.HasPrefix(file, ".") {
		return source{}, ui.Errorf("that_token_doesn_t_look_right", "That token doesn't look right", "The file name in the token isn't valid.", "Copy the install command from the Andronix app again.")
	}
	// The token proves the purchase of a product, not a file: it goes to
	// products-api as given (every field), with this CPU's file name.
	q.Del("f")
	base := API() + "/v1/modded/download/"
	return source{kind: "modded", api: API(), url: base + url.PathEscape(file) + "?" + q.Encode(),
		sumURL: base + url.PathEscape(file+".sha256") + "?" + q.Encode(), file: filepath.Join(p.Cache, file)}, nil
}

// setupUserScripted runs setup-user inside the distro for name, with the
// passwords, time zone and keyboard written to its stdin: proot passes a
// session's environment on its command line, which /proc shows.
func setupUserScripted(ctx context.Context, in *Inst, name string, onLine func(string)) error {
	if p := userNameProblem(name); p != "" {
		return &ui.UserError{Title: "Can't use that user name", Class: "bad_user",
			What: "'" + name + "': " + p + ".", Fix: "Use a different name, e.g. alex."}
	}
	if len(os.Getenv("ANDRONIX_USER_PASSWORD")) < 4 {
		return &ui.UserError{Title: "No password for " + name, Class: "bad_user",
			What: "ANDRONIX_USER_PASSWORD must hold the new user's password (4 characters or more).", Fix: "Set it in the environment, never on the command line."}
	}
	// A reserved name the static list missed, or one this distro's rootfs
	// already uses (passwd/group), or the aid_<user> collision: catch it here,
	// host-side, before useradd fails at the very end of the install (#579).
	if p := userNameTakenIn(in.Rootfs, name); p != "" {
		return &ui.UserError{Title: "Can't use that user name", Class: "bad_user",
			What: p + ".", Fix: "Run again with another name: andronix install " + in.D.ID + " --user <name>"}
	}
	rootfs.InstallSelf(in.Rootfs) // setup-user --user needs this build's in-distro andronix
	t := in.Target(true)
	var secrets strings.Builder
	for _, k := range secretKeys {
		if v := os.Getenv(k); v != "" && !strings.ContainsAny(v, "\n\r") {
			secrets.WriteString(k + "=" + v + "\n")
		}
	}
	var tail []string
	err := t.Run(ctx, "/usr/local/bin/andronix setup-user --user "+name+" --secrets-stdin", strings.NewReader(secrets.String()), func(l string) {
		if tail = append(tail, l); len(tail) > 6 {
			tail = tail[1:]
		}
		if onLine != nil {
			onLine(l)
		}
	})
	if err != nil {
		return &ui.UserError{Title: "Couldn't set up " + name, Class: "setup_user", What: lastNonEmpty(strings.Join(tail, "\n"), 3), Err: err,
			Fix: "Run it again: andronix setup-user " + in.D.ID + " --user " + name}
	}
	b, rerr := os.ReadFile(filepath.Join(in.Rootfs, "etc/andronix/user"))
	if rerr != nil || strings.TrimSpace(string(b)) != name {
		return &ui.UserError{Title: "Couldn't set up " + name, Class: "setup_user", What: "setup-user finished but didn't record the user.",
			Fix: "Run it again: andronix setup-user " + in.D.ID + " --user " + name}
	}
	in.Set("USER", name)
	return nil
}

// secretKeys are what setup-user --secrets-stdin accepts.
var secretKeys = []string{"ANDRONIX_USER_PASSWORD", "ANDRONIX_VNC_PASSWORD", "ANDRONIX_TZ", "ANDRONIX_KEYBOARD"}

// ReadSecrets sets secretKeys from KEY=value lines on stdin (setup-user
// --secrets-stdin inside the distro).
func ReadSecrets(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && slices.Contains(secretKeys, k) {
			os.Setenv(k, v)
		}
	}
}

// SetupUserHost is `andronix setup-user <distro> --user NAME` outside the
// distro (the standalone app), secrets from the environment.
func SetupUserHost(ctx context.Context, distro, name string) error {
	d, err := resolveDistro(distro, "start")
	if err != nil {
		return err
	}
	in := Open(d)
	if !in.Installed() {
		return ui.Errorf("not_installed", d.Label()+" isn't installed", "There's no distro to set a user up in.", "Install it first: andronix install "+d.ID)
	}
	if err := setupUserScripted(ctx, in, name, func(l string) { fmt.Println("  " + l) }); err != nil {
		return err
	}
	ui.OK(name + " is set up in " + d.Label() + ".")
	return nil
}

// tokenExpired reads a Modded token's expiry (e, unix seconds); a token
// without a readable one isn't judged here (the server decides).
func tokenExpired(token string, now time.Time) bool {
	q, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(token), "?"))
	if err != nil {
		return false
	}
	e, err := strconv.ParseInt(q.Get("e"), 10, 64)
	return err == nil && e > 0 && e < now.Add(time.Minute).Unix()
}

// moddedChecksum fetches a Modded download's sha256 (<file>.sha256, same
// token). Missing (404: products-api not serving it yet) means unchecked,
// logged; anything malformed is an error, never a silent pass.
func moddedChecksum(ctx context.Context, s *source, lg *Logger, warn func(string)) error {
	// products-api answers 302 to the file. Its first reachable address
	// is the one the download then uses.
	var b []byte
	var err error
	for _, api := range netx.APIs() {
		sum := api + strings.TrimPrefix(s.sumURL, s.api)
		if b, _, err = netx.Get(ctx, sum, nil); !netx.Unreachable(ctx, err) {
			if api != s.api {
				lg.Printf("modded: %s unreachable, using %s", s.api, api)
				s.url, s.sumURL, s.api = api+strings.TrimPrefix(s.url, s.api), sum, api
			}
			break
		}
	}
	var se *netx.StatusError
	switch {
	case errors.As(err, &se) && se.Code == 404:
		lg.Printf("modded: no checksum served for %s; downloading unchecked", filepath.Base(s.file))
		if warn != nil {
			warn("The server has no checksum for this edition yet; the download can't be verified.")
		}
		return nil
	case err != nil:
		return downloadErr(err, true) // products-api (Modded edition checksum)
	}
	f := strings.Fields(string(b))
	if len(f) == 0 || !sha256Re.MatchString(strings.ToLower(f[0])) {
		return ui.Errorf("download_check_failed", "Download check failed", "The server's checksum for this edition isn't valid.", "Run the same command again; if it keeps happening, tell us on Discord.")
	}
	s.sha = strings.ToLower(f[0])
	return nil
}

// kernelNewer reports whether release (e.g. "6.6.30-android15") is newer
// than max (e.g. "6.5"), comparing major.minor.
func kernelNewer(release, max string) bool {
	num := func(s string) (int, int) {
		var a, b int
		fmt.Sscanf(s, "%d.%d", &a, &b)
		return a, b
	}
	ra, rb := num(release)
	ma, mb := num(max)
	return ra > ma || ra == ma && rb > mb
}

func installLabel(de *conf.Desktop) string {
	if de.None() {
		return "Installing the basics"
	}
	return "Installing " + de.Name + " desktop"
}

// mirrorRetryWait is the pause before the second try of our mirror, and
// headWait the one between HEAD tries (variables for tests).
var mirrorRetryWait, headWait = 3 * time.Second, 2 * time.Second

// sleepCtx waits d, or returns false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// headRetry is netx.Head with up to 3 tries on a network error or a 5xx
// (#357: one dropped HEAD sent the install to Docker Hub). It reports
// whether the last failure was such a transient one.
func headRetry(ctx context.Context, u string) (code int, size int64, err error, transient bool) {
	for try := 0; try < 3; try++ {
		if try > 0 && !sleepCtx(ctx, time.Duration(try)*headWait) {
			return 0, 0, ctx.Err(), false
		}
		code, size, err = netx.Head(ctx, u)
		transient = ctx.Err() == nil && (err != nil || code >= 500)
		if !transient {
			return
		}
	}
	return
}

// mirrorTarball looks for the distro's tarball: the API's answer first (see
// resolve.go), then the built-in mirror URL. ok: use s. transient: it was
// skipped only because the network failed (worth another try later).
func mirrorTarball(ctx context.Context, d *conf.Distro, arch sys.Arch, p Paths, lg *Logger) (s source, ok, transient bool) {
	name := fmt.Sprintf("%s-%s-%s.tar.xz", d.ID, d.Version, arch)
	if res := resolve(ctx, "rootfs", url.Values{"distro": {d.ID}, "ver": {d.Version}}); res != nil {
		for _, u := range res.urls() {
			code, size, err := netx.Head(ctx, u)
			lg.Printf("resolved tarball %s (%s): %d %v", u, res.Version, code, err)
			if err == nil && code == 200 {
				if res.Size > 0 {
					size = res.Size
				}
				return source{kind: "tarball", url: u, size: size, sha: res.SHA256, file: filepath.Join(p.Cache, name)}, true, false
			}
		}
	}
	u := fmt.Sprintf("%s/rootfs/%s/%s/%s", Mirror(), d.ID, d.Version, name)
	code, size, err, transient := headRetry(ctx, u)
	lg.Printf("tarball %s: %d %v", u, code, err)
	if err != nil || code != 200 {
		return source{}, false, transient
	}
	s = source{kind: "tarball", url: u, size: size, file: filepath.Join(p.Cache, name)}
	b, _, gerr := netx.Get(ctx, u+".sha256", nil)
	if gerr == nil {
		if fs := strings.Fields(string(b)); len(fs) > 0 && sha256Re.MatchString(fs[0]) {
			s.sha = fs[0]
		}
	}
	// Never unchecked: without a checksum, the registry (whose layers are
	// checked by digest) is used instead.
	if s.sha != "" {
		return s, true, false
	}
	lg.Printf("tarball %s: no checksum, using the registry", u)
	return source{}, false, netx.Unreachable(ctx, gerr)
}

// pickSource finds the base rootfs: a local file (ANDRONIX_ROOTFS), our
// mirror's tarball, else the distro's registry image. say (may be nil)
// shows a progress line.
func pickSource(ctx context.Context, d *conf.Distro, arch sys.Arch, p Paths, lg *Logger, say func(string)) (source, error) {
	if f := os.Getenv("ANDRONIX_ROOTFS"); f != "" {
		if _, err := os.Stat(f); err != nil {
			return source{}, ui.Errorf("rootfs_file_not_found", "Rootfs file not found", "ANDRONIX_ROOTFS points at '"+f+"', which doesn't exist.", "Fix the path or unset ANDRONIX_ROOTFS.")
		}
		s := source{kind: "local", file: f}
		if b, err := os.ReadFile(f + ".sha256"); err == nil {
			if fs := strings.Fields(string(b)); len(fs) > 0 {
				s.sha = fs[0]
			}
		}
		return s, nil
	}
	mode := os.Getenv("ANDRONIX_SOURCE")
	// tarTransient: our mirror's tarball was skipped only because the network
	// failed on the way (not a 404 or a missing checksum), so it's worth
	// another try if the registry fails too (#357).
	tarTransient := false
	if mode != "registry" {
		var s source
		var ok bool
		if s, ok, tarTransient = mirrorTarball(ctx, d, arch, p, lg); ok {
			return s, nil
		}
		if mode == "tarball" {
			return source{}, ui.Errorf("download_not_available", "Download not available", "The "+d.Label()+" download isn't reachable right now.", "Check your connection and try again in a few minutes.")
		}
	}
	a, v := arch.OCI()
	im, err := oci.Resolve(ctx, d.Image, a, v)
	lg.Printf("registry %s: %v", d.Image, err)
	if err != nil {
		var np *oci.ErrNoPlatform
		var se *netx.StatusError
		// #357: Docker Hub blips (auth.docker.io TLS timeouts, a 403/429/5xx)
		// failed installs outright at "Finding the best download" even for
		// distros our mirror has. When the mirror was skipped only because
		// the network failed on the way, try it once more before giving up.
		if tarTransient && !errors.As(err, &np) && ctx.Err() == nil {
			lg.Printf("registry failed (%v); trying the Andronix mirror again", err)
			if say != nil {
				say("Docker Hub is busy; trying the Andronix mirror again...")
			}
			if !sleepCtx(ctx, mirrorRetryWait) {
				return source{}, ctx.Err()
			}
			if s, ok, _ := mirrorTarball(ctx, d, arch, p, lg); ok {
				return s, nil
			}
		}
		switch {
		case errors.As(err, &np) && d.UpstreamTarball(string(arch)) != "":
			// No OCI image for this CPU (Arch Linux ARM): the distro's own tarball.
			url := d.UpstreamTarball(string(arch))
			_, size, _ := netx.Head(ctx, url)
			s := source{kind: "upstream", url: url, size: size, file: filepath.Join(p.Cache, d.ID+"-upstream-"+string(arch)+filepath.Ext(url))}
			if b, _, err := netx.Get(ctx, url+".sha256", nil); err == nil {
				if f := strings.Fields(string(b)); len(f) > 0 && len(f[0]) == 64 {
					s.sha = f[0]
				}
			}
			return s, nil
		case errors.As(err, &np):
			return source{}, ui.Errorf("no_build_for_your_cpu", "No build for your CPU", d.Label()+" has no image for "+arch.Label()+".", "Pick another distro in the Andronix app.")
		case errors.As(err, &se) && se.Code == 429:
			return source{}, ui.Errorf("the_image_server_is_busy", "The image server is busy", "Docker Hub limits downloads per network, and your network hit the limit.",
				"Wait an hour, or switch between Wi-Fi and mobile data, then run the same command again.")
		}
		if ce, ok := clockCertErr(err); ok {
			return source{}, ce
		}
		return source{}, ui.Errorf("couldn_t_reach_the_download_servers", "Couldn't reach the download servers", err.Error(), "Check your internet connection, then run the same command again.")
	}
	return source{kind: "registry", image: im}, nil
}

// downloadErr turns a failed download into a user error. tokenAuth is true
// only for products-api downloads (Modded editions, the beta channel),
// where a 401/403 is the user's download token being refused or made for
// another edition. On every other source — the free distro tarball, the
// OCI registry, self-update — an HTTP status is just a failed download: a
// registry (docker.io) 401 is its own bearer token expiring mid-download,
// not the user's, so it must not be shown as "Download link expired ...
// daily limit" (#243, support #658).
func downloadErr(err error, tokenAuth bool) error {
	if errors.Is(err, context.Canceled) {
		return ui.ErrCancelled
	}
	if ce, ok := clockCertErr(err); ok {
		return ce
	}
	var se *netx.StatusError
	if tokenAuth && errors.As(err, &se) && se.Code == 403 {
		// products-api: a genuine token for another edition.
		return &ui.UserError{Title: "That token is for another edition", Class: "token_other_edition",
			What: "The download token was made for a different Modded edition than this command installs.",
			Fix:  "Copy the install command for this edition from the Andronix app.", Err: err}
	}
	if tokenAuth && errors.As(err, &se) && se.Code == 401 {
		return &ui.UserError{Title: "Download link expired", Class: "token_refused", What: "The download token was refused (it lasts a limited time, and each purchase has a daily limit).",
			Fix: "Copy a fresh install command from the Andronix app and run it again.", Err: err}
	}
	return &ui.UserError{Title: "Download failed", Class: "download", Kind: downloadKind(err), Detail: pkgmgr.CleanDetail(err.Error()),
		What: "The download stopped before it finished (" + err.Error() + ").",
		Fix:  "Check your internet connection, then run the same command again. It resumes where it stopped.", Err: err}
}

// downloadKind names why a download failed, for telemetry (download
// failures came with no detail: Ubuntu 26.04 on 32-bit phones, Oct 7).
func downloadKind(err error) string {
	var se *netx.StatusError
	var dns *net.DNSError
	s := strings.ToLower(err.Error())
	switch {
	case errors.As(err, &se):
		return fmt.Sprintf("http_%d", se.Code)
	case errors.As(err, &dns):
		return "network_dns"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "timeout") || strings.Contains(s, "timed out"):
		return "timeout"
	case strings.Contains(s, "connection refused") || strings.Contains(s, "connection reset") || strings.Contains(s, "network is unreachable") || strings.Contains(s, "no route to host"):
		return "network_connect"
	case strings.Contains(s, "unexpected eof") || strings.Contains(s, "stream error") || strings.Contains(s, "broken pipe"):
		return "interrupted"
	case strings.Contains(s, "sha256") || strings.Contains(s, "checksum"):
		return "checksum"
	case strings.Contains(s, "no space left"):
		return "disk_full"
	}
	return "other"
}

var playRepo = termux.PlayRepo // a variable for tests

// termuxPkgErr explains a failed Termux package install (proot, the
// X server) by what went wrong; manual is the command to do it by hand.
func termuxPkgErr(title, manual string, err error) error {
	e := &ui.UserError{Title: title, Class: "termux_pkg", What: "Termux's package manager stopped with an error.",
		Fix: "Update Termux's packages, then run the same command again: " + manual, Err: err}
	var f *pkgmgr.Failure
	if !errors.As(err, &f) {
		return e
	}
	e.Kind, e.Detail = string(f.Kind), f.Detail
	if f.Detail != "" {
		e.What = "Termux's package manager stopped: " + f.Detail
	}
	switch {
	case f.Kind == pkgmgr.KindCancelled:
		return ui.ErrCancelled
	case f.Kind == pkgmgr.KindDiskFull:
		e.Class, e.Title = "not_enough_space", "Your phone ran out of space"
		e.Fix = "Free up some space, then run the same command again."
	case f.Kind.Network() && playRepo():
		// Play Store Termux has one package server and no mirrors
		// (support #305: the network refused termux.net).
		e.Title = "Your network blocks Termux's package server"
		e.What = "Termux couldn't connect to termux.net (" + f.Detail + "). Termux from the Play Store gets its packages only from there; this network refused or dropped the connection."
		e.Fix = "Switch between Wi-Fi and mobile data, or turn a VPN on (or off), then run the same command again."
	case f.Kind.Network():
		e.What = "Termux's package servers didn't answer, on any mirror we tried (" + f.Detail + ")."
		e.Fix = "Check your internet (switch between Wi-Fi and mobile data, or turn a VPN off), then run the same command again. Or pick a mirror yourself: termux-change-repo"
	case f.Kind == pkgmgr.KindClock || f.Kind == pkgmgr.KindGPG:
		e.Fix = "Make sure the phone's date and time are set automatically, then run: " + manual
	}
	return e
}

// spaceNeed is the free space (MB) an install needs: the unpacked image,
// plus the desktop's packages, plus 20%. The image is streamed into the
// extractor and never stored (unpackImage: "no copy on disk"), so the
// download size isn't part of the peak; the base is the unpacked rootfs
// (DISTRO_DISK_MB). The desktop part comes from DISTRO_XFCE_MB (measured
// with XFCE) scaled by the desktop's size against XFCE's; distros not
// measured use the older estimate.
// notEnoughSpace is the "Not enough space" result built when an extraction
// hits ENOSPC mid-unpack, after the pre-download check passed (#1147: the
// estimate was within a few MB of free and still ran out). It reports the same
// need/free numbers as the pre-download check (install.go phone-check) so the
// two agree, and the same Class so the app treats it identically.
func notEnoughSpace(d *conf.Distro, de *conf.Desktop, haveRootfs bool) *ui.UserError {
	need := spaceNeed(d, de, haveRootfs)
	free := sys.FreeMB(sys.Home())
	deLabel := "the command line"
	if !de.None() {
		deLabel = de.Name
	}
	gap := need - free
	if gap < 1 {
		gap = 1
	}
	return &ui.UserError{Title: "Not enough space", Class: "not_enough_space",
		What: fmt.Sprintf("Ran out of space while unpacking %s with %s: the phone has %d MB free and it needs about %d MB. Unpacking stopped instead of leaving a half-finished system.", d.Label(), deLabel, free, need),
		Fix:  fmt.Sprintf("Free up about %d MB (old videos, app caches), then run the same command again.", gap)}
}

// downloadPeakMB is the space (MB) a stored download needs at its peak: the
// compressed tarball on disk plus the unpacked base rootfs (DISTRO_DISK_MB,
// +20%) it is being unpacked into. The desktop's packages are installed
// afterwards, once the tarball is gone, so they aren't in this peak — the
// later desktop peak is spaceNeed, which the phone-check already enforced.
// The streamed image path has no stored copy and doesn't use this (#301).
func downloadPeakMB(d *conf.Distro, size int64) int64 {
	dlMB := (size + (1 << 20) - 1) >> 20
	return dlMB + int64(float64(d.DiskMB)*1.2)
}

func spaceNeed(d *conf.Distro, de *conf.Desktop, haveRootfs bool) int64 {
	base := d.DiskMB
	if haveRootfs {
		base = 0
	}
	if len(standaloneExtras(d, de, sys.Standalone())) > 0 {
		base += standaloneExtrasMB
	}
	if x := d.XFCEMB(); x > 0 && !de.None() {
		desk := x - d.DiskMB
		if xfce, err := conf.ResolveDesktop("xfce"); err == nil && xfce.DiskMB > 0 && de.DiskMB > 0 {
			desk = desk * de.DiskMB / xfce.DiskMB
		}
		return int64(float64(base+desk) * 1.2)
	}
	if de.None() {
		return int64(float64(base+100) * 1.2)
	}
	return int64(base + de.DiskMB + 300)
}

// standaloneExtrasMB is what Xvfb and PulseAudio add to a desktop install
// (DISTRO_XFCE_MB is measured without them): about 60-80 MB, measured on an
// API 36 emulator with Debian 13 XFCE (sv2-startup).
const standaloneExtrasMB = 80

// vncPackages chooses the VNC packages to install: those the distro's
// archive actually has (have), or the full list (all) when the availability
// check resolved none. It never returns empty from a non-empty list, so a
// transient check failure can't silently drop the VNC server; in that case
// the install errors by name and the Xvnc/vncpasswd check in setupDesktop
// still fails loudly. Dropping only the missing supplementary packages is
// what fixes Ubuntu 26.04 "resolute" (tigervnc-tools gone, server kept).
func vncPackages(all, have []string) []string {
	if len(have) == 0 {
		return all
	}
	return have
}

// standaloneExtras are the packages a desktop install adds for the app's own
// display and sound (standalone only): DISTRO_XVFB and DISTRO_PULSE, the ones
// the first display start would install otherwise. None for Termux or for
// command line installs.
func standaloneExtras(d *conf.Distro, de *conf.Desktop, standalone bool) []string {
	if !standalone || de.None() {
		return nil
	}
	var out []string
	for _, p := range []string{d.Xvfb(), d.Pulse()} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// installKey is the package step's --progress json key.
func installKey(de *conf.Desktop) string {
	if de.None() {
		return "basics"
	}
	return "desktop"
}

// finishLabel: Termux writes its start script; the standalone app doesn't.
func finishLabel(d *conf.Distro) string {
	if sys.Standalone() {
		return "Finishing up"
	}
	return "Writing " + d.MainStart()
}

// packages is "1 package" or "n packages".
func packages(n int) string {
	if n == 1 {
		return "1 package"
	}
	return fmt.Sprintf("%d packages", n)
}

// refreshTitle is a failed package-list refresh; network failures get
// "Couldn't reach the package servers" (pkgErr), others keep this.
const refreshTitle = "The package lists couldn't be refreshed"

func pkgErr(title string, err error) error {
	if errors.Is(err, context.Canceled) {
		return ui.ErrCancelled
	}
	e := &ui.UserError{Title: title, What: "The package manager stopped with an error.", Class: "package_manager",
		Fix: "Run the same command again; downloads resume. If Android closed Termux (signal 9), see docs.andronix.app on the phantom process killer.", Err: err}
	if sys.Standalone() {
		e.Fix = "Try again; downloads resume. If Android stopped the install, keep the Andronix app open while it runs."
	}
	var f *pkgmgr.Failure
	if !errors.As(err, &f) {
		return e
	}
	e.Kind, e.Detail = string(f.Kind), f.Detail
	if f.Detail != "" {
		e.What = "The package manager stopped: " + f.Detail
	}
	switch {
	case f.Kind == pkgmgr.KindCancelled:
		return ui.ErrCancelled
	case f.Kind == pkgmgr.KindLock && strings.HasPrefix(f.Detail, "another andronix"):
		// support #313: a second install of the same distro (an andronix
		// without the distro lock, or one in a sandbox we can't see).
		e.Class = "install_running"
		e.Title = "Another andronix is using this distro's package manager"
		e.What = "It is running in another Termux session (" + f.Detail + ")."
		e.Fix = "Wait for it to finish, or close it, then run the same command again."
		if sys.Standalone() {
			e.What = "Another install of this distro is running in the Andronix app (" + f.Detail + ")."
			e.Fix = "Wait for it to finish, then try again."
		}
	case f.Kind == pkgmgr.KindDiskFull:
		e.Class = "not_enough_space"
		e.Title, e.What = "Your phone ran out of space", "The package manager couldn't write any more files ("+f.Detail+")."
		e.Fix = "Free up some space (photos, videos, apps you don't use), then run the same command again. It goes on where it stopped."
	case f.Kind == pkgmgr.KindProotSyscall || f.Kind == "proot_syscall_not_fixed":
		e.Title = "Linux programs fail under proot on this phone"
		e.What = "Programs in the distro crash or get 'Function not implemented' from the system (" + f.Detail + "), even after a second try with proot's fast mode off. It isn't your network."
		e.Fix = "Update Termux (from the Play Store or F-Droid, wherever you got it), then run the same command again. If it still fails, send a report: andronix report."
		if sys.Standalone() {
			e.Fix = "Update the Andronix app, then try again. If it still fails, send a report from the app."
		}
	case f.Kind == pkgmgr.KindProotCrash || f.Kind == "proot_crash_l2s_not_fixed":
		e.Class = "proot_crash"
		e.Title = "proot crashed on this phone, so the install couldn't finish"
		e.What = "proot (the layer that runs Linux on Android) crashed with signal 11 while the package manager was working (" + f.Detail + "), which left the install half-finished. It isn't your network or the distro — it's Termux's proot on this phone's kernel."
		e.Fix = "Install from the Andronix app instead — its built-in proot runs where Termux's crashes. Or first update Termux: run pkg upgrade in Termux (let it finish), then run the same command again. If it still fails, send a report: andronix report."
		if sys.Standalone() {
			e.What = "proot (the layer that runs Linux on Android) crashed with signal 11 while the package manager was working (" + f.Detail + "), which left the install half-finished. It isn't your network or the distro."
			e.Fix = "Update the Andronix app, then try again. If it still fails, send a report from the app."
		}
	case f.Kind == pkgmgr.KindFakeRoot || f.Kind == "fake_root_not_fixed":
		e.Title = "The distro's tools don't see root access on this phone"
		e.What = "A program in the distro refused to run because it isn't root (" + f.Detail + "). proot pretends to be root, and on this phone that didn't work, even after a second try."
		e.Fix = "Send a report so we can look: andronix report. Meanwhile, another distro (Debian, Ubuntu) may install fine."
	case f.Kind == pkgmgr.KindKeyring:
		e.Title = "The package signing keys couldn't be set up"
		e.What = "The key tool (gpg-agent) wouldn't start under proot on this phone (" + f.Detail + ")."
		e.Fix = "Send a report so we can look: andronix report. Meanwhile, Debian, Ubuntu or Fedora usually install fine."
	case f.Kind.Network():
		if title == refreshTitle {
			e.Title = "Couldn't reach the package servers"
		}
		e.What = "The package servers didn't answer, on any mirror we tried (" + f.Detail + ")."
		e.Fix = "Check your internet (switch between Wi-Fi and mobile data, or turn a VPN off), then run the same command again. It goes on where it stopped."
	case f.Kind == pkgmgr.KindClock || f.Kind == pkgmgr.KindGPG:
		if now, behind := clockBehind(); behind {
			// #354: every signature check fails with the clock this far back.
			e.Class = "clock_wrong"
			e.Title = "Your phone's date is wrong"
			e.What = "The phone's date is " + now.Format("2 January 2006") + ". With the date that far back, every package signature check fails (the signing keys look like they were made in the future), so nothing can be installed."
		}
		e.Fix = "Make sure the phone's date and time are set automatically (Settings, Date and time), then run the same command again."
	case f.Kind == pkgmgr.KindUnauth:
		e.Title = "The package lists couldn't be verified"
		e.What = "The package manager refused packages it couldn't verify (" + f.Detail + "): the package lists' signature check failed during the refresh, even after refreshing again."
		e.Fix = "Make sure the phone's date and time are set automatically (Settings, Date and time). If you're on public Wi-Fi, open a browser and sign in, or switch to mobile data. Then run the same command again."
	case f.Kind == pkgmgr.KindGuestLibs:
		e.Class = "guest_libs"
		e.Title = "Linux programs can't load their libraries on this phone"
		e.What = "The distro's own programs couldn't load their system libraries under proot (" + f.Detail + "). It isn't your network or a bad download, and installing again won't change it."
		if sys.DetectArch() == "arm" {
			e.What = "The distro's own programs couldn't load their system libraries under proot (" + f.Detail + "). We've seen this only on 32-bit phones with an old Android kernel (3.18), and we're working on it. It isn't your network or a bad download, and installing again won't change it."
		}
		e.Fix = "Please send a report so we can look: andronix report"
		if sys.Standalone() {
			e.Fix = "Please send a report from the app so we can look."
		}
	case f.Kind == pkgmgr.KindKilled:
		e.Fix = "Android stopped the install (signal 9). Keep Termux open in front while it runs, and see docs.andronix.app on the phantom process killer; then run the same command again."
		if sys.Standalone() {
			e.Fix = "Android stopped the install (signal 9). Keep the Andronix app open while it installs, then try again."
		}
	case f.Kind == pkgmgr.KindMirror:
		if title == refreshTitle {
			e.Title = "The package mirrors sent a broken answer"
		}
		e.What = "The package mirror redirected in circles or had no package index, on every mirror we tried (" + f.Detail + "). Your network or region is likely redirecting the package servers."
		e.Fix = "Switch between Wi-Fi and mobile data (or turn a VPN on or off), then run the same command again. It goes on where it stopped."
	case f.Kind == pkgmgr.KindIntercepted:
		e.What = "Downloads arrived changed, on every mirror we tried: your network is likely showing a sign-in page or filtering them."
		e.Fix = "Open a browser and sign in to the Wi-Fi, or switch between Wi-Fi and mobile data (or turn a VPN on or off), then run the same command again."
	case f.Kind == pkgmgr.KindProotArgs:
		e.What = "proot refused how Andronix runs it, even with only its basic options. Termux's proot may be broken or very old."
		e.Fix = "Reinstall it, then run the same command again: pkg reinstall proot"
		if sys.Standalone() {
			e.What = "proot refused how Andronix runs it, even with only its basic options."
			e.Fix = "Update the Andronix app, then try again. If it still fails, send a report from the app."
		}
	case f.Kind == pkgmgr.KindProotExec:
		e.What = "proot can't start programs inside the distro on this phone (" + f.Detail + ")."
		e.Fix = "Send a report so we can look: andronix report"
	case f.Kind == pkgmgr.KindProotFailed:
		e.What = "proot stopped with an error (" + f.Detail + ")."
		e.Fix = "Reinstall it, then run the same command again: pkg reinstall proot. If it still fails, send a report: andronix report"
	}
	return e
}

// count runs a simulate command and counts the packages it would touch.
func count(ctx context.Context, t *proot.Target, cmd string, f *pkgmgr.Family) int {
	n := 0
	t.Run(ctx, cmd, nil, func(l string) {
		if f.SimLine.MatchString(l) {
			n++
		}
	})
	return n
}

// pkgRun runs a package command with a progress bar read from its output.
func pkgRun(ctx context.Context, t *proot.Target, f *pkgmgr.Family, cmd string, n int, r ui.Reporter) error {
	defer rootfs.EnsureBwrapShim(t.Rootfs) // a package may have (re)installed bwrap
	if f.PostInstall != "" {
		defer t.Run(ctx, f.PostInstall, nil, nil)
	}
	steps := 0
	// apt: bytes while it downloads ("Need to get" .. the last "Get:"),
	// then the package steps as before (lead, 2.1: a big desktop's
	// download showed no movement for minutes).
	var need, got int64
	downloading := false
	return t.Run(ctx, cmd, nil, func(l string) {
		r.Line(l)
		if cur, tot, ok := f.Parse(l); ok {
			r.Progress(int64(cur), int64(tot), "packages")
			return
		}
		if f.ID == "apt" {
			if b, ok := pkgmgr.AptNeedBytes(l); ok && b > 0 {
				need, got, downloading = b, 0, true
				r.Progress(0, need, "bytes")
				return
			}
			if downloading {
				if b, ok := pkgmgr.AptGotBytes(l); ok {
					got = min(got+b, need)
					if f.Step != nil && f.Step.MatchString(l) {
						steps++ // still a step for the phases after the download
					}
					r.Progress(got, need, "bytes")
					return
				}
				if strings.HasPrefix(l, "Fetched ") || strings.HasPrefix(l, "Unpacking ") || strings.HasPrefix(l, "Setting up ") {
					downloading = false
				}
			}
		}
		if f.Step != nil && n > 0 && f.Step.MatchString(l) {
			steps++
			total := int64(n * f.Phases)
			r.Progress(int64(steps), total, "")
		}
	})
}

// doneBefore emits the "done" event without waiting for send (started
// first, so a process ended right after "done" has already spawned it),
// then waits for send to return.
func doneBefore(send func()) {
	sent := make(chan struct{})
	go func() { send(); close(sent) }()
	ui.EmitDone(nil)
	<-sent
}

func finish(in *Inst, de *conf.Desktop, user string, noStart, userStopped bool, rep *installReport) {
	d := in.D
	if ui.JSON {
		// #290 (#884, #1052, #4255): in the app, "done" waited for the
		// install_result telemetry spawn (a fork+exec of this binary, slow
		// on linker64 phones) and the closing box, so every step showed ok
		// with no completion for seconds and users cancelled into a
		// "Cancelled" after a finished install. Start the send, tell the
		// app at once, then let the send finish before going on.
		doneBefore(func() { rep.send("ok") })
	}
	if sys.Standalone() {
		// In the Andronix app: the app opens the terminal and the desktop.
		fmt.Println()
		fmt.Print(ui.Box(ui.BoxOK, d.Label()+" is ready", appFinishLines(d, de, user)...))
		fmt.Println()
		rep.send("ok")
		return
	}
	start := ui.Interactive && !noStart
	lines := []string{}
	// The desktop is Termux:X11 (owner decision); VNC is the other way.
	if !de.None() {
		lines = append(lines, ui.KV("Desktop", "andronix desktop "+d.ID),
			ui.KV("", "in Termux; it opens in the Termux:X11 app ("+termux.X11AppAPK+" from "+termux.X11AppURL+")"),
			ui.KV("", "help: "+termux.X11Docs), "")
	}
	if start {
		lines = append(lines, ui.KV("Terminal", "you're going into "+d.Name+"'s terminal now (type exit to leave)"),
			ui.KV("", "next time, from Termux: andronix start "+d.ID), "")
	} else {
		lines = append(lines, ui.KV("Terminal", "andronix start "+d.ID+" (or ./"+d.MainStart()+")"))
	}
	if !de.None() {
		lines = append(lines, ui.KV("Or VNC", "vncserver-start inside "+d.Name+", then localhost:1 in a VNC viewer"))
	}
	if user != "" {
		lines = append(lines, ui.KV("User", user+" (sudo works)"))
	}
	lines = append(lines, ui.KV("Remove", "andronix remove "+d.ID))
	if r := in.Get("RENAMED"); r != "" {
		lines = append(lines, "", "Your older "+d.Name+" is untouched. Start it with ./"+strings.Fields(r)[0])
		in.Set("RENAMED", "") // said once: later installs of this distro don't repeat it
	}
	fmt.Println()
	title := d.Label() + " is ready"
	if userStopped {
		title = d.Label() + " is installed" // the user isn't set up yet (said above)
	}
	fmt.Print(ui.Box(ui.BoxOK, title, lines...))
	if !de.None() {
		if installed, known := termux.X11AppInstalled(); known && !installed {
			x11AppBox("Then, in Termux: andronix desktop " + d.ID)
		}
	}
	ui.Footer()
	// The last line says how to come back (support #164 #176 #218: after a
	// command-line install, users didn't know how to start it again).
	fmt.Println("  " + startLine(d, de))
	fmt.Println()
	rep.send("ok") // before the shell: Login replaces this process
	if start {
		startSound(context.Background(), true)
		if err := in.Target(false).Login(d.Shell, nil); err != nil {
			ui.ShowError(err)
		}
	}
}

// appFinishLines is the install's closing box in the Andronix app.
func appFinishLines(d *conf.Distro, de *conf.Desktop, user string) []string {
	lines := []string{"Open " + d.Name + " from the Andronix app: its terminal"}
	if !de.None() {
		lines[0] += " and its " + de.Name + " desktop."
	} else {
		lines[0] += "."
	}
	if user != "" {
		lines = append(lines, ui.KV("User", user+" (sudo works)"))
	}
	return lines
}

// startLine is the install's last line: how to start the distro (or its
// desktop) again from Termux.
func startLine(d *conf.Distro, de *conf.Desktop) string {
	if !de.None() {
		return ui.Bold("Open the desktop any time, from Termux: andronix desktop " + d.ID)
	}
	return ui.Bold("Start " + d.Name + " any time, from Termux: andronix start " + d.ID)
}

// resolvedID is the id a distro or desktop name resolves to (the name
// itself if it doesn't).
func resolvedID(what, name string) string {
	if what == "distro" {
		if d, err := conf.ResolveDistro(name); err == nil {
			return d.ID
		}
	} else if de, _, err := conf.ResolveDesktopCompat(name); err == nil {
		return de.ID
	}
	return name
}

// preUpgrade runs DISTRO_PRE_UPGRADE (after a refresh, before upgrades),
// with the package manager's diagnosis and recoveries: Ubuntu 26.04's
// switch to GNU coreutils is an apt-get install that runs on every 2.0.x
// install and failed on Play Termux with no log or retry (support #147).
func preUpgrade(ctx context.Context, ops *pkgOps, d *conf.Distro, r ui.Reporter) error {
	if d.PreUpgrade == "" {
		return nil
	}
	ops.lg.Printf("package manager: preparing %s for updates: %s", d.Label(), d.PreUpgrade)
	if err := ops.run(ctx, d.PreUpgrade, 0, r); err != nil {
		return pkgErr("Couldn't prepare "+d.Label()+" for updates", err)
	}
	return nil
}

// editionName is what users see for a Modded edition id (owner naming):
// "Modded 2.0" for the lineup, "Classic" for the archived legacy-* ones.
// downloadLabel names the download step. A free install shows the distro
// ("Downloading Ubuntu 26.04"); a paid edition shows the edition instead
// ("Downloading Ubuntu KDE Plasma (Classic)"), so a Classic or Modded
// download no longer reads as a plain distro (#286).
func downloadLabel(d *conf.Distro, de *conf.Desktop, edition string) string {
	if edition == "" {
		return "Downloading " + d.Label()
	}
	kind := "Modded"
	if strings.HasPrefix(edition, "legacy-") {
		kind = "Classic"
	}
	name := d.Name
	if !de.None() {
		name += " " + de.Name
	}
	return "Downloading " + name + " (" + kind + ")"
}

func editionName(id string) string {
	if strings.HasPrefix(id, "legacy-") {
		return "Andronix Classic"
	}
	return "Andronix Modded 2.0"
}

// earlyErr explains a refused download of an early-access edition (nil
// for anything else).
func earlyErr(err error, ed *conf.Edition) error {
	var se *netx.StatusError
	if ed == nil || !ed.Early || !errors.As(err, &se) || se.Code != 401 && se.Code != 403 {
		return nil
	}
	return &ui.UserError{Title: "Early access: Premium", Class: "early_access",
		What: editionName(ed.ID) + " (" + ed.ID + ") is in early access for Andronix Premium and Modded Pass owners, before everyone gets it.",
		Fix:  "Get Premium or the Modded Pass in the Andronix app, then copy the install command again.", Err: err}
}
