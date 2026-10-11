# Builds proot, its loader and libtalloc for Android arm64 (standalone
# Andronix: jniLibs/arm64-v8a), reproducibly. See build.sh.
FROM --platform=linux/amd64 debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN apt-get update -q && apt-get install -y -q --no-install-recommends make python3 unzip binutils patchelf file gawk && rm -rf /var/lib/apt/lists/*
COPY inside.sh /inside.sh
ENTRYPOINT ["bash", "/inside.sh"]
