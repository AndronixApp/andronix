# Andronix app: native libraries for standalone mode

In standalone mode the Andronix Android app runs Linux distros without Termux. For that it ships six native libraries (`jniLibs/arm64-v8a/`):

| file | what it is | license |
|---|---|---|
| `libandronix.so` | the `andronix` installer from this repository (`andronix-android-aarch64` of the same release) | this repository's license |
| `libproot.so` | [proot](https://github.com/termux/proot) 5.1.107.96 (Termux's fork) | GPL-2.0 |
| `libproot-loader.so` | proot's loader program | GPL-2.0 |
| `libtalloc.so` | [talloc](https://talloc.samba.org) 2.5.0 | LGPL-3.0-or-later |
| `libvirgl.so` | the VirGL server for the optional GPU acceleration: [virglrenderer](https://gitlab.freedesktop.org/virgl/virglrenderer) 1.3.0's vtest server, with [libepoxy](https://github.com/anholt/libepoxy) 1.5.10 linked in | MIT |
| `libandronixpresent.so` | the app's side of the GPU's zero-copy present (JNI), built from `virgl/present_jni.c` in this repository | MIT |

proot also includes [libandroid-shmem](https://github.com/termux/libandroid-shmem) 0.7 (BSD-3-Clause), statically linked.

## Source code

Each app release that ships these libraries has a GitHub release here named `standalone-<installer version>`, for example `standalone-2.1.0`. Its assets are:

- `proot-5.1.107.96.zip`, `talloc-2.5.0.tar.gz` and `libandroid-shmem-0.7.tar.gz`: the exact source archives used, unmodified;
- `virglrenderer-1.3.0.tar.gz` and `libepoxy-1.5.10.tar.gz`, plus `virglrenderer-android.patch` (our changes to virglrenderer for Android);
- `SHA256SUMS`: the sums of those files and of the six libraries in the app;
- `build.json`: the installer version and the sums, as recorded at build time.

The build scripts are in [`proot/`](proot/), in the tagged commit of that release:

- `Dockerfile` sets up the build environment (Debian bookworm, pinned by digest).
- `inside.sh` does the build and holds every build setting. It checks each archive's sha256, builds libandroid-shmem, then talloc (a shared library, with cross-compile answers), then proot and its loader. It links proot against those two, with 16 KB page alignment and no build paths in the output.
- `build.sh` runs it.

proot, talloc and libandroid-shmem aren't patched: every change for Android is a build setting in `proot/inside.sh`. virglrenderer is patched for Android (`virgl/android.patch`) and built by `virgl/` the same way (Dockerfile, inside.sh, build.sh; `virgl/README.md`).

## Rebuilding byte for byte

You need Docker (x86-64, or emulation) and about 5 GB of disk.

1. Download the release's three source archives into `standalone/proot/sources/`.
2. Download the Android NDK r27c for Linux into the same folder: <https://dl.google.com/android/repository/android-ndk-r27c-linux.zip> (sha256 `59c2f6dc96743b5daf5d1626684640b20a6bd2b1d85b13156b90333741bad5cc`).
3. Run:

   ```sh
   standalone/proot/build.sh out
   cd out && shasum -a 256 libproot.so libproot-loader.so libtalloc.so
   ```

   The sums match the release's `SHA256SUMS`, and the libraries in the app. The build is reproducible: it uses a fixed `SOURCE_DATE_EPOCH`, maps out build paths, and pins the base image by digest.

`libandronix.so` is the release's `andronix-android-aarch64`, built from this repository with `VERSION=<version> ci/build-go.sh`.
