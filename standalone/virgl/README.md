# VirGL server for the Andronix app (standalone GPU acceleration)

`libvirgl.so` is virglrenderer's vtest server (`virgl_test_server`), with
virglrenderer and libepoxy linked in statically: one Android arm64
executable, named `lib*.so` so Android installs it into `nativeLibraryDir`.

- The app starts it next to a desktop when Settings > Linux > GPU
  acceleration is on: `libvirgl.so --use-egl-surfaceless --use-gles --socket-path <app tmp>/.virgl_test`.
- It draws with the phone's GLES through Android's libEGL/libGLESv2, which
  libepoxy loads at run time; it needs no other library.
- `andronix display <distro> --gpu virgl --gpu-socket <path>` binds that
  socket to the guest's `/tmp/.virgl_test` and starts the session with
  `GALLIUM_DRIVER=virpipe` (Mesa's vtest client, in every distro's stock
  Mesa). See `internal/app/displaygpu.go`.

| file | what it is |
|---|---|
| `Dockerfile` | Debian bookworm, pinned by digest, with meson and ninja |
| `inside.sh` | the build: checks each archive's sha256, builds libepoxy, then virglrenderer with `android.patch`, 16 KB pages, fixed `SOURCE_DATE_EPOCH`, no build paths |
| `android.patch` | the Android changes, explained at its top (fast readback, zero-copy present) |
| `present_jni.c` | `libandronixpresent.so`, the app's end of the zero-copy present (JNI) |
| `build.sh` | runs it: `DL=<sources> standalone/virgl/build.sh <out>` |
| `SHA256SUMS` | the expected result; the build is reproducible |
| `LICENSES` | every licence involved (shipped: MIT and the NDK's permissive runtime; no GPL) |

Sources (put them in `$DL`):

- `android-ndk-r27c-linux.zip` (the same one as proot's build)
- `virglrenderer-1.3.0.tar.gz`: <https://gitlab.freedesktop.org/virgl/virglrenderer/-/archive/virglrenderer-1.3.0/virglrenderer-virglrenderer-1.3.0.tar.gz>
- `libepoxy-1.5.10.tar.gz`: <https://github.com/anholt/libepoxy/archive/refs/tags/1.5.10.tar.gz>

Check: `cd <out> && shasum -a 256 -c ../standalone/virgl/SHA256SUMS`.
