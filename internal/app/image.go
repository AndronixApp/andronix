package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/rootfs"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// Ready-made desktop images: a free install's rootfs after its package
// steps (base distro + desktop packages, no browser), built by
// ci/build-image.sh with `andronix install --image-build`. Nothing of ours
// is in an image: setup, compat, desktop-setup and the user step run after
// unpacking exactly as for a package install, so installer changes never
// need a rebuild. An image is only a cache: when there's none, or it fails
// its checksum or download, the install falls back to the base rootfs and
// the package steps, silently.
//
//	<mirror>/rootfs/<id>/<ver>/<id>-<ver>-<de>-<arch>.json      the pointer
//	<mirror>/rootfs/<id>/<ver>/<id>-<ver>-<de>-<arch>-<date>.tar.zst (+ .sha256, .meta)
//
// ANDRONIX_IMAGE=<file> uses a local image (with <file>.sha256 next to it,
// and optionally <file>.meta), like ANDRONIX_ROOTFS for the base.
// ANDRONIX_NO_IMAGE=1 turns images off.

// Fallback reasons (install.conf FALLBACK, telemetry fallback_reason).
const (
	fbNoImage  = "no_image"    // none published for this distro, desktop and arch
	fbNoSum    = "no_checksum" // a pointer (or local image) without a valid sha256: never used unchecked
	fbOffline  = "unreachable" // the mirror didn't answer
	fbChecksum = "checksum"    // the stream didn't match its .sha256
	fbDownload = "download"    // the download failed after retries
	fbUnpack   = "unpack"      // the archive couldn't be unpacked
	fbNoSpace  = "nospace"     // the phone ran out of space mid-unpack (#1147): abort, don't fall back
	fbNoDesk   = "no_desktop"  // unpacked, but the desktop's session isn't in it
	fbOff      = "off"         // ANDRONIX_NO_IMAGE
)

func imageName(d *conf.Distro, de *conf.Desktop, arch sys.Arch) string {
	return fmt.Sprintf("%s-%s-%s-%s.tar.zst", d.ID, d.Version, de.ID, arch)
}

// imageEligible: images are only for free installs with a desktop.
// Editions, Modded and Classic have their own images; --de none needs
// none; an explicit ANDRONIX_ROOTFS means "this base", as before.
func imageEligible(o InstallOpts, edition string, de *conf.Desktop) bool {
	return !o.ImageBuild && edition == "" && !o.Modded && !de.None() && os.Getenv("ANDRONIX_ROOTFS") == ""
}

// imageMeta reads the unpacked size from a .meta (key=value lines;
// unpacked=<bytes>). 0 when unknown.
func imageMeta(b []byte) (unpacked int64) {
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k == "unpacked" {
			unpacked, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return unpacked
}

func firstField(b []byte) string {
	if fs := strings.Fields(string(b)); len(fs) > 0 && sha256Re.MatchString(fs[0]) {
		return fs[0]
	}
	return ""
}

// findImage looks for this install's image. It returns the source, or
// the fallback reason when there's none to use.
func findImage(ctx context.Context, d *conf.Distro, de *conf.Desktop, arch sys.Arch, lg *Logger) (source, string) {
	if os.Getenv("ANDRONIX_NO_IMAGE") != "" {
		return source{}, fbOff
	}
	if f := os.Getenv("ANDRONIX_IMAGE"); f != "" {
		st, err := os.Stat(f)
		if err != nil {
			lg.Printf("image %s: %v", f, err)
			return source{}, fbNoImage
		}
		s := source{kind: "image", file: f, size: st.Size()}
		if b, err := os.ReadFile(f + ".sha256"); err == nil {
			s.sha = firstField(b)
		}
		if b, err := os.ReadFile(strings.TrimSuffix(f, ".tar.zst") + ".meta"); err == nil {
			s.unpacked = imageMeta(b)
		}
		if s.sha == "" {
			return source{}, fbNoSum
		}
		return s, ""
	}
	// The pointer (ci/build-image.sh, published last by the weekly build):
	// which dated image is current. No pointer, no image: deleting it is
	// the kill switch.
	p := imagePointerURL(d, de, arch)
	b, _, err := netx.Get(ctx, p, nil)
	lg.Printf("image pointer %s: %v", p, err)
	if err != nil {
		if se, ok := err.(*netx.StatusError); ok && se.Code < 500 {
			return source{}, fbNoImage
		}
		return source{}, fbOffline
	}
	s, ok := parseImagePointer(b, Mirror())
	if !ok {
		return source{}, fbNoSum
	}
	return s, ""
}

func imagePointerURL(d *conf.Distro, de *conf.Desktop, arch sys.Arch) string {
	return fmt.Sprintf("%s/rootfs/%s/%s/%s.json", Mirror(), d.ID, d.Version, strings.TrimSuffix(imageName(d, de, arch), ".tar.zst"))
}

// imagePointer is <id>-<ver>-<de>-<arch>.json next to the images.
type imagePointer struct {
	URL      string `json:"url"` // a path under the mirror (rootfs/...), or an https URL
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Unpacked int64  `json:"unpacked"`
	Built    string `json:"built"`
}

// parseImagePointer: an image source from a pointer, or false when it
// can't be trusted (no checksum, no URL, not JSON).
func parseImagePointer(b []byte, mirror string) (source, bool) {
	var p imagePointer
	if json.Unmarshal(b, &p) != nil || !sha256Re.MatchString(p.SHA256) || p.URL == "" {
		return source{}, false
	}
	// A path under the mirror, or https on the mirror's own host: no
	// plaintext and no third-party host, even with the checksum (review).
	u := p.URL
	switch {
	case strings.Contains(u, ".."):
		return source{}, false
	case strings.HasPrefix(u, "https://"):
		pu, err1 := url.Parse(u)
		mu, err2 := url.Parse(mirror)
		if err1 != nil || err2 != nil || pu.Host == "" || pu.Host != mu.Host {
			return source{}, false
		}
	case strings.Contains(u, "://"):
		return source{}, false
	default:
		u = strings.TrimRight(mirror, "/") + "/" + strings.TrimLeft(u, "/")
	}
	return source{kind: "image", url: u, size: p.Size, sha: p.SHA256, unpacked: p.Unpacked}, true
}

// unpackImage streams the image (from the mirror or a local file) into x
// while hashing it: no copy on disk. A dropped connection resumes by
// Range inside netx.Stream; anything else is the caller's fallback, with
// its reason. On success it returns the bytes downloaded.
func unpackImage(ctx context.Context, s source, x *rootfs.Extractor, progress func(cur, total int64)) (int64, string, error) {
	var r io.Reader
	var downloaded func() int64
	if s.url != "" {
		st, err := netx.OpenStream(ctx, s.url, s.size, progress)
		if err != nil {
			return 0, fbDownload, err
		}
		defer st.Close()
		r, downloaded = st, st.Bytes
	} else {
		f, err := os.Open(s.file)
		if err != nil {
			return 0, fbUnpack, err
		}
		defer f.Close()
		r, downloaded = f, func() int64 { return 0 }
		x.Total = s.size
	}
	h := sha256.New()
	src := &errReader{r: r}
	tee := io.TeeReader(src, h)
	xerr := x.ExtractReader(tee)
	if ctx.Err() != nil {
		return downloaded(), fbDownload, ctx.Err()
	}
	if src.err != nil {
		return downloaded(), fbDownload, src.err
	}
	// Out of space: a distinct reason so the caller aborts with the clear
	// "Not enough space" result instead of falling back to the package
	// install, which would run the phone out of space all over again (#1147).
	if errors.Is(xerr, syscall.ENOSPC) {
		return downloaded(), fbNoSpace, xerr
	}
	// The phone's own trouble (disk full, permissions): stop now, without
	// downloading the rest just to check it.
	var pe *os.PathError
	var le *os.LinkError
	if errors.As(xerr, &pe) || errors.As(xerr, &le) {
		return downloaded(), fbUnpack, xerr
	}
	// Whatever follows the tar's end (padding, the zstd frame's end) is
	// part of the checksum too. Read it all even after a failed unpack, so
	// a damaged download (checksum) can be told from a bad image (unpack).
	if _, err := io.Copy(io.Discard, tee); err != nil || src.err != nil {
		if src.err != nil {
			err = src.err
		}
		return downloaded(), fbDownload, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != s.sha {
		return downloaded(), fbChecksum, fmt.Errorf("sha256 %s, want %s (unpack: %v)", got, s.sha, xerr)
	}
	if xerr != nil {
		return downloaded(), fbUnpack, xerr
	}
	return downloaded(), "", nil
}

// errReader remembers the source's own error (the network, the file), as
// opposed to the decompressor's or the extractor's.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

// imageInstalled reports a rootfs unpacked from a ready-made image
// (PREBUILT=image) that has the desktop's session: its desktop packages
// are in, so the desktop step is skipped; refresh, update and everything
// after still run. prebuilt()'s yes/no stays as it was for Modded.
func imageInstalled(in *Inst, de *conf.Desktop) bool {
	return in.Get("PREBUILT") == "image" && sessionPresent(in, de)
}

// imageManifestCmd lists a family's installed packages (name, version,
// arch, and the source package where the format has one), for the
// image's .manifest (ci/build-image.sh).
func imageManifestCmd(family string) string {
	switch family {
	case "apt":
		return `dpkg-query -W -f='${Package} ${Version} ${Architecture} ${source:Package} ${source:Version}\n'`
	case "dnf":
		return `rpm -qa --qf '%{NAME} %{VERSION}-%{RELEASE} %{ARCH} %{SOURCERPM}\n'`
	case "pacman":
		return "pacman -Q"
	case "apk":
		return "apk info -v 2>/dev/null"
	case "xbps":
		return "xbps-query -l | awk '{ print $2 }'"
	}
	return ""
}
