package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// `andronix apps <distro> --json`: the distro's desktop apps for the
// Andronix app's home-screen shortcuts. It only reads files (no proot,
// except to render SVG-only icons), so it works while a desktop runs.
// Icons are copied as PNGs to $ANDRONIX_HOME/icons/<distro>/<id>.png, where
// the app reads them without the rootfs's symlinks.

// App is one entry of the list.
type App struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Comment  string  `json:"comment"`
	Icon     *string `json:"icon"`
	Terminal bool    `json:"terminal"`
	// Categories as written in the .desktop file (split on ';', empties
	// dropped); never null.
	Categories []string `json:"categories"`
	exec       string
	iconName   string
}

var appID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// appsError is the JSON error object (stdout, exit 1).
func appsError(class, msg string) error {
	b, _ := json.Marshal(map[string]string{"error": class, "message": msg})
	fmt.Println(string(b))
	return errAppsJSON
}

var errAppsJSON = &ExitError{Code: 1}

// Apps prints the list (JSON array, sorted by name).
func Apps(name string, asJSON bool) error {
	d, err := conf.LoadDistro(name)
	if name == "" || err != nil {
		return appsError("no_distro", "There's no distro called '"+name+"'.")
	}
	in := Open(d)
	if !in.Installed() {
		return appsError("not_installed", d.Label()+" isn't installed.")
	}
	if _, err := os.Stat(filepath.Join(in.Rootfs, "usr")); err != nil {
		return appsError("broken", d.Label()+"'s files are missing.")
	}
	de, _ := conf.ResolveDesktop(in.Get("DE"))
	apps := listApps(in.Rootfs, de)
	dir := filepath.Join(GetPaths().Home, "icons", d.ID)
	os.MkdirAll(dir, 0o755)
	var svgs []svgJob
	canRender := guestHasProgram(in.Rootfs, "rsvg-convert")
	for i := range apps {
		a := &apps[i]
		ic := findIcons(in.Rootfs, a.iconName, de)
		dst := filepath.Join(dir, a.ID+".png")
		switch {
		case ic.big != "":
			if copyIfNewer(ic.big, dst) == nil {
				a.Icon = &dst
			}
		case ic.svg != "" && canRender:
			svgs = append(svgs, svgJob{guest: strings.TrimPrefix(ic.svg, in.Rootfs), dst: dst, app: a, fallback: ic.small})
		case ic.small != "": // smaller than wanted, but better than none
			if copyIfNewer(ic.small, dst) == nil {
				a.Icon = &dst
			}
		}
	}
	renderSVGs(in, svgs)
	if !asJSON {
		for _, a := range apps {
			fmt.Printf("%-24s %s\n", a.ID, a.Name)
		}
		return nil
	}
	if apps == nil {
		apps = []App{}
	}
	b, _ := json.Marshal(apps)
	fmt.Println(string(b))
	return nil
}

// listApps reads the rootfs's .desktop files: applications the desktop
// shows (not NoDisplay or Hidden, OnlyShowIn/NotShowIn for this desktop,
// TryExec present), sorted by name.
func listApps(root string, de *conf.Desktop) []App {
	xdg := ""
	if de != nil {
		xdg = de.XDGName()
	}
	seen := map[string]bool{}
	var out []App
	for _, dir := range []string{"usr/local/share/applications", "usr/share/applications"} {
		ents, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range ents {
			n := e.Name()
			if !strings.HasSuffix(n, ".desktop") {
				continue
			}
			id := strings.TrimSuffix(n, ".desktop")
			if seen[id] || !appID.MatchString(id) {
				continue
			}
			seen[id] = true
			f, err := os.Open(filepath.Join(root, dir, n))
			if err != nil {
				continue
			}
			kv := desktopEntry(f)
			f.Close()
			if a, ok := appFrom(id, kv, xdg, root); ok {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// desktopEntry is the [Desktop Entry] group's keys (unlocalized, plus the
// localized ones as Key[lang]).
func desktopEntry(r io.Reader) map[string]string {
	kv := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	in := false
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			in = l == "[Desktop Entry]"
			continue
		}
		if !in || l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if ok {
			if _, dup := kv[strings.TrimSpace(k)]; !dup {
				kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return kv
}

func appFrom(id string, kv map[string]string, xdg, root string) (App, bool) {
	if kv["Type"] != "Application" || kv["Name"] == "" || kv["Exec"] == "" {
		return App{}, false
	}
	if kv["NoDisplay"] == "true" || kv["Hidden"] == "true" {
		return App{}, false
	}
	if only := kv["OnlyShowIn"]; only != "" && (xdg == "" || !inList(only, xdg)) {
		return App{}, false
	}
	if not := kv["NotShowIn"]; not != "" && xdg != "" && inList(not, xdg) {
		return App{}, false
	}
	if t := kv["TryExec"]; t != "" && !guestHasProgram(root, t) {
		return App{}, false
	}
	cats := []string{}
	for _, c := range strings.Split(kv["Categories"], ";") {
		if c = strings.TrimSpace(c); c != "" {
			cats = append(cats, c)
		}
	}
	return App{ID: id, Name: kv["Name"], Comment: kv["Comment"], Terminal: kv["Terminal"] == "true",
		Categories: cats, exec: kv["Exec"], iconName: kv["Icon"]}, true
}

func inList(list, item string) bool {
	for _, x := range strings.Split(list, ";") {
		if strings.EqualFold(strings.TrimSpace(x), item) {
			return true
		}
	}
	return false
}

// guestHasProgram: a TryExec name on the distro's PATH, or an absolute path.
func guestHasProgram(root, prog string) bool {
	if strings.HasPrefix(prog, "/") {
		_, err := os.Stat(guestPath(root, prog))
		return err == nil
	}
	for _, d := range []string{"usr/local/bin", "usr/bin", "bin", "usr/games", "usr/local/games"} {
		if _, err := os.Stat(guestPath(root, "/"+d+"/"+prog)); err == nil {
			return true
		}
	}
	return false
}

// guestPath resolves a path as the distro sees it to a host path, following
// symlinks inside the rootfs (an absolute link target is the guest's /).
func guestPath(root, p string) string {
	parts := strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/")
	cur := "/"
	for hops := 0; len(parts) > 0 && hops < 64; {
		next := path.Join(cur, parts[0])
		parts = parts[1:]
		host := filepath.Join(root, next)
		st, err := os.Lstat(host)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		hops++
		t, err := os.Readlink(host)
		if err != nil {
			cur = next
			continue
		}
		if !strings.HasPrefix(t, "/") {
			t = path.Join(cur, t)
		}
		parts = append(strings.Split(strings.Trim(path.Clean(t), "/"), "/"), parts...)
		cur = "/"
	}
	return filepath.Join(root, cur)
}

// iconMin is the smallest size the app wants (it scales down nicely).
const iconMin = 192

// icons are an app icon's candidates: the smallest PNG of at least
// iconMin px, a scalable SVG, and the largest PNG of any size.
type icons struct{ big, svg, small string }

// findIcons looks in the desktop's themes, then hicolor, then
// /usr/share/pixmaps (host paths, resolved inside the rootfs).
func findIcons(root, name string, de *conf.Desktop) icons {
	var ic icons
	if name == "" {
		return ic
	}
	if strings.HasPrefix(name, "/") {
		p := guestPath(root, name)
		switch {
		case !exists(p):
		case strings.HasSuffix(name, ".svg"):
			ic.svg = p
		case strings.HasSuffix(name, ".png"):
			ic.small = p
		}
		return ic
	}
	themes := []string{}
	if de != nil {
		themes = append(themes, de.IconThemes()...)
	}
	themes = append(themes, "hicolor")
	bigSize, smallSize := 0, 0
	for _, th := range themes {
		gbase := "/usr/share/icons/" + th
		sizes, _ := os.ReadDir(guestPath(root, gbase))
		for _, s := range sizes {
			sz := iconDirSize(s.Name())
			for _, cat := range []string{"apps", "applications"} {
				// Each candidate resolved inside the rootfs: icon files are
				// often symlinks with absolute (guest) targets.
				if sz == 0 {
					if p := guestPath(root, gbase+"/"+s.Name()+"/"+cat+"/"+name+".svg"); ic.svg == "" && exists(p) {
						ic.svg = p
					}
					continue
				}
				p := guestPath(root, gbase+"/"+s.Name()+"/"+cat+"/"+name+".png")
				if !exists(p) {
					continue
				}
				if sz >= iconMin && (ic.big == "" || sz < bigSize) {
					ic.big, bigSize = p, sz
				}
				if sz > smallSize {
					ic.small, smallSize = p, sz
				}
			}
		}
		if ic.big != "" {
			return ic
		}
	}
	for _, ext := range []string{".png", ".svg"} {
		p := guestPath(root, "/usr/share/pixmaps/"+name+ext)
		switch {
		case !exists(p):
		case ext == ".svg" && ic.svg == "":
			ic.svg = p
		case ext == ".png" && ic.small == "":
			ic.small = p
		}
	}
	return ic
}

// iconDirSize is 128 for "128x128" or "128x128@2" (256), 0 for scalable.
func iconDirSize(n string) int {
	w, rest, ok := strings.Cut(n, "x")
	if !ok {
		return 0
	}
	h, scale, _ := strings.Cut(rest, "@")
	a, err1 := strconv.Atoi(w)
	b, err2 := strconv.Atoi(h)
	if err1 != nil || err2 != nil || a != b {
		return 0
	}
	if s, err := strconv.Atoi(scale); err == nil && s > 1 {
		a *= s
	}
	return a
}

func copyIfNewer(src, dst string) error {
	ss, err := os.Stat(src)
	if err != nil {
		return err
	}
	if ds, err := os.Stat(dst); err == nil && !ds.ModTime().Before(ss.ModTime()) && ds.Size() > 0 {
		return nil
	}
	tmp := dst + ".tmp"
	if err := copyFile(src, tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

type svgJob struct {
	guest, dst, fallback string // fallback: a smaller PNG if rendering fails
	app                  *App
}

// renderSVGs turns SVG-only icons into 256 px PNGs with the distro's
// rsvg-convert, in one short proot run; without it those icons stay null.
func renderSVGs(in *Inst, jobs []svgJob) {
	var todo []svgJob
	for _, j := range jobs {
		if ds, err := os.Stat(j.dst); err == nil && ds.Size() > 0 {
			dst := j.dst
			j.app.Icon = &dst
			continue
		}
		todo = append(todo, j)
	}
	if len(todo) == 0 {
		return
	}
	defer func() { // what didn't render gets its smaller PNG
		for _, j := range todo {
			if j.app.Icon == nil && j.fallback != "" && copyIfNewer(j.fallback, j.dst) == nil {
				dst := j.dst
				j.app.Icon = &dst
			}
		}
	}()
	work := filepath.Join(in.Rootfs, "tmp/andronix-icons")
	os.RemoveAll(work)
	os.MkdirAll(work, 0o755)
	defer os.RemoveAll(work)
	var sh strings.Builder
	for i, j := range todo {
		fmt.Fprintf(&sh, "rsvg-convert -w 256 -h 256 %s -o /tmp/andronix-icons/%d.png 2>/dev/null\n", shellQuote(j.guest), i)
	}
	t := in.Target(true)
	if err := t.Run(context.Background(), sh.String(), nil, nil); err != nil {
		return
	}
	for i, j := range todo {
		if copyIfNewer(filepath.Join(work, strconv.Itoa(i)+".png"), j.dst) == nil {
			dst := j.dst
			j.app.Icon = &dst
		}
	}
}
