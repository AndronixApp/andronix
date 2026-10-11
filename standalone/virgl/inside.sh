#!/usr/bin/env bash
# Runs in the container: /dl has the pinned sources, /out gets libvirgl.so.
#
# libvirgl.so is virglrenderer's vtest server (virgl_test_server) with
# virglrenderer and libepoxy linked in statically: one executable, named
# lib*.so so Android installs it into nativeLibraryDir. The app runs it next
# to the desktop; Mesa in the guest (GALLIUM_DRIVER=virpipe) sends GL to it
# over a unix socket, and it draws with the phone's GLES through Android's
# libEGL/libGLESv2 (libepoxy dlopens them at run time).
set -euo pipefail
export SOURCE_DATE_EPOCH=1759536000 # 2025-10-04, fixed
API=26 # Android 8+, as proot (ASharedMemory)
check() { echo "$2  /dl/$1" | sha256sum -c --quiet; }
check android-ndk-r27c-linux.zip 59c2f6dc96743b5daf5d1626684640b20a6bd2b1d85b13156b90333741bad5cc
check virglrenderer-1.3.0.tar.gz 56170f8caa1bb642a2624b649e3bcca095ec2834814e5c308efc8a85a709e4ce
check libepoxy-1.5.10.tar.gz a7ced37f4102b745ac86d6a70a9da399cc139ff168ba6b8002b4d8d43c900c15

B=/build
mkdir -p $B && cd $B
unzip -q /dl/android-ndk-r27c-linux.zip
TC=$B/android-ndk-r27c/toolchains/llvm/prebuilt/linux-x86_64/bin
SYS=$B/sysroot
# 16 KB pages (Play, Android 15+), no build paths in the output.
PAGE="'-Wl,-z,max-page-size=16384', '-Wl,-z,common-page-size=16384'"
REPRO="'-ffile-prefix-map=$B=.', '-fdebug-prefix-map=$B=.'"
cat > cross.ini <<X
[binaries]
c = '$TC/aarch64-linux-android$API-clang'
cpp = '$TC/aarch64-linux-android$API-clang++'
ar = '$TC/llvm-ar'
strip = '$TC/llvm-strip'
pkgconfig = '/usr/bin/pkg-config' # meson 1.0 (bookworm) spells it pkgconfig

[properties]
pkg_config_libdir = '$SYS/lib/pkgconfig'

[built-in options]
c_args = ['-O2', $REPRO]
c_link_args = [$PAGE, '-Wl,--build-id=sha1']

[host_machine]
system = 'android'
cpu_family = 'aarch64'
cpu = 'armv8-a'
endian = 'little'
X
M="--cross-file $B/cross.ini --prefix=$SYS --libdir=lib --buildtype=release -Ddefault_library=static -Db_ndebug=true"

# libepoxy (MIT): EGL only; it dlopens libEGL.so and libGLESv2.so on Android.
tar xzf /dl/libepoxy-1.5.10.tar.gz
meson setup $M -Degl=yes -Dglx=no -Dx11=false -Dtests=false -Ddocs=false libepoxy-1.5.10 epoxy-build >/dev/null
ninja -C epoxy-build install >/dev/null

# virglrenderer (MIT): GL (vrend) over EGL+GLES, the vtest server; no Venus,
# no DRM renderers, no video.
tar xzf /dl/virglrenderer-1.3.0.tar.gz
mv virglrenderer-virglrenderer-1.3.0 virglrenderer-1.3.0
patch -s -d virglrenderer-1.3.0 -p1 < /android.patch
meson setup $M -Dplatforms=egl -Dvenus=false -Ddrm-renderers=[] -Dvideo=false -Dtests=false -Dc_link_args="['-landroid','-llog',$PAGE,'-Wl,--build-id=sha1']" virglrenderer-1.3.0 virgl-build >/dev/null
ninja -C virgl-build vtest/virgl_test_server >/dev/null

cp virgl-build/vtest/virgl_test_server libvirgl.so
$TC/llvm-strip --strip-unneeded libvirgl.so
# libandronixpresent.so: the app's end of the zero-copy present (JNI; ours, MIT).
$TC/aarch64-linux-android$API-clang -O2 -fPIC -shared -ffile-prefix-map=$B=. -Wl,-z,max-page-size=16384 -Wl,-z,common-page-size=16384 \
	-Wl,--build-id=sha1 -o libandronixpresent.so /present_jni.c -landroid -lnativewindow -lEGL -lGLESv2
$TC/llvm-strip --strip-unneeded libandronixpresent.so
mkdir -p /out && cp libvirgl.so libandronixpresent.so /out/
cp virglrenderer-1.3.0/COPYING /out/COPYING.virglrenderer
cp libepoxy-1.5.10/COPYING /out/COPYING.libepoxy
cd /out
f=libvirgl.so
echo "$f: $(file -b $f | cut -d, -f1-2); needed: $($TC/llvm-readelf -d $f | awk '/NEEDED/ {print $5}' | tr -d '[]' | tr '\n' ' '); LOAD align: $($TC/llvm-readelf -lW $f | awk '/LOAD/ {print $NF}' | sort -u | tr '\n' ' ')"
for f in libandronixpresent.so; do
	echo "$f: $(file -b $f | cut -d, -f1-2); needed: $($TC/llvm-readelf -d $f | awk '/NEEDED/ {print $5}' | tr -d '[]' | tr '\n' ' '); LOAD align: $($TC/llvm-readelf -lW $f | awk '/LOAD/ {print $NF}' | sort -u | tr '\n' ' ')"
done
sha256sum libvirgl.so libandronixpresent.so | tee SHA256SUMS
