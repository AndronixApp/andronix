#!/usr/bin/env bash
# Runs in the container: /dl has the pinned sources, /out gets the libs.
set -euo pipefail
export SOURCE_DATE_EPOCH=1759536000 # 2025-10-04, fixed
API=26 # Android 8+: ASharedMemory (see libandroid-shmem below)
check() { echo "$2  /dl/$1" | sha256sum -c --quiet; }
check android-ndk-r27c-linux.zip 59c2f6dc96743b5daf5d1626684640b20a6bd2b1d85b13156b90333741bad5cc
check talloc-2.5.0.tar.gz 912afa237510ae542a7733998eb18a12bcda35ab6729c8e2ddb43e8d0ebab007
check proot-5.1.107.96.zip 75f654fe60dea92dabff2bf083ae8bfe4f91baa6a1a374786a6bf391015eebaa
check libandroid-shmem-0.7.tar.gz 1e5ff8459bc0a8c229dd8a94b27d119987e09ef3414331c2b5ebfff20b98e867

B=/build
mkdir -p $B && cd $B
unzip -q /dl/android-ndk-r27c-linux.zip
TC=$B/android-ndk-r27c/toolchains/llvm/prebuilt/linux-x86_64/bin
export CC="$TC/aarch64-linux-android$API-clang" AR=$TC/llvm-ar STRIP=$TC/llvm-strip OBJCOPY=$TC/llvm-objcopy
# 16 KB pages (Play, Android 15+), no build paths in the output.
PAGE="-Wl,-z,max-page-size=16384 -Wl,-z,common-page-size=16384"
REPRO="-ffile-prefix-map=$B=. -fdebug-prefix-map=$B=. -Wno-unused-command-line-argument"
mkdir -p $B/sysroot/lib $B/sysroot/include

# libandroid-shmem (BSD-3-Clause): static, inside proot.
tar xzf /dl/libandroid-shmem-0.7.tar.gz
# Termux's NDK defines _PATH_TMP as its own tmp; ours reads the app's at run
# time (keyed SysV segments keep a symlink there).
sed -i 's|sprintf(symlink_path, ASHV_KEY_SYMLINK_PATH, key);|snprintf(symlink_path, sizeof(symlink_path), "%s/ashv_key_%d", getenv("PROOT_TMP_DIR") ? getenv("PROOT_TMP_DIR") : getenv("TMPDIR") ? getenv("TMPDIR") : "/data/local/tmp", key);|' libandroid-shmem-0.7/shmem.c
grep -q 'getenv("PROOT_TMP_DIR")' libandroid-shmem-0.7/shmem.c
# Built for API 26: ASharedMemory_create instead of opening /dev/ashmem,
# which apps targeting Android 10+ may not do (shmget failed, so X had no
# MIT-SHM in the app: frames went over the socket). Needs Android 8+.
(cd libandroid-shmem-0.7 && $CC -O2 -fPIC -std=c11 -D_GNU_SOURCE -include fcntl.h -D'_PATH_TMP="/data/local/tmp/"' $REPRO -c shmem.c -o shmem.o && $AR rcsD $B/sysroot/lib/libandroid-shmem.a shmem.o && mkdir -p $B/sysroot/include/sys && cp shm.h $B/sysroot/include/sys/shm.h)

# libtalloc (LGPL-3.0+): its own libtalloc.so (jniLibs), soname to match.
tar xzf /dl/talloc-2.5.0.tar.gz
cd talloc-2.5.0
cat > cross-answers.txt <<'X'
Checking uname sysname type: "Linux"
Checking uname machine type: "dontcare"
Checking uname release type: "dontcare"
Checking uname version type: "dontcare"
Checking simple C program: OK
building library support: OK
Checking for large file support: OK
Checking for -D_FILE_OFFSET_BITS=64: OK
Checking for WORDS_BIGENDIAN: OK
Checking for C99 vsnprintf: OK
Checking for HAVE_SECURE_MKSTEMP: OK
rpath library support: OK
-Wl,--version-script support: FAIL
Checking correct behavior of strtoll: OK
Checking correct behavior of strptime: OK
Checking for HAVE_IFACE_GETIFADDRS: OK
Checking for HAVE_IFACE_IFCONF: OK
Checking for HAVE_IFACE_IFREQ: OK
Checking getconf LFS_CFLAGS: OK
Checking for large file support without additional flags: OK
Checking for working strptime: OK
Checking for HAVE_SHARED_MMAP: OK
Checking for HAVE_MREMAP: OK
Checking for HAVE_INCOHERENT_MMAP: OK
Checking getconf large file support flags work: OK
X
CFLAGS="-O2 $REPRO" LDFLAGS="$PAGE" ./configure --prefix=$B/sysroot --disable-rpath --disable-python --cross-compile --cross-answers=cross-answers.txt >/dev/null
make -s install >/dev/null 2>&1
cd $B
T=$(readlink -f sysroot/lib/libtalloc.so)
cp "$T" $B/libtalloc.so
patchelf --set-soname libtalloc.so $B/libtalloc.so
rm -f sysroot/lib/libtalloc.so*
cp $B/libtalloc.so sysroot/lib/libtalloc.so

# proot (GPL-2.0): the loader as its own file, found through PROOT_LOADER.
unzip -q /dl/proot-5.1.107.96.zip
cd proot-5.1.107.96
# Guests die with their proot (PTRACE_O_EXITKILL, Linux 3.8+). Without it,
# when Android's phantom process killer SIGKILLs proot, its guests run on
# untraced and every syscall proot's seccomp filter traps returns ENOSYS:
# the X server looped on poll() at full CPU and wrote 10 GB of log in
# 20 minutes (a16 emulator, Oct 7).
sed -i 's/PTRACE_O_TRACEEXIT);/PTRACE_O_TRACEEXIT	|\n\t\t\t\tPTRACE_O_EXITKILL);/' src/tracee/event.c
grep -q 'PTRACE_O_EXITKILL);' src/tracee/event.c
grep -q 'define PTRACE_O_EXITKILL' src/compat.h || sed -i 's/^#    ifndef PTRACE_O_TRACESYSGOOD$/#    ifndef PTRACE_O_EXITKILL\n#        define PTRACE_O_EXITKILL	0x00100000\n#    endif\n&/' src/compat.h
grep -q 'define PTRACE_O_EXITKILL' src/compat.h
# Andronix's changes: guests start with no signals blocked; kompat traps
# only what it does work for on Android (see andronix.py).
# PROOT_PLAIN=1 leaves them out (a "before" build for measurements only).
if [ "${PROOT_PLAIN:-}" = 1 ]; then echo "proot: Andronix changes left out (PROOT_PLAIN)"; else python3 /dl-stops/andronix.py && echo "proot: Andronix changes applied"; fi
# PROOT_STOPS=1 (a measurement build, never shipped): count tracer stops
# per syscall, written to <guest root>/tmp/proot-stops.<pid> on SIGUSR1
# (sv2-gpu, #69 c: what proot's CPU goes to).
if [ -n "${PROOT_STOPS:-}" ]; then
	python3 /dl-stops/stops.py
fi
# Flags through the environment: the GNUmakefile appends its own (-I.).
LOADER_LDFLAGS="$PAGE" CPPFLAGS="-I$B/sysroot/include -DARG_MAX=131072 -DVERSION=\\\"5.1.107.96\\\"" \
	CFLAGS="-O2 $REPRO -Wno-error=implicit-function-declaration" LDFLAGS="-L$B/sysroot/lib $PAGE -Wl,--build-id=sha1 -llog -landroid" \
	make -s -C src proot loader/loader CC="$CC" LD="$CC" STRIP="$STRIP" OBJCOPY="$OBJCOPY" \
	PROOT_WITH_LIBANDROID_SHMEM=true PROOT_UNBUNDLE_LOADER=/nonexistent-use-PROOT_LOADER HAS_LOADER_32BIT= 2>&1 | grep -iE "error|undefined|not found|No such" | grep -v "rpc/rpc.h" | head -20 || true
cd $B
cp proot-5.1.107.96/src/proot libproot.so
cp proot-5.1.107.96/src/loader/loader libproot-loader.so
for f in libproot.so libproot-loader.so libtalloc.so; do $STRIP --strip-unneeded $f; done
mkdir -p /out && cp libproot.so libproot-loader.so libtalloc.so /out/
cp proot-5.1.107.96/COPYING /out/COPYING.proot
cp talloc-2.5.0/LICENSE /out/LICENSE.talloc 2>/dev/null || head -30 talloc-2.5.0/talloc.h > /out/LICENSE.talloc
cp libandroid-shmem-0.7/LICENSE /out/LICENSE.libandroid-shmem
cd /out
for f in libproot.so libproot-loader.so libtalloc.so; do
	echo "$f: $(file -b $f | cut -d, -f1-2); needed: $($TC/llvm-readelf -d $f 2>/dev/null | awk '/NEEDED/ {print $5}' | tr -d '[]' | tr '\n' ' '); LOAD align: $($TC/llvm-readelf -lW $f | awk '/LOAD/ {print $NF}' | sort -u | tr '\n' ' ')"
done
sha256sum libproot.so libproot-loader.so libtalloc.so | tee SHA256SUMS
