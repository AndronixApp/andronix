package app

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/rootfs"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/telemetry"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Start is `andronix start <distro> [--root] [-- command...]`.
// `andronix start <distro> --x11` opens the desktop on Termux:X11 instead
// (Desktop).
func Start(name string, asRoot bool, cmd []string) error {
	d, err := resolveDistro(name, "start")
	if err != nil {
		return err
	}
	in := Open(d)
	if !in.Installed() {
		if _, err := os.Stat(in.Rootfs); err == nil {
			return ui.Errorf("not_finished", d.Label()+" isn't finished", "The last install stopped part way.", "Run: andronix install "+d.ID+" (it picks up where it stopped).")
		}
		if l := in.Legacy(); len(l) > 0 {
			return ui.Errorf("not_installed", d.Label()+" isn't installed", "You have an older "+d.Name+" from the previous installer at "+ui.Tilde(l[0])+".",
				"Start that one with ./"+strings.TrimSuffix(d.MainStart(), ".sh")+"-old.sh, or install the new one: andronix install "+d.ID)
		}
		return ui.Errorf("not_installed", d.Label()+" isn't installed", "There's nothing to start yet.", "Install it first: andronix install "+d.ID)
	}
	if !proot.Available() {
		return ui.Errorf("proot_is_missing", "proot is missing", "Andronix needs proot to run Linux inside Termux.", "Install it: pkg install proot -y")
	}
	rootfs.EnsureBwrapShim(in.Rootfs) // self-heal after the user's own updates
	rootfs.QuietSudo(in.Rootfs)       // and after a switch to sudo-rs
	// The in-distro andronix is this build's (a no-op when the stamp
	// matches): in the app a distro the beta installer used may be started
	// by the stable one next (card #209), and the helper must match the host.
	rootfs.InstallSelf(in.Rootfs)
	startSound(context.Background(), len(cmd) == 0 && ui.Interactive)
	p := map[string]any{"distro": d.ID, "de": in.Get("DE"), "root": asRoot, "command": len(cmd) > 0}
	telemetry.Send("start", p) // kept for one release next to cli_login (telemetry v2)
	telemetry.Send("cli_login", p)
	return in.Target(asRoot).Login(d.Shell, cmd)
}

// Remove is `andronix remove <distro> [--yes] [--legacy]`.
func Remove(ctx context.Context, name string, legacy bool) error {
	if ed, err := conf.ResolveEdition(name); err == nil {
		return removeEdition(ctx, ed, legacy)
	}
	d, err := resolveDistro(name, "remove")
	if err != nil {
		return err
	}
	unlock, err := lockDistro(d, "remove")
	if err != nil {
		return err
	}
	defer unlock()
	in := Open(d)
	var old []string
	if legacy {
		old = in.Legacy()
	}
	ui.SetDone("distro", d.ID)
	_, statErr := os.Stat(in.Dir)
	if statErr != nil && len(old) == 0 {
		ui.SetDone("outcome", "not_installed")
		fmt.Println()
		ui.OK(d.Label() + " isn't installed, so there's nothing to remove.")
		if freed := removeCache(cacheEntries(GetPaths().Cache, d.ID)); freed > 0 {
			ui.Note("Deleted " + ui.Bytes(freed) + " of " + d.Name + " downloads from an earlier install.")
		}
		if l := in.Legacy(); len(l) > 0 {
			ui.Note("An older " + d.Name + " from the previous installer is at " + ui.Tilde(l[0]) + ". Remove it with: andronix remove " + d.ID + " --legacy")
		}
		fmt.Println()
		return nil
	}
	lines := []string{}
	if statErr == nil {
		lines = append(lines, "Deletes "+d.Label()+" and everything inside it.", ui.KV("Folder", ui.Tilde(in.Dir)+" ("+ui.Bytes(diskSize(in.Dir))+")"), ui.KV("Launcher", "~/"+d.MainStart()))
		if have := installedEdition(in); have != "" {
			lines = append(lines, "", "It's "+editionLabel(d, have)+".")
		}
		if n := cacheSize(cacheEntries(GetPaths().Cache, d.ID)); n > 0 {
			lines = append(lines, ui.KV("Downloads", ui.Bytes(n)+" in "+ui.Tilde(GetPaths().Cache)))
		}
	}
	for _, l := range old {
		lines = append(lines, "", "Also deletes the older install:", ui.KV("Folder", ui.Tilde(l)+" ("+ui.Bytes(diskSize(l))+")"))
	}
	lines = append(lines, "", "Files in /sdcard are not touched.")
	fmt.Println()
	fmt.Print(ui.Box(ui.BoxWarn, "Remove "+d.Label()+"?", lines...))
	fmt.Println()
	ok, err := ui.Confirm("Remove it?", "", false)
	if err != nil {
		return err
	}
	if !ok {
		ui.Note("Nothing was removed.")
		return nil
	}
	lg := NewLog("remove-" + d.ID)
	defer lg.Close()
	var steps []ui.Step
	if statErr == nil {
		steps = append(steps, ui.Step{Label: "Removing " + d.Label(), Key: "remove", Run: func(ctx context.Context, r ui.Reporter) error {
			if err := rootfs.Remove(in.Dir); err != nil {
				return &ui.UserError{Title: "Couldn't remove everything", Class: "remove_failed", What: err.Error(),
					Fix: "Exit every " + d.Name + " session (and vncserver-stop), then try again."}
			}
			RemoveLaunchers(d)
			removeCache(cacheEntries(GetPaths().Cache, d.ID))
			os.RemoveAll(filepath.Join(GetPaths().Home, "icons", d.ID)) // andronix apps' copies
			return nil
		}})
	}
	for _, l := range old {
		l := l
		steps = append(steps, ui.Step{Label: "Removing the older install", Run: func(ctx context.Context, r ui.Reporter) error {
			if err := rootfs.Remove(l); err != nil {
				return ui.Errorf("couldn_t_remove_the_older_install", "Couldn't remove the older install", err.Error(), "Exit every session of it, then try again.")
			}
			for _, n := range d.StartScripts {
				matches, _ := filepath.Glob(filepath.Join(sys.Home(), strings.TrimSuffix(n, ".sh")+"-old*.sh"))
				for _, f := range append(matches, filepath.Join(sys.Home(), n)) {
					if b, err := os.ReadFile(f); err == nil && !strings.Contains(string(b), LauncherMark) {
						os.Remove(f)
					}
				}
			}
			if d.BindsDir != "" {
				os.RemoveAll(filepath.Join(sys.Home(), d.BindsDir))
			}
			return nil
		}})
	}
	fmt.Println()
	if err := ui.RunSteps(ctx, steps, lg); err != nil {
		return err
	}
	fmt.Println()
	ui.OK(d.Label() + " is gone. Reinstall any time from the Andronix app.")
	if _, err := os.Stat(filepath.Join(sys.Home(), d.BindsDir)); err == nil && d.BindsDir != "" {
		ui.Note("Your bind settings in ~/" + d.BindsDir + " were kept.")
	}
	fmt.Println()
	return nil
}

// List is `andronix list`.
func List() error {
	fmt.Print(ui.Banner(""))
	ui.Section("Installed")
	any := false
	for _, id := range conf.DistroIDs() {
		d, _ := conf.LoadDistro(id)
		in := Open(d)
		switch {
		case in.Installed():
			ui.Row(ui.GOK+" "+id, d.Label()+" "+ui.GSep+" "+in.Get("DE_NAME")+"  ./"+d.MainStart(), 12)
			any = true
		case exists(in.Dir):
			ui.Row(ui.GWarn+" "+id, d.Label()+" "+ui.GSep+" unfinished: andronix install "+id, 12)
			any = true
		}
		for _, l := range in.Legacy() {
			ui.Row(ui.GDot+" "+id, d.Name+" from the old installer ("+ui.Tilde(l)+")", 12)
			any = true
		}
	}
	if !any {
		ui.Note("Nothing yet.")
	}
	ui.Section("Available")
	for _, id := range conf.DistroIDs() {
		d, _ := conf.LoadDistro(id)
		label := d.Label()
		if d.Codename != "" {
			label += " (" + d.Codename + ")"
		}
		ui.Row("  "+id, label, 12)
	}
	ui.Section("Desktops")
	for _, id := range conf.DesktopIDs() {
		de, _ := conf.ResolveDesktop(id)
		ui.Row("  "+id, de.Name, 12)
	}
	fmt.Println()
	ui.Note("Install one with: andronix install debian --de xfce")
	fmt.Println()
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Backup is `andronix backup <distro> [file]`.
// Backup is `andronix backup <distro> [file] [--to storage]`.
func Backup(ctx context.Context, name, out string, toStorage bool) error {
	d, err := resolveDistro(name, "backup")
	if err != nil {
		return err
	}
	in := Open(d)
	if !in.Installed() {
		return ui.Errorf("not_installed", d.Label()+" isn't installed", "There's nothing to back up.", "Install it first: andronix install "+d.ID)
	}
	if out == "" {
		dir := sys.Home()
		if toStorage {
			if dir, err = ensureStorage(); err != nil {
				return err
			}
		}
		out = filepath.Join(dir, "andronix-"+d.ID+"-"+time.Now().Format("20060102-1504")+".tar.gz")
	}
	lg := NewLog("backup-" + d.ID)
	defer lg.Close()
	fmt.Println()
	err = ui.RunSteps(ctx, []ui.Step{{Label: "Backing up " + d.Label(), Run: func(ctx context.Context, r ui.Reporter) error {
		if err := writeBackup(ctx, filepath.Dir(in.Dir), d.ID, out, r); err != nil {
			os.Remove(out)
			return ui.Errorf("backup_failed", "Backup failed", err.Error(), "Check free space, and exit "+d.Name+" first so no files change.")
		}
		return nil
	}}}, lg)
	if err != nil {
		return err
	}
	st, _ := os.Stat(out)
	fmt.Println()
	ui.OK(fmt.Sprintf("Saved to %s (%s).", ui.Tilde(out), ui.Bytes(st.Size())))
	ui.Note("Restore it with: andronix restore " + ui.Tilde(out))
	fmt.Println()
	return nil
}

func writeBackup(ctx context.Context, base, id, out string, r ui.Reporter) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	n := 0
	// The backup is portable: link2symlink groups (.l2s data + symlinks
	// holding this phone's host path) are written as one file plus tar
	// hard links, other host-path symlinks become guest paths, and the
	// .l2s files themselves are left out. Restore turns the hard links
	// into copies where Android refuses links.
	root := filepath.Join(base, id, "rootfs")
	written := map[string]string{} // .l2s data file -> archive name
	writeFile := func(h *tar.Header, src string) error {
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := os.Open(src)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	err = filepath.Walk(filepath.Join(base, id), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(base, p)
		if strings.HasPrefix(rel, id+"/shm/") || rootfs.IsL2S(p) {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
			if data := rootfs.L2SData(root, p); data != "" {
				if first, ok := written[data]; ok {
					h := &tar.Header{Typeflag: tar.TypeLink, Name: rel, Linkname: first, ModTime: info.ModTime()}
					return tw.WriteHeader(h)
				}
				st, err := os.Stat(data)
				if err != nil {
					return nil
				}
				h, _ := tar.FileInfoHeader(st, "")
				h.Name = rel
				h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
				written[data] = rel
				n++
				return writeFile(h, data)
			}
			if g, ok := rootfs.GuestTarget(root, link); ok {
				link = g
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil
		}
		h.Name = rel
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if info.Mode().IsRegular() {
			if err := writeFile(h, p); err != nil {
				return err
			}
		} else if err := tw.WriteHeader(h); err != nil {
			return err
		}
		n++
		if n%500 == 0 {
			r.Label(fmt.Sprintf("Backing up %s (%d files)", id, n))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Restore is `andronix restore <file> [--replace]`. With "-" the backup
// comes in on stdin (the Andronix app streams a file the user picked: a
// backup made in Termux moves into the app this way), and --bytes N gives
// its size for the progress. Without a terminal (the app, --progress json)
// an installed distro is only replaced with --replace.
func Restore(ctx context.Context, file string, replace bool, size int64) error {
	if file == "" {
		var err error
		if file, err = pickBackup(); err != nil || file == "" {
			return err
		}
	}
	var src io.Reader
	name := "'" + file + "'"
	if file == "-" {
		src, name = os.Stdin, "The backup"
	} else {
		f, err := os.Open(file)
		if err != nil {
			return ui.Errorf("backup_not_found", "Backup not found", "There's no file at '"+file+"'.", "Pass the path of a backup made with 'andronix backup'.")
		}
		defer f.Close()
		if st, err := f.Stat(); err == nil {
			size = st.Size()
		}
		src = f
	}
	br := bufio.NewReaderSize(src, 1<<20)
	id := backupID(br)
	d, err := conf.LoadDistro(id)
	if id == "" || err != nil {
		return &ui.UserError{Title: "Not an Andronix backup", Class: "not_a_backup",
			What: name + " doesn't look like a backup from 'andronix backup'.", Fix: "Check you picked the right file."}
	}
	ui.SetDone("distro", d.ID)
	unlock, err := lockDistro(d, "restore")
	if err != nil {
		return err
	}
	defer unlock()
	in := Open(d)
	if exists(in.Dir) && !ui.Interactive && !replace {
		return &ui.UserError{Title: d.Label() + " is already installed", Class: "restore_would_replace",
			What: "Restoring would replace it with the one in the backup.", Fix: "Remove it first, or restore with --replace."}
	}
	if exists(in.Dir) {
		fmt.Println()
		fmt.Print(ui.Box(ui.BoxWarn, "Replace "+d.Label()+"?", "Restoring replaces the "+d.Label()+" you have now with the one in the backup."))
		ok, err := ui.Confirm("Replace it?", "", false)
		if err != nil || !ok {
			ui.Note("Nothing changed.")
			return err
		}
		rootfs.Remove(in.Dir)
	}
	lg := NewLog("restore-" + d.ID)
	defer lg.Close()
	fmt.Println()
	err = ui.RunSteps(ctx, []ui.Step{
		{Label: "Restoring " + d.Label(), Key: "restore", Run: func(ctx context.Context, r ui.Reporter) error {
			os.MkdirAll(GetPaths().Distros, 0o755)
			x := &rootfs.Extractor{Root: GetPaths().Distros, Total: size, Progress: func(c, t int64) { r.Progress(c, t, "bytes") }}
			if err := x.ExtractReader(br); err != nil {
				rootfs.Remove(in.Dir) // half a distro would look installed
				return &ui.UserError{Title: "Restore failed", Class: "restore_failed", What: err.Error(), Fix: "Free up space and try again."}
			}
			x.Finish()
			// Backups from before portable backups kept link2symlink
			// links with the old phone's host path; point them here.
			if k := rootfs.RepairHostLinks(in.Rootfs); k > 0 {
				ui.DetailOf(r, "links.repaired", fmt.Sprintf("%d links repaired", k), map[string]any{"count": k})
			}
			rootfs.InstallSelf(in.Rootfs)
			// A backup made during a session carries its DNS forwarders'
			// port files (card #141): they belong to another phone or app.
			os.RemoveAll(filepath.Join(in.Rootfs, "run/andronix/dns"))
			return nil
		}},
		{Label: finishLabel(d), Key: "finish", Run: func(ctx context.Context, r ui.Reporter) error {
			if sys.Standalone() { // the app starts it; no launcher scripts
				return nil
			}
			_, err := WriteLaunchers(d)
			return err
		}},
	}, lg)
	if err != nil {
		return err
	}
	fmt.Println()
	if sys.Standalone() {
		ui.OK(d.Label() + " is back.")
	} else {
		ui.OK(d.Label() + " is back. Start it with ./" + d.MainStart())
	}
	fmt.Println()
	return nil
}

// backupID is the distro a backup holds: its first entry's top folder.
// It only peeks, so the stream can still be unpacked from the start.
func backupID(br *bufio.Reader) string {
	b, _ := br.Peek(256 << 10)
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return ""
	}
	h, err := tar.NewReader(gz).Next()
	if err != nil {
		return ""
	}
	return strings.SplitN(strings.TrimPrefix(h.Name, "./"), "/", 2)[0]
}
