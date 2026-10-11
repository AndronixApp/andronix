# Builds the VirGL server (virglrenderer's vtest server with libepoxy, one
# static executable) for Android arm64 (standalone Andronix:
# jniLibs/arm64-v8a/libvirgl.so), reproducibly. See build.sh.
FROM --platform=linux/amd64 debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN apt-get update -q && apt-get install -y -q --no-install-recommends meson ninja-build pkg-config python3 python3-yaml unzip patch file && rm -rf /var/lib/apt/lists/*
COPY inside.sh android.patch present_jni.c /
ENTRYPOINT ["bash", "/inside.sh"]
