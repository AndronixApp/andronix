package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/oci"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// errLayerDamaged: a registry layer didn't match its digest.
var errLayerDamaged = errors.New("a layer's checksum doesn't match")

// pullLayers downloads the image's layers into dir, each one checked against
// its digest; a layer already there and intact is kept. progress gets the
// bytes of the whole image so far.
func pullLayers(ctx context.Context, im *oci.Image, dir string, lg *Logger, progress func(cur, total int64)) error {
	os.MkdirAll(dir, 0o755)
	var done int64
	for _, l := range im.Layers {
		dst := filepath.Join(dir, strings.TrimPrefix(l.Digest, "sha256:"))
		if sum, _ := netx.FileSHA256(dst); "sha256:"+sum == l.Digest {
			done += l.Size
			continue
		}
		base := done
		blob := func() (string, error) {
			return netx.Download(ctx, im.BlobURL(l), dst+".part", im.Headers(), l.Size,
				func(c, _ int64) { progress(base+c, im.Total) })
		}
		sum, err := blob()
		var se *netx.StatusError
		if errors.As(err, &se) && se.Code == 401 {
			// The registry (docker.io) bearer expired mid-pull, not
			// the user's token (#243/#658): refresh it and resume the
			// blob from the .part (netx.Download Ranges).
			lg.Printf("registry blob %s: 401, refreshing bearer", l.Digest)
			if rerr := im.RefreshToken(ctx); rerr == nil {
				sum, err = blob()
			}
		}
		if err != nil {
			return err
		}
		if "sha256:"+sum != l.Digest {
			os.Remove(dst + ".part")
			return errLayerDamaged
		}
		os.Rename(dst+".part", dst)
		done += l.Size
	}
	return nil
}

// registryRefused: the registry answered a layer download with 401 (after a
// fresh bearer), 403 or 429. Docker Hub does that per network or region
// (card #394: 7% of download-step failures, Nov 6-9), so trying it again
// from the same phone gets the same answer.
func registryRefused(err error) bool {
	var se *netx.StatusError
	return errors.As(err, &se) && (se.Code == 401 || se.Code == 403 || se.Code == 429)
}

// mirrorAfterRegistry looks for our mirror's tarball after the registry
// refused a layer (#394; #357 did the same for "Finding the best download").
// The tarball is used only with its sha256 known, so the download is checked
// like any other. ANDRONIX_SOURCE=registry keeps the registry (tests).
func mirrorAfterRegistry(ctx context.Context, err error, d *conf.Distro, arch sys.Arch, p Paths, lg *Logger, say func(string)) (source, bool) {
	if !registryRefused(err) || ctx.Err() != nil || os.Getenv("ANDRONIX_SOURCE") == "registry" {
		return source{}, false
	}
	lg.Printf("registry refused the download (%v); trying the Andronix mirror", err)
	s, ok, _ := mirrorTarball(ctx, d, arch, p, lg)
	if !ok || s.sha == "" {
		lg.Printf("no usable tarball on the Andronix mirror; the registry's error stands")
		return source{}, false
	}
	if say != nil {
		say("The image server refused the download; using the Andronix mirror instead...")
	}
	return s, true
}

// downloadRegistry is the download step for a registry image: its layers,
// each checked against its digest. When the registry refuses one (#394) and
// our mirror has the distro's tarball, *src becomes that tarball and tarball
// (the step's download of *src, sha256 checked) fetches it; the layers
// fetched so far go first, so the phone doesn't hold both, and the tarball
// gets the space check it gets as the step's first choice (downloadSpaceErr;
// the registry's layers have none). viaMirror: the source is the tarball
// now, not the layers.
func downloadRegistry(ctx context.Context, src *source, d *conf.Distro, arch sys.Arch, p Paths, lg *Logger, r ui.Reporter, tarball func() error) (viaMirror bool, err error) {
	dir := filepath.Join(p.Cache, "layers-"+d.ID)
	im := src.image
	err = pullLayers(ctx, im, dir, lg, func(c, t int64) { r.Progress(c, t, "bytes") })
	if err == nil {
		return false, nil
	}
	if errors.Is(err, errLayerDamaged) {
		return false, ui.Errorf("download_is_damaged", "Download is damaged", "A layer's checksum doesn't match, so it was deleted.", "Run the same command again to download it fresh.")
	}
	s, ok := mirrorAfterRegistry(ctx, err, d, arch, p, lg, r.Line)
	if !ok {
		// A 401 that survives the refresh is a plain download failure
		// here, never the user's "token refused".
		return false, registryDownloadErr(err, im.Ref.Host)
	}
	os.RemoveAll(dir)
	*src = s
	if err := downloadSpaceErr(d, s, lg); err != nil {
		return true, err
	}
	return true, tarball()
}

// fetchTarball downloads s.url to s.file+".part" and checks its sha256.
// damaged: the checksum didn't match and the file was deleted.
func fetchTarball(ctx context.Context, s source, progress func(cur, total int64)) (damaged bool, err error) {
	part := s.file + ".part"
	sum, err := netx.Download(ctx, s.url, part, nil, s.size, progress)
	if err != nil {
		return false, err
	}
	if s.sha != "" && sum != s.sha {
		os.Remove(part)
		return true, nil
	}
	return false, nil
}

// imageServerBusy is a registry's 429 in words.
func imageServerBusy() error {
	return ui.Errorf("the_image_server_is_busy", "The image server is busy", "Docker Hub limits downloads per network, and your network hit the limit.",
		"Wait an hour, or switch between Wi-Fi and mobile data, then run the same command again.")
}

// registryDownloadErr turns a failed layer download into a user error: Docker
// Hub's 429 names its limit, as at "Finding the best download"; the rest is
// a failed download (never the user's "token refused", #243).
func registryDownloadErr(err error, host string) error {
	var se *netx.StatusError
	if errors.As(err, &se) && se.Code == 429 && host == "registry-1.docker.io" {
		return imageServerBusy()
	}
	return downloadErr(err, false)
}
